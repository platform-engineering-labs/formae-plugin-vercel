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
