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
	"net/http/httptest"
	"testing"

	vercelapi "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/transport/vercel"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func clientFor(t *testing.T, h http.HandlerFunc) *vercelapi.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := vercelapi.NewClient(vercelapi.Config{BaseURL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// A plain account-scoped resource: POST /v1/drains, item at /v1/drains/{id}.
func drainDef() Definition {
	return Definition{
		Type:           "VERCEL::Drains::Drain",
		Scope:          ScopeAccount,
		CollectionPath: "/v1/drains",
		ItemPath:       "/v1/drains/{id}",
		ListField:      "drains",
		Fields:         []string{"name", "url", "deliveryFormat"},
		CreateOnly:     []string{"deliveryFormat"},
	}
}

func TestCreate_AccountScope(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/drains" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "ship-logs" || body["deliveryFormat"] != "json" {
			t.Errorf("body = %v", body)
		}
		_, _ = io.WriteString(w, `{"id":"drain_1","name":"ship-logs","url":"https://x","deliveryFormat":"json"}`)
	})
	p := New(drainDef(), c, "")
	props, _ := json.Marshal(map[string]any{"name": "ship-logs", "url": "https://x", "deliveryFormat": "json"})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.ProgressResult.NativeID != "drain_1" {
		t.Errorf("NativeID = %q", res.ProgressResult.NativeID)
	}
}

// Read must surface only declared fields, plus id, or unmanaged server-side
// fields show up as permanent drift.
func TestRead_FiltersToDeclaredFields(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/drains/drain_1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":"drain_1","name":"ship-logs","url":"https://x",
			"deliveryFormat":"json","createdAt":123,"ownerId":"acc_1","secret":"shh"}`)
	})
	p := New(drainDef(), c, "")
	res, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "drain_1"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(res.Properties), &got)
	for _, want := range []string{"id", "name", "url", "deliveryFormat"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	for _, unwanted := range []string{"createdAt", "ownerId", "secret"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("unmanaged field %q leaked", unwanted)
		}
	}
}

func TestRead_NotFound(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"nope"}}`)
	})
	p := New(drainDef(), c, "")
	res, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "drain_x"})
	if res.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("ErrorCode = %v", res.ErrorCode)
	}
}

func TestUpdate_OmitsCreateOnlyFields(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s", r.Method)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, present := body["deliveryFormat"]; present {
			t.Error("createOnly field must not be sent on update")
		}
		if body["name"] != "renamed" {
			t.Errorf("name = %v", body["name"])
		}
		_, _ = io.WriteString(w, `{"id":"drain_1","name":"renamed"}`)
	})
	p := New(drainDef(), c, "")
	props, _ := json.Marshal(map[string]any{"name": "renamed", "deliveryFormat": "json"})
	res, err := p.Update(context.Background(), &resource.UpdateRequest{NativeID: "drain_1", DesiredProperties: props})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", res.ProgressResult.OperationStatus, res.ProgressResult.StatusMessage)
	}
}

func TestDelete_Idempotent(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"gone"}}`)
	})
	p := New(drainDef(), c, "")
	res, _ := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "drain_1"})
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v", res.ProgressResult.OperationStatus)
	}
}

func TestDelete_UnsupportedIsRejected(t *testing.T) {
	def := drainDef()
	def.NoDelete = true
	p := New(def, nil, "")
	res, _ := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "drain_1"})
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("ErrorCode = %v, want InvalidRequest", res.ProgressResult.ErrorCode)
	}
}

func TestList_AccountScope(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"drains":[{"id":"drain_1"},{"id":"drain_2"}]}`)
	})
	p := New(drainDef(), c, "")
	res, err := p.List(context.Background(), &resource.ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.NativeIDs) != 2 || res.NativeIDs[0] != "drain_1" {
		t.Errorf("NativeIDs = %v", res.NativeIDs)
	}
}

// -- project scope ----------------------------------------------------------

func customEnvDef() Definition {
	return Definition{
		Type:           "VERCEL::Projects::CustomEnvironment",
		Scope:          ScopeProject,
		ParentProperty: "projectId",
		CollectionPath: "/v9/projects/{parent}/custom-environments",
		ItemPath:       "/v9/projects/{parent}/custom-environments/{id}",
		ListField:      "environments",
		Fields:         []string{"slug", "description"},
	}
}

func TestCreate_ProjectScope_CompositeNativeID(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v9/projects/prj_1/custom-environments" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["projectId"]; ok {
			t.Error("parent property is a path segment, not a body field")
		}
		_, _ = io.WriteString(w, `{"id":"env_1","slug":"staging"}`)
	})
	p := New(customEnvDef(), c, "")
	props, _ := json.Marshal(map[string]any{"projectId": "prj_1", "slug": "staging"})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.ProgressResult.NativeID != "prj_1/env_1" {
		t.Errorf("NativeID = %q, want prj_1/env_1", res.ProgressResult.NativeID)
	}
}

func TestCreate_ProjectScope_RequiresParent(t *testing.T) {
	p := New(customEnvDef(), nil, "")
	props, _ := json.Marshal(map[string]any{"slug": "staging"})
	res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("ErrorCode = %v, want InvalidRequest", res.ProgressResult.ErrorCode)
	}
}

// Read must put the parent id back into the property document, or the desired
// state (which has it) never matches the read state.
func TestRead_ProjectScope_RestoresParentProperty(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v9/projects/prj_1/custom-environments/env_1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":"env_1","slug":"staging","description":"d"}`)
	})
	p := New(customEnvDef(), c, "")
	res, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1/env_1"})
	var got map[string]any
	_ = json.Unmarshal([]byte(res.Properties), &got)
	if got["projectId"] != "prj_1" {
		t.Errorf("projectId = %v, want prj_1", got["projectId"])
	}
}

func TestRead_ProjectScope_BadNativeID(t *testing.T) {
	p := New(customEnvDef(), nil, "")
	res, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "no-slash"})
	if res.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("ErrorCode = %v", res.ErrorCode)
	}
}

func TestList_ProjectScope_UsesScopeWhenSet(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v10/projects" {
			t.Error("scoped List must not enumerate projects")
		}
		_, _ = io.WriteString(w, `{"environments":[{"id":"env_1"}]}`)
	})
	p := New(customEnvDef(), c, "prj_1")
	res, _ := p.List(context.Background(), &resource.ListRequest{})
	if len(res.NativeIDs) != 1 || res.NativeIDs[0] != "prj_1/env_1" {
		t.Errorf("NativeIDs = %v", res.NativeIDs)
	}
}

func TestList_ProjectScope_WalksProjects(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v10/projects":
			_, _ = io.WriteString(w, `{"projects":[{"id":"prj_1"},{"id":"prj_2"}],"pagination":{"count":2,"next":null}}`)
		default:
			_, _ = io.WriteString(w, `{"environments":[{"id":"env_1"}]}`)
		}
	})
	p := New(customEnvDef(), c, "")
	res, _ := p.List(context.Background(), &resource.ListRequest{})
	if len(res.NativeIDs) != 2 {
		t.Fatalf("NativeIDs = %v", res.NativeIDs)
	}
}

// -- read via collection ----------------------------------------------------

// DNS records have no usable single-record GET, so Read scans the collection.
func TestRead_ViaCollection(t *testing.T) {
	def := Definition{
		Type:              "VERCEL::DNS::Record",
		Scope:             ScopeParent,
		ParentProperty:    "domain",
		CollectionPath:    "/v5/domains/{parent}/records",
		ItemPathDelete:    "/v2/domains/{parent}/records/{id}",
		ListField:         "records",
		ReadViaCollection: true,
		Fields:            []string{"name", "type", "value"},
	}
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/domains/example.com/records" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"records":[
			{"id":"rec_other","name":"a","type":"A","value":"1.1.1.1"},
			{"id":"rec_1","name":"www","type":"A","value":"2.2.2.2"}]}`)
	})
	p := New(def, c, "")
	res, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "example.com/rec_1"})
	var got map[string]any
	_ = json.Unmarshal([]byte(res.Properties), &got)
	if got["name"] != "www" || got["value"] != "2.2.2.2" {
		t.Errorf("props = %v", got)
	}
	if got["domain"] != "example.com" {
		t.Errorf("parent property not restored: %v", got)
	}
}

func TestRead_ViaCollection_NotFound(t *testing.T) {
	def := Definition{
		Type:              "VERCEL::DNS::Record",
		Scope:             ScopeParent,
		ParentProperty:    "domain",
		CollectionPath:    "/v5/domains/{parent}/records",
		ListField:         "records",
		ReadViaCollection: true,
		Fields:            []string{"name"},
	}
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"records":[{"id":"rec_other"}]}`)
	})
	p := New(def, c, "")
	res, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "example.com/rec_gone"})
	if res.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("ErrorCode = %v", res.ErrorCode)
	}
}

// -- misc -------------------------------------------------------------------

func TestQueryIsApplied(t *testing.T) {
	def := drainDef()
	def.Query = map[string]string{"decrypt": "true"}
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("decrypt") != "true" {
			t.Errorf("query = %v", r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{"id":"drain_1"}`)
	})
	p := New(def, c, "")
	if _, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "drain_1"}); err != nil {
		t.Fatalf("Read: %v", err)
	}
}

func TestStatus_IsSynchronous(t *testing.T) {
	p := New(drainDef(), nil, "")
	res, err := p.Status(context.Background(), &resource.StatusRequest{NativeID: "drain_1"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v", res.ProgressResult.OperationStatus)
	}
}

func TestCreate_MissingIDInResponse(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"name":"ship-logs"}`)
	})
	p := New(drainDef(), c, "")
	props, _ := json.Marshal(map[string]any{"name": "ship-logs"})
	res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if res.ProgressResult.OperationStatus != resource.OperationStatusFailure {
		t.Errorf("status = %v, want Failure", res.ProgressResult.OperationStatus)
	}
}

// A custom IDField covers resources keyed by something other than "id".
func TestCreate_CustomIDField(t *testing.T) {
	def := drainDef()
	def.IDField = "slug"
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"slug":"my-drain","name":"ship-logs"}`)
	})
	p := New(def, c, "")
	props, _ := json.Marshal(map[string]any{"name": "ship-logs"})
	res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if res.ProgressResult.NativeID != "my-drain" {
		t.Errorf("NativeID = %q", res.ProgressResult.NativeID)
	}
}

// PKL reserves `type`, so a DNS record's type has to be declared under another
// name and renamed on the wire in both directions.
func TestRename_BothDirections(t *testing.T) {
	def := drainDef()
	def.Fields = []string{"recordType", "name"}
	def.Rename = map[string]string{"recordType": "type"}
	def.CreateOnly = nil

	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["type"] != "A" {
				t.Errorf("wire body should use the API name: %v", body)
			}
			if _, leaked := body["recordType"]; leaked {
				t.Error("PKL name leaked onto the wire")
			}
		}
		_, _ = io.WriteString(w, `{"id":"rec_1","type":"A","name":"www"}`)
	})
	p := New(def, c, "")
	props, _ := json.Marshal(map[string]any{"recordType": "A", "name": "www"})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(res.ProgressResult.ResourceProperties, &got)
	if got["recordType"] != "A" {
		t.Errorf("response should map back to the PKL name: %v", got)
	}
	if _, leaked := got["type"]; leaked {
		t.Error("API name leaked into the property document")
	}
}

// Some Vercel payloads arrive wrapped: creating a repository answers
// {"repository": {...}}.
func TestUnwrap_CreateAndRead(t *testing.T) {
	def := Definition{
		Type:           "VERCEL::VCR::Repository",
		Scope:          ScopeAccount,
		CollectionPath: "/v1/vcr/repository",
		ItemPath:       "/v1/vcr/repository/{id}",
		Unwrap:         "repository",
		ListField:      "repositories",
		Fields:         []string{"name", "projectId"},
		CreateOnly:     []string{"name", "projectId"},
		NoUpdate:       true,
	}
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"repository":{"id":"repo_1","name":"api","projectId":"prj_1"}}`)
	})
	p := New(def, c, "")
	props, _ := json.Marshal(map[string]any{"name": "api", "projectId": "prj_1"})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.ProgressResult.NativeID != "repo_1" {
		t.Fatalf("NativeID = %q", res.ProgressResult.NativeID)
	}
	read, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "repo_1"})
	var got map[string]any
	_ = json.Unmarshal([]byte(read.Properties), &got)
	if got["name"] != "api" {
		t.Errorf("props = %v", got)
	}
}

// Aliases are created under a deployment but read, listed and deleted
// account-wide, so the deployment is a create-time path input rather than a
// parent in the native id.
func TestCreatePath_PropertyTemplate(t *testing.T) {
	def := Definition{
		Type:           "VERCEL::Deployments::Alias",
		Scope:          ScopeAccount,
		CreatePath:     "/v2/deployments/{prop:deploymentId}/aliases",
		CollectionPath: "/v4/aliases",
		ItemPath:       "/v4/aliases/{id}",
		ItemPathDelete: "/v2/aliases/{id}",
		ListField:      "aliases",
		IDField:        "uid",
		CreateIDField:  "uid",
		Fields:         []string{"alias", "deploymentId"},
		CreateOnly:     []string{"alias", "deploymentId"},
		NoUpdate:       true,
	}
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path != "/v2/deployments/dpl_1/aliases" {
			t.Errorf("create path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"uid":"al_1","alias":"a.example.com"}`)
	})
	p := New(def, c, "")
	props, _ := json.Marshal(map[string]any{"alias": "a.example.com", "deploymentId": "dpl_1"})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Account-scoped: the native id is the alias id alone, not "dpl_1/al_1".
	if res.ProgressResult.NativeID != "al_1" {
		t.Errorf("NativeID = %q, want al_1", res.ProgressResult.NativeID)
	}
}

func TestCreatePath_MissingPropertyIsRejected(t *testing.T) {
	def := Definition{
		Type:           "VERCEL::Deployments::Alias",
		Scope:          ScopeAccount,
		CreatePath:     "/v2/deployments/{prop:deploymentId}/aliases",
		CollectionPath: "/v4/aliases",
		ItemPath:       "/v4/aliases/{id}",
		Fields:         []string{"alias"},
	}
	p := New(def, nil, "")
	props, _ := json.Marshal(map[string]any{"alias": "a.example.com"})
	res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("ErrorCode = %v, want InvalidRequest", res.ProgressResult.ErrorCode)
	}
}

// -- request-body wrapper ---------------------------------------------------

// A project route: the write body nests the managed fields under "route" while
// the response and every list entry carry them flat, so reads and writes
// disagree on shape. Wrap covers the request; Unwrap covers the response, and
// they are independent.
func projectRouteDef() Definition {
	return Definition{
		Type:           "VERCEL::Projects::Route",
		Scope:          ScopeProject,
		ParentProperty: "projectId",
		CollectionPath: "/v1/projects/{parent}/routes",
		ItemPath:       "/v1/projects/{parent}/routes/{id}",
		ListField:      "routes",
		Wrap:           "route",
		WrapExclude:    []string{"position"},
		Fields:         []string{"name", "description", "enabled", "srcSyntax", "routeRule", "position"},
		Rename:         map[string]string{"routeRule": "route"},
		CreateOnly:     []string{"position"},
	}
}

func routeProperties() []byte {
	props, _ := json.Marshal(map[string]any{
		"projectId":   "prj_1",
		"name":        "api",
		"description": "d",
		"enabled":     true,
		"srcSyntax":   "regex",
		"routeRule":   map[string]any{"src": "/a", "dest": "/b"},
		"position":    map[string]any{"placement": "before", "referenceId": "rt_0"},
	})
	return props
}

func TestWrap_CreateNestsTheManagedFields(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		wrapped, ok := body["route"].(map[string]any)
		if !ok {
			t.Fatalf("body is not wrapped: %v", body)
		}
		if wrapped["name"] != "api" || wrapped["srcSyntax"] != "regex" {
			t.Errorf("wrapper = %v", wrapped)
		}
		// A renamed field keeps its API name inside the wrapper.
		if _, ok := wrapped["route"].(map[string]any); !ok {
			t.Errorf("renamed field missing from the wrapper: %v", wrapped)
		}
		// The excluded field stays a sibling of the wrapper.
		if _, ok := body["position"].(map[string]any); !ok {
			t.Errorf("excluded field must stay top-level: %v", body)
		}
		if _, leaked := wrapped["position"]; leaked {
			t.Errorf("excluded field was wrapped: %v", wrapped)
		}
		if _, leaked := body["name"]; leaked {
			t.Errorf("wrapped field also sent flat: %v", body)
		}

		// The response is flat, next to fields we do not manage.
		_, _ = io.WriteString(w, `{"id":"rt_1","name":"api","description":"d","enabled":true,
			"srcSyntax":"regex","route":{"src":"/a","dest":"/b"},"staged":false,"rawSrc":"/a"}`)
	})
	p := New(projectRouteDef(), c, "")
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: routeProperties()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pr := res.ProgressResult
	if pr.NativeID != "prj_1/rt_1" {
		t.Fatalf("NativeID = %q", pr.NativeID)
	}
	// The flat response maps straight back onto the declared field names.
	var got map[string]any
	_ = json.Unmarshal(pr.ResourceProperties, &got)
	if got["name"] != "api" {
		t.Errorf("props = %v", got)
	}
	if rule, ok := got["routeRule"].(map[string]any); !ok || rule["src"] != "/a" {
		t.Errorf("renamed field did not map back: %v", got)
	}
	if _, leaked := got["staged"]; leaked {
		t.Errorf("unmanaged field leaked: %v", got)
	}
}

func TestWrap_ReadIsUnaffected(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"rt_1","name":"api","route":{"src":"/a"},"staged":false}`)
	})
	p := New(projectRouteDef(), c, "")
	res, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1/rt_1"})
	var got map[string]any
	_ = json.Unmarshal([]byte(res.Properties), &got)
	if got["name"] != "api" || got["projectId"] != "prj_1" {
		t.Errorf("props = %v", got)
	}
	if _, ok := got["routeRule"]; !ok {
		t.Errorf("flat response should map onto the declared name: %v", got)
	}
}

func TestWrap_UpdateWrapsAndStillOmitsCreateOnlyFields(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s", r.Method)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		wrapped, ok := body["route"].(map[string]any)
		if !ok {
			t.Fatalf("body is not wrapped: %v", body)
		}
		if wrapped["name"] != "api" {
			t.Errorf("wrapper = %v", wrapped)
		}
		if _, present := body["position"]; present {
			t.Error("createOnly field must not be sent on update, wrapped or not")
		}
		_, _ = io.WriteString(w, `{"id":"rt_1","name":"api"}`)
	})
	p := New(projectRouteDef(), c, "")
	res, _ := p.Update(context.Background(), &resource.UpdateRequest{NativeID: "prj_1/rt_1", DesiredProperties: routeProperties()})
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", res.ProgressResult.OperationStatus, res.ProgressResult.StatusMessage)
	}
}

// Nothing left to nest means no wrapper: an empty object is a different request
// from an absent one, and some APIs reject it.
func TestWrap_EmptyWrapperIsOmitted(t *testing.T) {
	def := projectRouteDef()
	def.CreateOnly = []string{"name", "description", "enabled", "srcSyntax", "routeRule"}
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, present := body["route"]; present {
			t.Errorf("empty wrapper should be omitted: %v", body)
		}
		if _, present := body["position"]; !present {
			t.Errorf("body = %v", body)
		}
		_, _ = io.WriteString(w, `{"id":"rt_1"}`)
	})
	p := New(def, c, "")
	if _, err := p.Update(context.Background(), &resource.UpdateRequest{NativeID: "prj_1/rt_1", DesiredProperties: routeProperties()}); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// Wrap with nothing excluded nests every declared field.
func TestWrap_WithoutExclusions(t *testing.T) {
	def := drainDef()
	def.Wrap = "drain"
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		wrapped, ok := body["drain"].(map[string]any)
		if !ok || wrapped["name"] != "ship-logs" {
			t.Errorf("body = %v", body)
		}
		if len(body) != 1 {
			t.Errorf("nothing should sit beside the wrapper: %v", body)
		}
		_, _ = io.WriteString(w, `{"id":"drain_1","name":"ship-logs"}`)
	})
	p := New(def, c, "")
	props, _ := json.Marshal(map[string]any{"name": "ship-logs", "url": "https://x", "deliveryFormat": "json"})
	if _, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props}); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

// -- verb pairs -------------------------------------------------------------

// An association whose lifecycle is connect/disconnect rather than CRUD: the
// link response carries no id of its own, the id is one of the properties, and
// the unlink is a POST naming what to remove in its body.
func blobConnectionDef() Definition {
	return Definition{
		Type:                 "VERCEL::Storage::BlobProjectConnection",
		Scope:                ScopeParent,
		ParentProperty:       "storeId",
		ParentListPath:       "/v1/storage/stores",
		ParentListField:      "stores",
		CollectionPath:       "/v1/storage/stores/{parent}/connections",
		ItemPathDelete:       "/v1/storage/stores/{parent}/disconnect",
		ListField:            "connections",
		IDField:              "projectId",
		CreateIDFromProperty: "projectId",
		DeleteMethod:         "POST",
		DeleteBody:           map[string]any{"projectId": "{id}"},
		ReadViaCollection:    true,
		Fields:               []string{"projectId"},
		CreateOnly:           []string{"projectId"},
		NoUpdate:             true,
	}
}

func TestVerbPair_LinkTakesIDFromProperty(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/storage/stores/store_1/connections" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		// The link verb answers with no id of its own.
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	p := New(blobConnectionDef(), c, "")
	props, _ := json.Marshal(map[string]any{"storeId": "store_1", "projectId": "prj_1"})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pr := res.ProgressResult
	if pr.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", pr.OperationStatus, pr.StatusMessage)
	}
	if pr.NativeID != "store_1/prj_1" {
		t.Errorf("NativeID = %q, want store_1/prj_1", pr.NativeID)
	}
}

func TestVerbPair_LinkRequiresTheIDProperty(t *testing.T) {
	p := New(blobConnectionDef(), nil, "")
	props, _ := json.Marshal(map[string]any{"storeId": "store_1"})
	res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("ErrorCode = %v, want InvalidRequest", res.ProgressResult.ErrorCode)
	}
}

func TestVerbPair_UnlinkUsesItsOwnVerbAndBody(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/storage/stores/store_1/disconnect" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["projectId"] != "prj_1" {
			t.Errorf("body = %v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	p := New(blobConnectionDef(), c, "")
	res, _ := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "store_1/prj_1"})
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v", res.ProgressResult.OperationStatus)
	}
}

// Read infers existence from the listing, since an association has no item GET.
func TestVerbPair_ReadInfersExistenceFromTheListing(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/storage/stores/store_1/connections" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"connections":[{"projectId":"prj_other"}]}`)
	})
	p := New(blobConnectionDef(), c, "")
	res, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "store_1/prj_1"})
	if res.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("ErrorCode = %v, want NotFound", res.ErrorCode)
	}
}

// Some create responses answer with the *parent's* id — POST
// /v1/projects/{id}/members "responds with the project ID on success". Taking
// the id from the response would mint "prj_1/prj_1", which never reads back.
func TestCreateIDFromProperty_IgnoresAMisleadingResponseID(t *testing.T) {
	def := Definition{
		Type:                 "VERCEL::Projects::Member",
		Scope:                ScopeProject,
		ParentProperty:       "projectId",
		CollectionPath:       "/v1/projects/{parent}/members",
		ItemPathDelete:       "/v1/projects/{parent}/members/{id}",
		ListField:            "members",
		IDField:              "uid",
		CreateIDFromProperty: "uid",
		ReadViaCollection:    true,
		Fields:               []string{"uid", "role"},
		CreateOnly:           []string{"uid"},
		NoUpdate:             true,
	}
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"prj_1"}`)
	})
	p := New(def, c, "")
	props, _ := json.Marshal(map[string]any{"projectId": "prj_1", "uid": "user_1", "role": "MEMBER"})
	res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if res.ProgressResult.NativeID != "prj_1/user_1" {
		t.Errorf("NativeID = %q, want prj_1/user_1", res.ProgressResult.NativeID)
	}
}

// -- bulk delete with a request body ----------------------------------------

// Global Config tokens are removed by a DELETE to the collection carrying the
// tokens to remove; there is no per-token path.
func configTokenDef() Definition {
	return Definition{
		Type:              "VERCEL::GlobalConfig::Token",
		Scope:             ScopeParent,
		ParentProperty:    "edgeConfigId",
		ParentListPath:    "/v1/global-config",
		CollectionPath:    "/v1/global-config/{parent}/token",
		ItemPathDelete:    "/v1/global-config/{parent}/tokens",
		ListPath:          "/v1/global-config/{parent}/tokens",
		ListField:         "tokens",
		IDField:           "id",
		ReadViaCollection: true,
		DeleteBody:        map[string]any{"tokens": []string{"{id}"}},
		Fields:            []string{"label"},
		CreateOnly:        []string{"label"},
		NoUpdate:          true,
	}
}

func TestBulkDelete_SendsTheBodyToTheCollection(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/global-config/ecfg_1/tokens" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Tokens []string `json:"tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.Tokens) != 1 || body.Tokens[0] != "tok_1" {
			t.Errorf("body = %v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	p := New(configTokenDef(), c, "")
	res, _ := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "ecfg_1/tok_1"})
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v", res.ProgressResult.OperationStatus)
	}
}

// A token is created at .../token but listed at .../tokens, so the scan Read
// does must follow the parent-scoped ListPath, not the create collection.
func TestReadViaCollection_PrefersAParentScopedListPath(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/global-config/ecfg_1/tokens" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"tokens":[{"id":"tok_1","label":"ci"}]}`)
	})
	p := New(configTokenDef(), c, "")
	res, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "ecfg_1/tok_1"})
	if res.ErrorCode != "" {
		t.Fatalf("ErrorCode = %v", res.ErrorCode)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(res.Properties), &got)
	if got["label"] != "ci" || got["edgeConfigId"] != "ecfg_1" {
		t.Errorf("props = %v", got)
	}
}

// A bulk delete of something already gone is still a converged delete.
func TestBulkDelete_NotFoundIsIdempotent(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"gone"}}`)
	})
	p := New(configTokenDef(), c, "")
	res, _ := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "ecfg_1/tok_1"})
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v, want Success", res.ProgressResult.OperationStatus)
	}
}

func TestBulkDelete_APIFailureIsReported(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":"forbidden","message":"nope"}}`)
	})
	p := New(configTokenDef(), c, "")
	res, _ := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "ecfg_1/tok_1"})
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeAccessDenied {
		t.Errorf("ErrorCode = %v, want AccessDenied", res.ProgressResult.ErrorCode)
	}
}

// The delete body is a template: {id} and {parent} are substituted wherever
// they appear, including inside nested objects and arrays.
func TestDeleteBody_SubstitutesNestedPlaceholders(t *testing.T) {
	def := configTokenDef()
	def.DeleteBody = map[string]any{
		"scope":  map[string]any{"edgeConfigId": "{parent}"},
		"tokens": []any{"{id}"},
		"force":  true,
	}
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		scope, _ := body["scope"].(map[string]any)
		if scope["edgeConfigId"] != "ecfg_1" {
			t.Errorf("scope = %v", body["scope"])
		}
		tokens, _ := body["tokens"].([]any)
		if len(tokens) != 1 || tokens[0] != "tok_1" {
			t.Errorf("tokens = %v", body["tokens"])
		}
		if body["force"] != true {
			t.Errorf("non-string values must pass through: %v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	p := New(def, c, "")
	if _, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "ecfg_1/tok_1"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// A definition that declares no delete body must keep sending none: a stray
// body changes how some APIs behave.
func TestDelete_SendsNoBodyByDefault(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if len(body) != 0 {
			t.Errorf("unexpected delete body %q", body)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	p := New(drainDef(), c, "")
	if _, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "drain_1"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// Access groups key their id `accessGroupId`, including in the parent
// collection used to enumerate children.
func TestParentIDField(t *testing.T) {
	def := Definition{
		Type:            "VERCEL::AccessGroups::ProjectAssignment",
		Scope:           ScopeParent,
		ParentProperty:  "accessGroupId",
		ParentListPath:  "/v1/access-groups",
		ParentListField: "accessGroups",
		ParentIDField:   "accessGroupId",
		CollectionPath:  "/v1/access-groups/{parent}/projects",
		ItemPath:        "/v1/access-groups/{parent}/projects/{id}",
		ListField:       "projects",
		IDField:         "projectId",
		Fields:          []string{"role"},
	}
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/access-groups":
			_, _ = io.WriteString(w, `{"accessGroups":[{"accessGroupId":"ag_1"}]}`)
		case "/v1/access-groups/ag_1/projects":
			_, _ = io.WriteString(w, `{"projects":[{"projectId":"prj_1","role":"ADMIN"}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	p := New(def, c, "")
	res, _ := p.List(context.Background(), &resource.ListRequest{})
	if len(res.NativeIDs) != 1 || res.NativeIDs[0] != "ag_1/prj_1" {
		t.Errorf("NativeIDs = %v", res.NativeIDs)
	}
}

// formae renders unset optional sub-resource fields as explicit nulls, and
// Vercel rejects those for typed optionals ("`delivery.compression` should be
// string"). The engine must omit them instead.
func TestBody_StripsNullsFromNestedPayloads(t *testing.T) {
	def := Definition{
		Type:           "VERCEL::Drains::Drain",
		Scope:          ScopeAccount,
		CollectionPath: "/v1/drains",
		ItemPath:       "/v1/drains/{id}",
		Fields:         []string{"name", "delivery", "variants"},
	}
	var got map[string]any
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"id":"drain_1"}`)
	})
	p := New(def, c, "")
	props, _ := json.Marshal(map[string]any{
		"name": "d",
		"delivery": map[string]any{
			"endpoint": "https://x", "compression": nil, "secret": nil,
		},
		"variants": []any{
			map[string]any{"id": "on", "label": nil},
		},
	})
	if _, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	delivery, _ := got["delivery"].(map[string]any)
	if _, present := delivery["compression"]; present {
		t.Error("null compression must be omitted, not sent")
	}
	if _, present := delivery["secret"]; present {
		t.Error("null secret must be omitted, not sent")
	}
	if delivery["endpoint"] != "https://x" {
		t.Errorf("non-null values must survive: %v", delivery)
	}
	variants, _ := got["variants"].([]any)
	if len(variants) != 1 {
		t.Fatalf("variants = %v", variants)
	}
	v0, _ := variants[0].(map[string]any)
	if _, present := v0["label"]; present {
		t.Error("null label inside an array element must be omitted")
	}
	if v0["id"] != "on" {
		t.Errorf("array element lost data: %v", v0)
	}
}

// A VCR repository is created with its project in the body, addressed by a
// bare id, and both read and listed with ?projectId=. All of it at once.
func vcrDef() Definition {
	return Definition{
		Type:           "VERCEL::VCR::Repository",
		Scope:          ScopeProject,
		ParentProperty: "projectId",
		ParentInBody:   true,
		CollectionPath: "/v1/vcr/repository",
		ItemPath:       "/v1/vcr/repository/{id}",
		ItemQuery:      map[string]string{"projectId": "{parent}"},
		ListQuery:      map[string]string{"projectId": "{parent}"},
		Unwrap:         "repository",
		ListField:      "repositories",
		Fields:         []string{"name"},
		CreateOnly:     []string{"name"},
		NoUpdate:       true,
	}
}

func TestParentInBody_SendsParentAsField(t *testing.T) {
	var body map[string]any
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"repository":{"id":"repo_1","name":"api","projectId":"prj_1"}}`)
	})
	p := New(vcrDef(), c, "")
	props, _ := json.Marshal(map[string]any{"projectId": "prj_1", "name": "api"})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if body["projectId"] != "prj_1" {
		t.Errorf("parent must be sent in the create body, got %v", body)
	}
	if res.ProgressResult.NativeID != "prj_1/repo_1" {
		t.Errorf("NativeID = %q, want prj_1/repo_1", res.ProgressResult.NativeID)
	}
}

// Regression: delete used to omit the project entirely and the API answered
// "400 missing required property projectId".
func TestItemQuery_SendsParentAsQueryParam(t *testing.T) {
	var gotQuery string
	var gotPath string
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("projectId")
		w.WriteHeader(http.StatusAccepted)
	})
	p := New(vcrDef(), c, "")
	res, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "prj_1/repo_1"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if gotPath != "/v1/vcr/repository/repo_1" {
		t.Errorf("path = %q", gotPath)
	}
	if gotQuery != "prj_1" {
		t.Errorf("projectId query = %q, want prj_1", gotQuery)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v", res.ProgressResult.OperationStatus)
	}
}

// Regression: this resource used to be listed account-wide, recovering each
// item's project from a field in the response. There is no account-wide list —
// GET /v1/vcr/repository without ?projectId= answers 400, which discovery
// reported as "0 resources", and the conformance discovery test sat waiting
// for a repository it had just created until it timed out. Listing walks
// projects and asks per project.
func TestListQuery_TemplatesParentPerProject(t *testing.T) {
	var asked []string
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v10/projects":
			_, _ = io.WriteString(w, `{"projects":[{"id":"prj_1"},{"id":"prj_2"}],"pagination":{"count":2,"next":null}}`)
		case "/v1/vcr/repository":
			q := r.URL.Query().Get("projectId")
			asked = append(asked, q)
			_, _ = io.WriteString(w, `{"repositories":[{"id":"repo_of_`+q+`"}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	p := New(vcrDef(), c, "")
	res, err := p.List(context.Background(), &resource.ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(asked) != 2 || asked[0] != "prj_1" || asked[1] != "prj_2" {
		t.Errorf("projectId asked per project = %v, want [prj_1 prj_2]", asked)
	}
	want := []string{"prj_1/repo_of_prj_1", "prj_2/repo_of_prj_2"}
	if len(res.NativeIDs) != 2 || res.NativeIDs[0] != want[0] || res.NativeIDs[1] != want[1] {
		t.Errorf("NativeIDs = %v, want %v", res.NativeIDs, want)
	}
}

// ListQuery is templated against a parent that only List knows. It must not
// leak into writes, where the placeholder would go out unsubstituted.
func TestListQuery_DoesNotLeakIntoCreate(t *testing.T) {
	var gotQuery string
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("projectId")
		_, _ = io.WriteString(w, `{"repository":{"id":"repo_1","name":"api","projectId":"prj_1"}}`)
	})
	p := New(vcrDef(), c, "")
	props, _ := json.Marshal(map[string]any{"projectId": "prj_1", "name": "api"})
	if _, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotQuery != "" {
		t.Errorf("create sent projectId=%q as a query parameter; ListQuery is for List only", gotQuery)
	}
}

// Read must carry ItemQuery too, not just Delete: GET on a container registry
// repository is rejected without ?projectId=, and a failing Read makes formae
// mark the resource Failed before it ever attempts the delete.
func TestItemQuery_AppliesToRead(t *testing.T) {
	var gotQuery string
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("projectId")
		_, _ = io.WriteString(w, `{"repository":{"id":"repo_1","name":"api","projectId":"prj_1"}}`)
	})
	p := New(vcrDef(), c, "")
	res, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1/repo_1"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if gotQuery != "prj_1" {
		t.Errorf("Read projectId query = %q, want prj_1", gotQuery)
	}
	if res.ErrorCode != "" {
		t.Errorf("ErrorCode = %v", res.ErrorCode)
	}
}

// A custom environment's DELETE takes an optional JSON body, and the endpoint
// rejects the request when there is none: sending no body answers
// `400 Invalid JSON`, while `{}` answers 200. The spec marks the body optional,
// so this is only discoverable against the live API — the conformance fixture
// failed at Destroy for exactly this reason after passing Create through Update.
func TestDeleteBody_EmptyObjectIsStillSent(t *testing.T) {
	var sawBody bool
	var raw string
	def := customEnvDef()
	def.DeleteBody = map[string]any{}
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		raw = string(b)
		sawBody = len(b) > 0
		w.WriteHeader(http.StatusOK)
	})
	p := New(def, c, "")
	if _, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "prj_1/env_1"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !sawBody {
		t.Fatal("an explicitly declared empty DeleteBody must still put {} on the wire")
	}
	if raw != "{}" {
		t.Errorf("body = %q, want {}", raw)
	}
}

// The distinction matters: a nil DeleteBody means "no body at all", which is
// what every other resource needs.
func TestDeleteBody_NilSendsNoBody(t *testing.T) {
	var length int
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		length = len(b)
		w.WriteHeader(http.StatusOK)
	})
	p := New(customEnvDef(), c, "")
	if _, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "prj_1/env_1"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if length != 0 {
		t.Errorf("nil DeleteBody sent %d bytes, want none", length)
	}
}

// A declared field whose value is null must be omitted entirely, not sent as
// null. stripNulls cleaned nulls *inside* objects and arrays but returned a
// top-level nil unchanged, so the key still landed on the wire.
//
// Feature flags are where this surfaced: a forma that sets no `permanent`
// renders it as null, and PUT .../feature-flags/flags answers
// `400 Invalid request: permanent should be boolean`. Every engine-declared
// resource sent top-level nulls this way; flags are simply the strictest
// endpoint. This was the "undiagnosed" conformance failure.
func TestBody_OmitsTopLevelNulls(t *testing.T) {
	var body map[string]any
	def := drainDef()
	def.Fields = []string{"name", "url", "deliveryFormat"}
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"id":"drain_1","name":"keep"}`)
	})
	p := New(def, c, "")
	props, _ := json.Marshal(map[string]any{
		"name":           "keep",
		"url":            nil, // unset optional
		"deliveryFormat": nil, // unset optional
	})
	if _, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if body["name"] != "keep" {
		t.Errorf("set field lost: %v", body)
	}
	for _, k := range []string{"url", "deliveryFormat"} {
		if v, present := body[k]; present {
			t.Errorf("null field %q was sent as %v; it must be omitted", k, v)
		}
	}
}

// The same on update: a field cleared in the forma must be omitted, since these
// endpoints spell "absent" by omission.
func TestBody_OmitsTopLevelNullsOnUpdate(t *testing.T) {
	var body map[string]any
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"id":"drain_1","name":"x"}`)
	})
	p := New(drainDef(), c, "")
	props, _ := json.Marshal(map[string]any{"name": "x", "url": nil})
	if _, err := p.Update(context.Background(), &resource.UpdateRequest{
		NativeID: "drain_1", DesiredProperties: props,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, present := body["url"]; present {
		t.Errorf("null field sent on update: %v", body)
	}
}
