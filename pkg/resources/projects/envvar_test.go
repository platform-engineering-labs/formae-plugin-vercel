// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package projects

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestEnvVar_Create(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v10/projects/prj_1/env" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["key"] != "API_URL" || body["value"] != "https://example.com" || body["type"] != "plain" {
			t.Errorf("body = %v", body)
		}
		if _, ok := body["projectId"]; ok {
			t.Error("projectId is a path segment, not a body field")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"created":{"id":"env_1","key":"API_URL","value":"https://example.com","type":"plain","target":["production"]},"failed":[]}`)
	})
	e := &EnvVar{Client: c}
	props, _ := json.Marshal(EnvVarProperties{
		ProjectID: "prj_1", Key: "API_URL", Value: "https://example.com",
		Type: "plain", Target: []string{"production"},
	})
	res, err := e.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.ProgressResult.NativeID != "prj_1/env_1" {
		t.Errorf("NativeID = %q, want prj_1/env_1", res.ProgressResult.NativeID)
	}
}

// A 201 whose `failed` array is non-empty is still a failure.
func TestEnvVar_Create_PartialFailureIsFailure(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"created":null,"failed":[{"error":{"code":"ENV_ALREADY_EXISTS","message":"already exists","key":"API_URL"}}]}`)
	})
	e := &EnvVar{Client: c}
	props, _ := json.Marshal(EnvVarProperties{ProjectID: "prj_1", Key: "API_URL", Value: "v", Type: "plain", Target: []string{"production"}})
	res, _ := e.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if res.ProgressResult.OperationStatus != resource.OperationStatusFailure {
		t.Fatalf("status = %v, want Failure", res.ProgressResult.OperationStatus)
	}
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeAlreadyExists {
		t.Errorf("ErrorCode = %v, want AlreadyExists", res.ProgressResult.ErrorCode)
	}
}

func TestEnvVar_Create_RequiresFields(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("API must not be called with incomplete properties")
	})
	e := &EnvVar{Client: c}
	for name, props := range map[string]EnvVarProperties{
		"no project": {Key: "K", Value: "v", Type: "plain", Target: []string{"production"}},
		"no key":     {ProjectID: "prj_1", Value: "v", Type: "plain", Target: []string{"production"}},
		"no scope":   {ProjectID: "prj_1", Key: "K", Value: "v", Type: "plain"},
	} {
		raw, _ := json.Marshal(props)
		res, _ := e.Create(context.Background(), &resource.CreateRequest{Properties: raw})
		if res.ProgressResult.ErrorCode != resource.OperationErrorCodeInvalidRequest {
			t.Errorf("%s: ErrorCode = %v, want InvalidRequest", name, res.ProgressResult.ErrorCode)
		}
	}
}

// Type defaults to "encrypted" — the API requires a type, and encrypted is what
// the dashboard uses for a plain "add variable".
func TestEnvVar_Create_DefaultsType(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["type"] != "encrypted" {
			t.Errorf("type = %v, want encrypted", body["type"])
		}
		_, _ = io.WriteString(w, `{"created":{"id":"env_1","key":"K","type":"encrypted"},"failed":[]}`)
	})
	e := &EnvVar{Client: c}
	props, _ := json.Marshal(EnvVarProperties{ProjectID: "prj_1", Key: "K", Value: "v", Target: []string{"production"}})
	if _, err := e.Create(context.Background(), &resource.CreateRequest{Properties: props}); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func TestEnvVar_Read(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v10/projects/prj_1/env" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("decrypt") != "true" {
			t.Error("decrypt=true is required or values read back redacted")
		}
		_, _ = io.WriteString(w, `{"envs":[
			{"id":"env_other","key":"OTHER","value":"x","type":"plain","target":["preview"]},
			{"id":"env_1","key":"API_URL","value":"https://example.com","type":"plain","target":["production"],"comment":"hi"}
		]}`)
	})
	e := &EnvVar{Client: c}
	res, err := e.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1/env_1", ResourceType: ResourceTypeEnvVar})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var got EnvVarProperties
	if err := json.Unmarshal([]byte(res.Properties), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Key != "API_URL" || got.Value != "https://example.com" || got.Comment != "hi" {
		t.Errorf("props = %+v", got)
	}
	if got.ProjectID != "prj_1" {
		t.Errorf("projectId = %q, want prj_1", got.ProjectID)
	}
	if len(got.Target) != 1 || got.Target[0] != "production" {
		t.Errorf("target = %v", got.Target)
	}
}

// The API documents `target` as either a list or a bare string.
func TestEnvVar_Read_ScalarTarget(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"envs":[{"id":"env_1","key":"K","value":"v","type":"plain","target":"production"}]}`)
	})
	e := &EnvVar{Client: c}
	res, _ := e.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1/env_1"})
	var got EnvVarProperties
	_ = json.Unmarshal([]byte(res.Properties), &got)
	if len(got.Target) != 1 || got.Target[0] != "production" {
		t.Errorf("target = %v, want [production]", got.Target)
	}
}

func TestEnvVar_Read_NotFoundWhenAbsentFromList(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"envs":[{"id":"env_other","key":"OTHER","value":"x","type":"plain"}]}`)
	})
	e := &EnvVar{Client: c}
	res, err := e.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1/env_gone"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("ErrorCode = %v, want NotFound", res.ErrorCode)
	}
}

func TestEnvVar_Read_BadNativeID(t *testing.T) {
	e := &EnvVar{}
	res, _ := e.Read(context.Background(), &resource.ReadRequest{NativeID: "no-slash"})
	if res.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("ErrorCode = %v, want InvalidRequest", res.ErrorCode)
	}
}

func TestEnvVar_Update(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v9/projects/prj_1/env/env_1" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["value"] != "https://new.example.com" {
			t.Errorf("value = %v", body["value"])
		}
		if body["comment"] != "updated" {
			t.Errorf("comment = %v", body["comment"])
		}
		_, _ = io.WriteString(w, `{"id":"env_1","key":"API_URL","value":"https://new.example.com","type":"plain","target":["production"],"comment":"updated"}`)
	})
	e := &EnvVar{Client: c}
	props, _ := json.Marshal(EnvVarProperties{
		ProjectID: "prj_1", Key: "API_URL", Value: "https://new.example.com",
		Type: "plain", Target: []string{"production"}, Comment: "updated",
	})
	res, err := e.Update(context.Background(), &resource.UpdateRequest{NativeID: "prj_1/env_1", DesiredProperties: props})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", res.ProgressResult.OperationStatus, res.ProgressResult.StatusMessage)
	}
}

func TestEnvVar_Delete(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v9/projects/prj_1/env/env_1" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	e := &EnvVar{Client: c}
	res, _ := e.Delete(context.Background(), &resource.DeleteRequest{NativeID: "prj_1/env_1"})
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v", res.ProgressResult.OperationStatus)
	}
}

func TestEnvVar_Delete_Idempotent(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"gone"}}`)
	})
	e := &EnvVar{Client: c}
	res, _ := e.Delete(context.Background(), &resource.DeleteRequest{NativeID: "prj_1/env_gone"})
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("deleting a missing env var must succeed, got %v", res.ProgressResult.OperationStatus)
	}
}

// With ProjectScope set, List must not enumerate every project the token sees.
func TestEnvVar_List_ScopedToProject(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v10/projects" {
			t.Error("scoped List must not enumerate projects")
		}
		if r.URL.Path != "/v10/projects/prj_1/env" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"envs":[{"id":"env_1","key":"A"},{"id":"env_2","key":"B"}]}`)
	})
	e := &EnvVar{Client: c, ProjectScope: "prj_1"}
	res, err := e.List(context.Background(), &resource.ListRequest{ResourceType: ResourceTypeEnvVar})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"prj_1/env_1", "prj_1/env_2"}
	if len(res.NativeIDs) != 2 || res.NativeIDs[0] != want[0] || res.NativeIDs[1] != want[1] {
		t.Errorf("NativeIDs = %v, want %v", res.NativeIDs, want)
	}
}

func TestEnvVar_List_WalksAllProjects(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v10/projects":
			_, _ = io.WriteString(w, `{"projects":[{"id":"prj_1"},{"id":"prj_2"}],"pagination":{"count":2,"next":null}}`)
		case "/v10/projects/prj_1/env":
			_, _ = io.WriteString(w, `{"envs":[{"id":"env_1"}]}`)
		case "/v10/projects/prj_2/env":
			_, _ = io.WriteString(w, `{"envs":[{"id":"env_2"}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	e := &EnvVar{Client: c}
	res, _ := e.List(context.Background(), &resource.ListRequest{})
	if len(res.NativeIDs) != 2 {
		t.Fatalf("NativeIDs = %v", res.NativeIDs)
	}
}

func TestRegistry_BothTypesRegistered(t *testing.T) {
	for _, rt := range []string{ResourceTypeProject, ResourceTypeEnvVar} {
		if !registry.Has(rt) {
			t.Errorf("%s not registered", rt)
		}
		if len(registry.GetOperations(rt)) != 5 {
			t.Errorf("%s: expected 5 operations, got %v", rt, registry.GetOperations(rt))
		}
	}
}
