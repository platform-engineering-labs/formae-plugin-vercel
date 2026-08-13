// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package projects

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/registry"
	vercelapi "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/transport/vercel"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// ResourceTypeEnvVar is the formae type id for a project environment variable.
const ResourceTypeEnvVar = "VERCEL::Projects::EnvironmentVariable"

// defaultEnvVarType matches what the Vercel dashboard uses when you add a plain
// variable. The API requires `type`, so an unset one has to default to
// something.
const defaultEnvVarType = "encrypted"

func init() {
	registry.Register(
		ResourceTypeEnvVar,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(c *vercelapi.Client, cfg *registry.TargetConfig) prov.Provisioner {
			return &EnvVar{Client: c, ProjectScope: cfg.ProjectID}
		},
	)
}

// EnvVar provisions VERCEL::Projects::EnvironmentVariable.
//
// Native id: `{projectId}/{envId}`.
type EnvVar struct {
	Client *vercelapi.Client
	// ProjectScope, when set, restricts List() to a single project instead of
	// walking every project the token can see.
	ProjectScope string
}

// stringList decodes a JSON value that the Vercel API documents as either an
// array of strings or a bare string (`target` is spelled both ways).
type stringList []string

func (s *stringList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*s = stringList{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

// EnvVarProperties is the Forma-facing shape (matches the PKL schema).
type EnvVarProperties struct {
	ID                   string   `json:"id,omitempty"`
	ProjectID            string   `json:"projectId,omitempty"`
	Key                  string   `json:"key,omitempty"`
	Value                string   `json:"value,omitempty"`
	Type                 string   `json:"variableType,omitempty"`
	Target               []string `json:"targets,omitempty"`
	GitBranch            string   `json:"gitBranch,omitempty"`
	Comment              string   `json:"comment,omitempty"`
	CustomEnvironmentIDs []string `json:"customEnvironmentIds,omitempty"`
}

// envVarAPI is the subset of Vercel's env-var object we manage.
type envVarAPI struct {
	ID                   string     `json:"id"`
	Key                  string     `json:"key"`
	Value                string     `json:"value"`
	Type                 string     `json:"type"`
	Target               stringList `json:"target"`
	GitBranch            string     `json:"gitBranch"`
	Comment              string     `json:"comment"`
	CustomEnvironmentIDs []string   `json:"customEnvironmentIds"`
}

func (a envVarAPI) toProps(projectID string) EnvVarProperties {
	return EnvVarProperties{
		ID: a.ID, ProjectID: projectID, Key: a.Key, Value: a.Value, Type: a.Type,
		Target: a.Target, GitBranch: a.GitBranch, Comment: a.Comment,
		CustomEnvironmentIDs: a.CustomEnvironmentIDs,
	}
}

// createEnvResponse is the shape of POST .../env: `created` is an object when a
// single variable was posted and an array when several were, and a non-empty
// `failed` means the write did not happen even though the status was 2xx.
type createEnvResponse struct {
	Created json.RawMessage `json:"created"`
	Failed  []struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"failed"`
}

func (r createEnvResponse) firstCreated() (envVarAPI, error) {
	var one envVarAPI
	if len(r.Created) == 0 || string(r.Created) == "null" {
		return one, fmt.Errorf("create response contained no created variable")
	}
	if err := json.Unmarshal(r.Created, &one); err == nil && one.ID != "" {
		return one, nil
	}
	var many []envVarAPI
	if err := json.Unmarshal(r.Created, &many); err != nil || len(many) == 0 {
		return one, fmt.Errorf("create response contained no created variable")
	}
	return many[0], nil
}

func (e *EnvVar) Create(ctx context.Context, req *resource.CreateRequest) (*resource.CreateResult, error) {
	var desired EnvVarProperties
	if err := json.Unmarshal(req.Properties, &desired); err != nil {
		return prov.FailCreate(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}
	if desired.ProjectID == "" || desired.Key == "" {
		return prov.FailCreate(resource.OperationErrorCodeInvalidRequest, "projectId and key are required"), nil
	}
	// The API requires at least one of target / customEnvironmentIds.
	if len(desired.Target) == 0 && len(desired.CustomEnvironmentIDs) == 0 {
		return prov.FailCreate(resource.OperationErrorCodeInvalidRequest,
			"one of target or customEnvironmentIds is required"), nil
	}

	body := e.writeBody(desired)
	var created createEnvResponse
	if err := e.Client.Do(ctx, vercelapi.Request{
		Method: "POST",
		Path:   "/v10/projects/" + desired.ProjectID + "/env",
		Body:   body,
	}, &created); err != nil {
		return prov.FailCreate(vercelapi.ClassifyError(err), err.Error()), nil
	}
	if len(created.Failed) > 0 {
		f := created.Failed[0].Error
		return prov.FailCreate(classifyCreateFailure(f.Code, f.Message), f.Message), nil
	}
	one, err := created.firstCreated()
	if err != nil {
		return prov.FailCreate(resource.OperationErrorCodeServiceInternalError, err.Error()), nil
	}
	return prov.SuccessCreate(prov.JoinTwoPart(desired.ProjectID, one.ID), one.toProps(desired.ProjectID)), nil
}

// classifyCreateFailure maps an entry of the `failed` array, which carries only
// a code and a message — no HTTP status to classify from.
func classifyCreateFailure(code, message string) resource.OperationErrorCode {
	if vercelapi.IsAlreadyExistsMessage(code, message) {
		return resource.OperationErrorCodeAlreadyExists
	}
	return resource.OperationErrorCodeInvalidRequest
}

// writeBody builds the create/update payload. Empty optional fields are omitted
// rather than sent as empty strings, which the API rejects.
func (e *EnvVar) writeBody(p EnvVarProperties) map[string]any {
	typ := p.Type
	if typ == "" {
		typ = defaultEnvVarType
	}
	body := map[string]any{
		"key":   p.Key,
		"value": p.Value,
		"type":  typ,
	}
	if len(p.Target) > 0 {
		body["target"] = p.Target
	}
	if len(p.CustomEnvironmentIDs) > 0 {
		body["customEnvironmentIds"] = p.CustomEnvironmentIDs
	}
	if p.GitBranch != "" {
		body["gitBranch"] = p.GitBranch
	}
	if p.Comment != "" {
		body["comment"] = p.Comment
	}
	return body
}

// Read lists the project's variables with decrypt=true and picks ours out.
// There is no single-variable endpoint that returns the full object with a
// usable value, so the list is the only faithful read.
func (e *EnvVar) Read(ctx context.Context, req *resource.ReadRequest) (*resource.ReadResult, error) {
	projectID, envID, err := prov.ParseTwoPart(req.NativeID)
	if err != nil {
		return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: resource.OperationErrorCodeInvalidRequest}, nil
	}
	envs, err := e.listEnvs(ctx, projectID)
	if err != nil {
		return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: vercelapi.ClassifyError(err)}, nil
	}
	for _, env := range envs {
		if env.ID == envID {
			return &resource.ReadResult{
				ResourceType: req.ResourceType,
				Properties:   string(prov.MustMarshal(env.toProps(projectID))),
			}, nil
		}
	}
	return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: resource.OperationErrorCodeNotFound}, nil
}

func (e *EnvVar) Update(ctx context.Context, req *resource.UpdateRequest) (*resource.UpdateResult, error) {
	projectID, envID, err := prov.ParseTwoPart(req.NativeID)
	if err != nil {
		return prov.FailUpdate(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}
	var desired EnvVarProperties
	if err := json.Unmarshal(req.DesiredProperties, &desired); err != nil {
		return prov.FailUpdate(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}
	var updated envVarAPI
	if err := e.Client.Do(ctx, vercelapi.Request{
		Method: "PATCH",
		Path:   "/v9/projects/" + projectID + "/env/" + envID,
		Body:   e.writeBody(desired),
	}, &updated); err != nil {
		return prov.FailUpdate(vercelapi.ClassifyError(err), err.Error()), nil
	}
	return prov.SuccessUpdate(req.NativeID, updated.toProps(projectID)), nil
}

func (e *EnvVar) Delete(ctx context.Context, req *resource.DeleteRequest) (*resource.DeleteResult, error) {
	projectID, envID, err := prov.ParseTwoPart(req.NativeID)
	if err != nil {
		return prov.FailDelete(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}
	if err := e.Client.Do(ctx, vercelapi.Request{
		Method: "DELETE",
		Path:   "/v9/projects/" + projectID + "/env/" + envID,
	}, nil); err != nil {
		if vercelapi.IsNotFound(err) {
			return prov.SuccessDelete(req.NativeID), nil
		}
		return prov.FailDelete(vercelapi.ClassifyError(err), err.Error()), nil
	}
	return prov.SuccessDelete(req.NativeID), nil
}

func (e *EnvVar) Status(_ context.Context, req *resource.StatusRequest) (*resource.StatusResult, error) {
	return prov.SuccessStatus(req.NativeID), nil
}

func (e *EnvVar) List(ctx context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	projectIDs := []string{e.ProjectScope}
	if e.ProjectScope == "" {
		ids, err := listProjectIDs(ctx, e.Client)
		if err != nil {
			return &resource.ListResult{NativeIDs: []string{}}, nil
		}
		projectIDs = ids
	}

	nativeIDs := []string{}
	for _, projectID := range projectIDs {
		envs, err := e.listEnvs(ctx, projectID)
		if err != nil {
			// One unreadable project must not abort discovery of the rest.
			continue
		}
		for _, env := range envs {
			if env.ID != "" {
				nativeIDs = append(nativeIDs, prov.JoinTwoPart(projectID, env.ID))
			}
		}
	}
	return &resource.ListResult{NativeIDs: nativeIDs}, nil
}

// envListResponse covers the documented shapes of GET .../env: an `envs`
// envelope (with or without pagination) or a bare array.
type envListResponse struct {
	Envs []envVarAPI `json:"envs"`
}

func (e *EnvVar) listEnvs(ctx context.Context, projectID string) ([]envVarAPI, error) {
	var raw json.RawMessage
	if err := e.Client.Do(ctx, vercelapi.Request{
		Method: "GET",
		Path:   "/v10/projects/" + projectID + "/env",
		Query:  map[string]string{"decrypt": "true"},
	}, &raw); err != nil {
		return nil, err
	}
	if len(raw) > 0 && raw[0] == '[' {
		var envs []envVarAPI
		if err := json.Unmarshal(raw, &envs); err != nil {
			return nil, err
		}
		return envs, nil
	}
	var wrapped envListResponse
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Envs, nil
}
