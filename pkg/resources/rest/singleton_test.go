// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package rest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// A project's rolling-release config: one per project, no id of its own,
// created and updated by the same PATCH.
func rollingReleaseDef() Definition {
	return Definition{
		Type:           "VERCEL::Projects::RollingRelease",
		Scope:          ScopeProject,
		Singleton:      true,
		ParentProperty: "projectId",
		CollectionPath: "/v1/projects/{parent}/rolling-release/config",
		ItemPath:       "/v1/projects/{parent}/rolling-release/config",
		CreateMethod:   "PATCH",
		Fields:         []string{"enabled", "advancementType", "stages"},
	}
}

func TestSingleton_CreateUsesParentAsNativeID(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		if r.URL.Path != "/v1/projects/prj_1/rolling-release/config" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, leaked := body["projectId"]; leaked {
			t.Error("the parent is a path segment, not a body field")
		}
		if body["enabled"] != true {
			t.Errorf("body = %v", body)
		}
		_, _ = io.WriteString(w, `{"enabled":true,"advancementType":"manual"}`)
	})
	p := New(rollingReleaseDef(), c, "")
	props, _ := json.Marshal(map[string]any{"projectId": "prj_1", "enabled": true})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pr := res.ProgressResult
	if pr.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", pr.OperationStatus, pr.StatusMessage)
	}
	// No id of its own: the native id is the parent, with no second segment.
	if pr.NativeID != "prj_1" {
		t.Errorf("NativeID = %q, want prj_1", pr.NativeID)
	}
	var got map[string]any
	_ = json.Unmarshal(pr.ResourceProperties, &got)
	if got["projectId"] != "prj_1" {
		t.Errorf("parent property not restored: %v", got)
	}
	if _, ok := got["id"]; ok {
		t.Errorf("a singleton has no id of its own: %v", got)
	}
}

func TestSingleton_CreateRequiresParent(t *testing.T) {
	p := New(rollingReleaseDef(), nil, "")
	props, _ := json.Marshal(map[string]any{"enabled": true})
	res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("ErrorCode = %v, want InvalidRequest", res.ProgressResult.ErrorCode)
	}
}

// With no parent there is nothing left to key the resource by, so declaring a
// singleton account-scoped is a definition bug and must not reach the API.
func TestSingleton_AccountScopeIsRejected(t *testing.T) {
	def := rollingReleaseDef()
	def.Scope = ScopeAccount
	def.ParentProperty = ""
	p := New(def, nil, "")
	props, _ := json.Marshal(map[string]any{"enabled": true})
	res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("create ErrorCode = %v, want InvalidRequest", res.ProgressResult.ErrorCode)
	}
	read, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1"})
	if read.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("read ErrorCode = %v, want InvalidRequest", read.ErrorCode)
	}
}

func TestSingleton_ReadFillsParentPath(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/prj_1/rolling-release/config" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"enabled":true,"advancementType":"manual","updatedAt":123}`)
	})
	p := New(rollingReleaseDef(), c, "")
	res, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1"})
	if res.ErrorCode != "" {
		t.Fatalf("ErrorCode = %v", res.ErrorCode)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(res.Properties), &got)
	if got["advancementType"] != "manual" || got["projectId"] != "prj_1" {
		t.Errorf("props = %v", got)
	}
	if _, leaked := got["updatedAt"]; leaked {
		t.Errorf("unmanaged field leaked: %v", got)
	}
}

// A project that never configured the singleton must read as absent, not as an
// empty-but-present resource.
func TestSingleton_ReadWhenAbsent(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"no rolling release"}}`)
	})
	p := New(rollingReleaseDef(), c, "")
	res, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1"})
	if res.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("ErrorCode = %v, want NotFound", res.ErrorCode)
	}
}

func TestSingleton_UpdateKeepsNativeID(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/prj_1/rolling-release/config" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"enabled":false}`)
	})
	p := New(rollingReleaseDef(), c, "")
	props, _ := json.Marshal(map[string]any{"projectId": "prj_1", "enabled": false})
	res, _ := p.Update(context.Background(), &resource.UpdateRequest{NativeID: "prj_1", DesiredProperties: props})
	pr := res.ProgressResult
	if pr.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", pr.OperationStatus, pr.StatusMessage)
	}
	if pr.NativeID != "prj_1" {
		t.Errorf("NativeID = %q", pr.NativeID)
	}
}

func TestSingleton_Delete(t *testing.T) {
	called := false
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/projects/prj_1/rolling-release/config" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	p := New(rollingReleaseDef(), c, "")
	res, _ := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "prj_1"})
	if !called {
		t.Fatal("delete was never issued")
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v", res.ProgressResult.OperationStatus)
	}
}

// Some singletons cannot be removed at all — only reset by another write.
func TestSingleton_DeleteUnsupported(t *testing.T) {
	def := rollingReleaseDef()
	def.NoDelete = true
	p := New(def, nil, "")
	res, _ := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "prj_1"})
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("ErrorCode = %v, want InvalidRequest", res.ProgressResult.ErrorCode)
	}
}

// Discovery enumerates the parents: there is exactly one singleton per parent.
func TestSingleton_ListEnumeratesParents(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v10/projects" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"projects":[{"id":"prj_1"},{"id":"prj_2"}],"pagination":{"count":2,"next":null}}`)
	})
	p := New(rollingReleaseDef(), c, "")
	res, _ := p.List(context.Background(), &resource.ListRequest{})
	if len(res.NativeIDs) != 2 || res.NativeIDs[0] != "prj_1" || res.NativeIDs[1] != "prj_2" {
		t.Errorf("NativeIDs = %v", res.NativeIDs)
	}
}

func TestSingleton_ListHonoursProjectScope(t *testing.T) {
	c := clientFor(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("scoped List must not call the API, got %s", r.URL.Path)
	})
	p := New(rollingReleaseDef(), c, "prj_9")
	res, _ := p.List(context.Background(), &resource.ListRequest{})
	if len(res.NativeIDs) != 1 || res.NativeIDs[0] != "prj_9" {
		t.Errorf("NativeIDs = %v", res.NativeIDs)
	}
}

// A singleton under a non-project parent, e.g. an Edge Config's schema.
func TestSingleton_ParentScope(t *testing.T) {
	def := Definition{
		Type:            "VERCEL::GlobalConfig::Schema",
		Scope:           ScopeParent,
		Singleton:       true,
		ParentProperty:  "edgeConfigId",
		ParentListPath:  "/v1/global-config",
		CollectionPath:  "/v1/global-config/{parent}/schema",
		ItemPath:        "/v1/global-config/{parent}/schema",
		ParentListField: "configs",
		CreateMethod:    "POST",
		Fields:          []string{"definition"},
	}
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/global-config":
			_, _ = io.WriteString(w, `{"configs":[{"id":"ecfg_1"},{"id":"ecfg_2"}]}`)
		case "/v1/global-config/ecfg_1/schema":
			_, _ = io.WriteString(w, `{"definition":{"type":"object"}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	p := New(def, c, "")

	list, _ := p.List(context.Background(), &resource.ListRequest{})
	if len(list.NativeIDs) != 2 || list.NativeIDs[0] != "ecfg_1" {
		t.Errorf("NativeIDs = %v", list.NativeIDs)
	}

	read, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "ecfg_1"})
	var got map[string]any
	_ = json.Unmarshal([]byte(read.Properties), &got)
	if got["edgeConfigId"] != "ecfg_1" {
		t.Errorf("props = %v", got)
	}
}

// A singleton whose write is asynchronous still polls through Status().
func TestSingleton_AsyncCreate(t *testing.T) {
	def := rollingReleaseDef()
	def.Async = &AsyncSpec{StatusField: "state", Pending: []string{"applying"}, Ready: []string{"applied"}}
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"enabled":true,"state":"applying"}`)
	})
	p := New(def, c, "")
	props, _ := json.Marshal(map[string]any{"projectId": "prj_1", "enabled": true})
	res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	pr := res.ProgressResult
	if pr.OperationStatus != resource.OperationStatusInProgress {
		t.Fatalf("status = %v, want InProgress", pr.OperationStatus)
	}
	if pr.RequestID != "prj_1" || pr.NativeID != "prj_1" {
		t.Errorf("RequestID/NativeID = %q/%q", pr.RequestID, pr.NativeID)
	}
}
