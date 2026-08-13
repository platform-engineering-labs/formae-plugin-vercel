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

	var created ProjectProperties
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
	return prov.SuccessCreate(created.ID, created), nil
}

func (p *Project) Read(ctx context.Context, req *resource.ReadRequest) (*resource.ReadResult, error) {
	var got ProjectProperties
	if err := p.Client.Do(ctx, vercelapi.Request{
		Method: "GET",
		Path:   "/v9/projects/" + req.NativeID,
	}, &got); err != nil {
		return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: vercelapi.ClassifyError(err)}, nil
	}
	return &resource.ReadResult{
		ResourceType: req.ResourceType,
		Properties:   string(prov.MustMarshal(got)),
	}, nil
}

func (p *Project) Update(ctx context.Context, req *resource.UpdateRequest) (*resource.UpdateResult, error) {
	var desired ProjectProperties
	if err := json.Unmarshal(req.DesiredProperties, &desired); err != nil {
		return prov.FailUpdate(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}

	// PATCH is declarative: every mutable field is sent on every update so a
	// field the user removed is cleared (nil marshals to null, which is how
	// Vercel spells "auto-detect"). `name` is createOnly and never sent.
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

	var updated ProjectProperties
	if err := p.Client.Do(ctx, vercelapi.Request{
		Method: "PATCH",
		Path:   "/v9/projects/" + req.NativeID,
		Body:   body,
	}, &updated); err != nil {
		return prov.FailUpdate(vercelapi.ClassifyError(err), err.Error()), nil
	}
	return prov.SuccessUpdate(req.NativeID, updated), nil
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
		return &resource.ListResult{NativeIDs: []string{}}, err
	}
	return &resource.ListResult{NativeIDs: ids}, nil
}
