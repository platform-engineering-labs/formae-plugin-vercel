// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

// Package projects implements the VERCEL::Projects::* resource types.
package projects

import (
	"context"
	"encoding/json"

	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/registry"
	vercelapi "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/transport/vercel"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// ResourceTypeProject is the formae type id for a Vercel project.
const ResourceTypeProject = "VERCEL::Projects::Project"

func init() {
	registry.Register(
		ResourceTypeProject,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(c *vercelapi.Client, _ *registry.TargetConfig) prov.Provisioner {
			return &Project{Client: c}
		},
	)
}

// Project provisions VERCEL::Projects::Project.
//
// Native id: the Vercel project id (`prj_…`). Team scope lives on the client,
// not in the id — a project cannot move between teams without recreation.
type Project struct {
	Client *vercelapi.Client
}

// ProjectProperties is the Forma-facing shape. Field names match the PKL schema
// and, deliberately, the Vercel API's own camelCase.
//
// Nullable build settings are pointers: Vercel uses null to mean "auto-detect",
// which is a different state from "empty string".
type ProjectProperties struct {
	ID              string  `json:"id,omitempty"`
	Name            string  `json:"name,omitempty"`
	AccountID       string  `json:"accountId,omitempty"`
	Framework       *string `json:"framework,omitempty"`
	BuildCommand    *string `json:"buildCommand,omitempty"`
	DevCommand      *string `json:"devCommand,omitempty"`
	InstallCommand  *string `json:"installCommand,omitempty"`
	OutputDirectory *string `json:"outputDirectory,omitempty"`
	RootDirectory   *string `json:"rootDirectory,omitempty"`
	NodeVersion     *string `json:"nodeVersion,omitempty"`

	// GitRepository is asymmetric on the wire: it is written as
	// `gitRepository` and read back as `link`, in a shape that differs per
	// provider. projectLink does that translation; this field is what the forma
	// declares and what Read must reproduce.
	GitRepository *GitRepository `json:"gitRepository,omitempty"`
}

// GitRepository is the connected source repository. Create-only: the documented
// PATCH /v9/projects/{idOrName} body has 44 properties and none of them is the
// git link, so a change here is a replace.
//
// Terraform's git_repository also carries production_branch and deploy_hooks.
// Neither has a documented endpoint — they are not in the REST reference nor in
// the machine-readable spec (288 paths, checked) — so they are out of scope
// under the documented-endpoints-only policy in docs/RESOURCES.md.
type GitRepository struct {
	// Type is the provider: github, github-limited, gitlab, bitbucket, vercel,
	// cursor-origin.
	Type string `json:"type"`
	// Repo is "owner/name", e.g. "vercel/next.js".
	Repo string `json:"repo"`
}

// projectLink is the `link` object Vercel answers with. Each provider spells
// the owner and the repository differently, and every variant folds back into
// GitRepository.Repo as "owner/name".
type projectLink struct {
	Type string `json:"type"`
	// Sourceless marks a link whose repository has been disconnected. Vercel
	// keeps the object; there is no repository.
	Sourceless bool `json:"sourceless"`

	// github, github-limited, github-custom-host, vercel
	Org  string `json:"org"`
	Repo string `json:"repo"`

	// gitlab
	ProjectNamespace string `json:"projectNamespace"`
	ProjectName      string `json:"projectName"`

	// bitbucket
	Owner string `json:"owner"`
	Slug  string `json:"slug"`
}

// gitRepository folds a link into the declared shape, or returns nil when the
// project has no usable repository. Reporting a repository that is not there
// would make every plain project drift on every sync.
func (l *projectLink) gitRepository() *GitRepository {
	if l == nil || l.Type == "" || l.Sourceless {
		return nil
	}
	var owner, name string
	switch {
	case l.ProjectNamespace != "" || l.ProjectName != "":
		owner, name = l.ProjectNamespace, l.ProjectName
	case l.Owner != "" || l.Slug != "":
		owner, name = l.Owner, l.Slug
	default:
		owner, name = l.Org, l.Repo
	}
	if owner == "" || name == "" {
		// A provider variant nobody mapped. Silently inventing half a
		// repository name is worse than reporting none.
		return nil
	}
	return &GitRepository{Type: l.Type, Repo: owner + "/" + name}
}

// projectResponse is the API's own shape: everything ProjectProperties has,
// except the repository, which arrives as `link`.
type projectResponse struct {
	ProjectProperties
	Link *projectLink `json:"link"`
}

// properties is the response as the forma declares it.
func (r *projectResponse) properties() ProjectProperties {
	out := r.ProjectProperties
	out.GitRepository = r.Link.gitRepository()
	return out
}

func (p *Project) Create(ctx context.Context, req *resource.CreateRequest) (*resource.CreateResult, error) {
	var desired ProjectProperties
	if err := json.Unmarshal(req.Properties, &desired); err != nil {
		return prov.FailCreate(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}
	if desired.Name == "" {
		return prov.FailCreate(resource.OperationErrorCodeInvalidRequest, "name is required"), nil
	}

	// Only `name` is required by POST /v11/projects; everything else is sent
	// when set. nodeVersion is absent from the documented create body, so it is
	// applied by the first update instead.
	body := map[string]any{"name": desired.Name}
	for field, v := range map[string]*string{
		"framework":       desired.Framework,
		"buildCommand":    desired.BuildCommand,
		"devCommand":      desired.DevCommand,
		"installCommand":  desired.InstallCommand,
		"outputDirectory": desired.OutputDirectory,
		"rootDirectory":   desired.RootDirectory,
	} {
		if v != nil {
			body[field] = *v
		}
	}

	// The repository can only be attached at create time, and both halves are
	// required together by POST /v11/projects.
	if gr := desired.GitRepository; gr != nil {
		if gr.Type == "" || gr.Repo == "" {
			return prov.FailCreate(resource.OperationErrorCodeInvalidRequest,
				"gitRepository needs both type and repo"), nil
		}
		body["gitRepository"] = map[string]any{"type": gr.Type, "repo": gr.Repo}
	}

	var created projectResponse
	if err := p.Client.Do(ctx, vercelapi.Request{
		Method: "POST",
		Path:   "/v11/projects",
		Body:   body,
	}, &created); err != nil {
		return prov.FailCreate(vercelapi.ClassifyError(err), err.Error()), nil
	}
	if created.ID == "" {
		return prov.FailCreate(resource.OperationErrorCodeServiceInternalError, "create response missing id"), nil
	}
	return prov.SuccessCreate(created.ID, created.properties()), nil
}

func (p *Project) Read(ctx context.Context, req *resource.ReadRequest) (*resource.ReadResult, error) {
	var got projectResponse
	if err := p.Client.Do(ctx, vercelapi.Request{
		Method: "GET",
		Path:   "/v9/projects/" + req.NativeID,
	}, &got); err != nil {
		return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: vercelapi.ClassifyError(err)}, nil
	}
	return &resource.ReadResult{
		ResourceType: req.ResourceType,
		Properties:   string(prov.MustMarshal(got.properties())),
	}, nil
}

func (p *Project) Update(ctx context.Context, req *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var desired ProjectProperties
	if err := json.Unmarshal(req.DesiredProperties, &desired); err != nil {
		return prov.FailUpdate(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}

	// PATCH is declarative: every mutable field is sent on every update so a
	// field the user removed is cleared (nil marshals to null, which is how
	// Vercel spells "auto-detect"). `name` and `gitRepository` are createOnly
	// and never sent.
	body := map[string]any{
		"framework":       desired.Framework,
		"buildCommand":    desired.BuildCommand,
		"devCommand":      desired.DevCommand,
		"installCommand":  desired.InstallCommand,
		"outputDirectory": desired.OutputDirectory,
		"rootDirectory":   desired.RootDirectory,
	}
	if desired.NodeVersion != nil {
		// Unlike the others, nodeVersion has no meaningful null; omit it rather
		// than asking Vercel to unset it.
		body["nodeVersion"] = *desired.NodeVersion
	}

	var updated projectResponse
	if err := p.Client.Do(ctx, vercelapi.Request{
		Method: "PATCH",
		Path:   "/v9/projects/" + req.NativeID,
		Body:   body,
	}, &updated); err != nil {
		return prov.FailUpdate(vercelapi.ClassifyError(err), err.Error()), nil
	}
	return prov.SuccessUpdate(req.NativeID, updated.properties()), nil
}

func (p *Project) Delete(ctx context.Context, req *resource.DeleteRequest) (*resource.DeleteResult, error) {
	if err := p.Client.Do(ctx, vercelapi.Request{
		Method: "DELETE",
		Path:   "/v9/projects/" + req.NativeID,
	}, nil); err != nil {
		if vercelapi.IsNotFound(err) {
			return prov.SuccessDelete(req.NativeID), nil
		}
		return prov.FailDelete(vercelapi.ClassifyError(err), err.Error()), nil
	}
	return prov.SuccessDelete(req.NativeID), nil
}

func (p *Project) Status(_ context.Context, req *resource.StatusRequest) (*resource.StatusResult, error) {
	// Project create/update/delete are synchronous; nothing to poll.
	return prov.SuccessStatus(req.NativeID), nil
}

func (p *Project) List(ctx context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	ids, err := prov.ProjectIDs(ctx, p.Client, "")
	if err != nil {
		return &resource.ListResult{NativeIDs: []string{}}, nil
	}
	return &resource.ListResult{NativeIDs: ids}, nil
}
