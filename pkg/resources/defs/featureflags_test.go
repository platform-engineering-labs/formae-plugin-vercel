// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package defs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/rest"
	vercelapi "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/transport/vercel"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

const (
	typeFlag    = "VERCEL::FeatureFlags::Flag"
	typeSegment = "VERCEL::FeatureFlags::Segment"
	typeSDKKey  = "VERCEL::FeatureFlags::SDKKey"
)

func ffClient(t *testing.T, h http.HandlerFunc) *vercelapi.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := vercelapi.NewClient(vercelapi.Config{BaseURL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// ffDef finds a declared definition by type. Looking it up through All()
// rather than calling the constructor directly also proves the definition is
// actually part of a registered group.
func ffDef(t *testing.T, typ string) rest.Definition {
	t.Helper()
	for _, def := range All() {
		if def.Type == typ {
			return def
		}
	}
	t.Fatalf("%s is not declared", typ)
	return rest.Definition{}
}

func ffResource(t *testing.T, typ, projectScope string, h http.HandlerFunc) *rest.Resource {
	t.Helper()
	return rest.New(ffDef(t, typ), ffClient(t, h), projectScope)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return body
}

// =============================================================================
// Flag
// =============================================================================

// Create is PUT, not POST — the flags endpoint has no POST verb at all.
func TestFlagCreate_PutsToProjectCollection(t *testing.T) {
	called := false
	p := ffResource(t, typeFlag, "", func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/v1/projects/prj_1/feature-flags/flags" {
			t.Errorf("path = %s", r.URL.Path)
		}
		body := decodeBody(t, r)
		if body["slug"] != "new-checkout" || body["kind"] != "boolean" {
			t.Errorf("body = %v", body)
		}
		if _, ok := body["environments"]; !ok {
			t.Errorf("environments missing from create body: %v", body)
		}
		if _, ok := body["projectId"]; ok {
			t.Error("projectId is a path segment, not a body field")
		}
		_, _ = io.WriteString(w, `{"id":"flag_1","slug":"new-checkout","kind":"boolean",
			"state":"active","variants":[],"environments":{},"projectId":"prj_1"}`)
	})

	props := mustJSON(t, map[string]any{
		"projectId":    "prj_1",
		"slug":         "new-checkout",
		"kind":         "boolean",
		"variants":     []any{map[string]any{"id": "on", "value": true}},
		"environments": map[string]any{"production": map[string]any{"active": true}},
	})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !called {
		t.Fatal("no request was made")
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", res.ProgressResult.OperationStatus, res.ProgressResult.StatusMessage)
	}
	if res.ProgressResult.NativeID != "prj_1/flag_1" {
		t.Errorf("NativeID = %q, want prj_1/flag_1", res.ProgressResult.NativeID)
	}
}

// The read document must carry exactly the declared fields plus the id and the
// project, or every sync reports drift on server-owned fields.
func TestFlagRead_FiltersFieldsAndRestoresProject(t *testing.T) {
	p := ffResource(t, typeFlag, "", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/v1/projects/prj_1/feature-flags/flags/flag_1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":"flag_1","slug":"new-checkout","kind":"boolean",
			"state":"active","variants":[],"environments":{},"description":"d",
			"createdAt":1,"updatedAt":2,"createdBy":"u","ownerId":"team_1",
			"projectId":"prj_1","revision":3,"seed":42,"typeName":"flag"}`)
	})

	res, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1/flag_1"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(res.Properties), &got); err != nil {
		t.Fatalf("unmarshal properties: %v", err)
	}
	for _, want := range []string{"id", "projectId", "slug", "kind", "state", "variants", "environments", "description"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if got["projectId"] != "prj_1" {
		t.Errorf("projectId = %v, want prj_1", got["projectId"])
	}
	for _, unwanted := range []string{"createdAt", "updatedAt", "createdBy", "ownerId", "revision", "seed", "typeName"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("unmanaged field %q leaked into the read document", unwanted)
		}
	}
}

// PATCH accepts neither slug nor kind: both identify the flag and changing
// either has to replace it.
func TestFlagUpdate_OmitsCreateOnlyFields(t *testing.T) {
	p := ffResource(t, typeFlag, "", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		if r.URL.Path != "/v1/projects/prj_1/feature-flags/flags/flag_1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		body := decodeBody(t, r)
		for _, createOnly := range []string{"slug", "kind"} {
			if _, present := body[createOnly]; present {
				t.Errorf("createOnly field %q must not be sent on update: %v", createOnly, body)
			}
		}
		if body["description"] != "now with more checkout" {
			t.Errorf("description = %v", body["description"])
		}
		_, _ = io.WriteString(w, `{"id":"flag_1","slug":"new-checkout","kind":"boolean","state":"active"}`)
	})

	props := mustJSON(t, map[string]any{
		"projectId":   "prj_1",
		"slug":        "new-checkout",
		"kind":        "boolean",
		"description": "now with more checkout",
		"state":       "active",
	})
	res, err := p.Update(context.Background(), &resource.UpdateRequest{NativeID: "prj_1/flag_1", DesiredProperties: props})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", res.ProgressResult.OperationStatus, res.ProgressResult.StatusMessage)
	}
}

// A flag deleted out of band must converge, not fail the apply.
func TestFlagDelete_IdempotentOn404(t *testing.T) {
	p := ffResource(t, typeFlag, "", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/v1/projects/prj_1/feature-flags/flags/flag_1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"gone"}}`)
	})

	res, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "prj_1/flag_1"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v, want Success", res.ProgressResult.OperationStatus)
	}
}

// The collection answers {"data": [...]}, and a target-scoped project must not
// make discovery walk every project in the account.
func TestFlagList_ScopedToProject(t *testing.T) {
	p := ffResource(t, typeFlag, "prj_1", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v10/projects" {
			t.Error("a project-scoped List must not enumerate projects")
		}
		if r.URL.Path != "/v1/projects/prj_1/feature-flags/flags" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"flag_1"},{"id":"flag_2"}],"pagination":{"next":null}}`)
	})

	res, err := p.List(context.Background(), &resource.ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"prj_1/flag_1", "prj_1/flag_2"}
	if len(res.NativeIDs) != len(want) {
		t.Fatalf("NativeIDs = %v, want %v", res.NativeIDs, want)
	}
	for i, id := range want {
		if res.NativeIDs[i] != id {
			t.Errorf("NativeIDs[%d] = %q, want %q", i, res.NativeIDs[i], id)
		}
	}
}

// =============================================================================
// Segment
// =============================================================================

// `label` is reserved by formae.Resource, so the PKL name is segmentLabel and
// only the wire may see `label`.
func TestSegmentCreate_PutsAndRenamesLabel(t *testing.T) {
	p := ffResource(t, typeSegment, "", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/v1/projects/prj_1/feature-flags/segments" {
			t.Errorf("path = %s", r.URL.Path)
		}
		body := decodeBody(t, r)
		if body["label"] != "Beta testers" {
			t.Errorf("wire body must use the API name `label`: %v", body)
		}
		if _, leaked := body["segmentLabel"]; leaked {
			t.Error("PKL name segmentLabel leaked onto the wire")
		}
		if body["slug"] != "beta-testers" || body["hint"] != "email" {
			t.Errorf("body = %v", body)
		}
		if _, ok := body["data"]; !ok {
			t.Errorf("data missing from create body: %v", body)
		}
		_, _ = io.WriteString(w, `{"id":"seg_1","slug":"beta-testers","label":"Beta testers",
			"hint":"email","data":{},"projectId":"prj_1","typeName":"segment"}`)
	})

	props := mustJSON(t, map[string]any{
		"projectId":    "prj_1",
		"slug":         "beta-testers",
		"segmentLabel": "Beta testers",
		"hint":         "email",
		"data":         map[string]any{"include": map[string]any{}},
	})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.ProgressResult.NativeID != "prj_1/seg_1" {
		t.Errorf("NativeID = %q, want prj_1/seg_1", res.ProgressResult.NativeID)
	}
	var created map[string]any
	if err := json.Unmarshal(res.ProgressResult.ResourceProperties, &created); err != nil {
		t.Fatalf("unmarshal created properties: %v", err)
	}
	if created["segmentLabel"] != "Beta testers" {
		t.Errorf("response must map `label` back to segmentLabel: %v", created)
	}
	if _, leaked := created["label"]; leaked {
		t.Error("API name `label` leaked into the property document")
	}
}

func TestSegmentRead_FiltersFieldsAndRestoresProject(t *testing.T) {
	p := ffResource(t, typeSegment, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/prj_1/feature-flags/segments/seg_1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":"seg_1","slug":"beta-testers","label":"Beta testers",
			"hint":"email","data":{},"description":"d","createdAt":1,"updatedAt":2,
			"createdBy":"u","usedByFlags":["flag_1"],"projectId":"prj_1","typeName":"segment"}`)
	})

	res, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1/seg_1"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(res.Properties), &got); err != nil {
		t.Fatalf("unmarshal properties: %v", err)
	}
	if got["projectId"] != "prj_1" {
		t.Errorf("projectId = %v, want prj_1", got["projectId"])
	}
	if got["segmentLabel"] != "Beta testers" {
		t.Errorf("segmentLabel = %v", got["segmentLabel"])
	}
	for _, unwanted := range []string{"label", "createdAt", "updatedAt", "createdBy", "usedByFlags", "typeName"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("unmanaged field %q leaked into the read document", unwanted)
		}
	}
}

// The slug identifies the segment and PATCH does not accept it.
func TestSegmentUpdate_OmitsSlug(t *testing.T) {
	p := ffResource(t, typeSegment, "", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		body := decodeBody(t, r)
		if _, present := body["slug"]; present {
			t.Errorf("slug is createOnly and must not be sent on update: %v", body)
		}
		if body["label"] != "Renamed" {
			t.Errorf("label = %v", body["label"])
		}
		_, _ = io.WriteString(w, `{"id":"seg_1","label":"Renamed","slug":"beta-testers","hint":"email","data":{}}`)
	})

	props := mustJSON(t, map[string]any{
		"projectId":    "prj_1",
		"slug":         "beta-testers",
		"segmentLabel": "Renamed",
		"hint":         "email",
		"data":         map[string]any{},
	})
	res, err := p.Update(context.Background(), &resource.UpdateRequest{NativeID: "prj_1/seg_1", DesiredProperties: props})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", res.ProgressResult.OperationStatus, res.ProgressResult.StatusMessage)
	}
}

func TestSegmentDelete_IdempotentOn404(t *testing.T) {
	p := ffResource(t, typeSegment, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/prj_1/feature-flags/segments/seg_1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"gone"}}`)
	})

	res, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "prj_1/seg_1"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v, want Success", res.ProgressResult.OperationStatus)
	}
}

func TestSegmentList_ScopedToProject(t *testing.T) {
	p := ffResource(t, typeSegment, "prj_1", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v10/projects" {
			t.Error("a project-scoped List must not enumerate projects")
		}
		if r.URL.Path != "/v1/projects/prj_1/feature-flags/segments" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"seg_1"}]}`)
	})

	res, err := p.List(context.Background(), &resource.ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.NativeIDs) != 1 || res.NativeIDs[0] != "prj_1/seg_1" {
		t.Errorf("NativeIDs = %v, want [prj_1/seg_1]", res.NativeIDs)
	}
}

// =============================================================================
// SDK key
// =============================================================================

// The id is `hashKey`, and the create body spells the key type `sdkKeyType`
// even though every read spells it `type`.
func TestSDKKeyCreate_PutsAndKeysOnHashKey(t *testing.T) {
	p := ffResource(t, typeSDKKey, "", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/v1/projects/prj_1/feature-flags/sdk-keys" {
			t.Errorf("path = %s", r.URL.Path)
		}
		body := decodeBody(t, r)
		if body["sdkKeyType"] != "server" {
			t.Errorf("create body must spell the key type `sdkKeyType`: %v", body)
		}
		if body["environment"] != "production" {
			t.Errorf("environment = %v", body["environment"])
		}
		if body["label"] != "backend-sdk" {
			t.Errorf("wire body must use the API name `label`: %v", body)
		}
		_, _ = io.WriteString(w, `{"hashKey":"hk_1","projectId":"prj_1","type":"server",
			"environment":"production","label":"backend-sdk","keyValue":"vf_server_secret",
			"partialKeyValue":"vf_server_abc********","createdAt":1,"updatedAt":2,"createdBy":"u"}`)
	})

	props := mustJSON(t, map[string]any{
		"projectId":   "prj_1",
		"sdkKeyType":  "server",
		"environment": "production",
		"keyLabel":    "backend-sdk",
	})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.ProgressResult.NativeID != "prj_1/hk_1" {
		t.Errorf("NativeID = %q, want prj_1/hk_1", res.ProgressResult.NativeID)
	}
	var created map[string]any
	if err := json.Unmarshal(res.ProgressResult.ResourceProperties, &created); err != nil {
		t.Fatalf("unmarshal created properties: %v", err)
	}
	// The cleartext secrets are returned once, at creation, and must never
	// become part of managed state.
	for _, secret := range []string{"keyValue", "tokenValue", "connectionString", "partialKeyValue"} {
		if _, leaked := created[secret]; leaked {
			t.Errorf("%q must not be part of the property document", secret)
		}
	}
}

// There is no single-key GET, so Read scans the collection and matches hashKey.
func TestSDKKeyRead_ViaCollection(t *testing.T) {
	p := ffResource(t, typeSDKKey, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/prj_1/feature-flags/sdk-keys" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":[
			{"hashKey":"hk_other","type":"client","environment":"preview"},
			{"hashKey":"hk_1","type":"server","environment":"production","label":"backend-sdk",
			 "partialKeyValue":"vf_server_abc********","createdAt":1}]}`)
	})

	res, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1/hk_1"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(res.Properties), &got); err != nil {
		t.Fatalf("unmarshal properties: %v", err)
	}
	if got["id"] != "hk_1" {
		t.Errorf("id = %v, want hk_1", got["id"])
	}
	if got["projectId"] != "prj_1" {
		t.Errorf("projectId = %v, want prj_1", got["projectId"])
	}
	if got["environment"] != "production" {
		t.Errorf("environment = %v", got["environment"])
	}
	if got["keyLabel"] != "backend-sdk" {
		t.Errorf("keyLabel = %v", got["keyLabel"])
	}
	for _, unwanted := range []string{"partialKeyValue", "createdAt", "hashKey", "label"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("unmanaged field %q leaked into the read document", unwanted)
		}
	}
}

func TestSDKKeyRead_NotFoundInCollection(t *testing.T) {
	p := ffResource(t, typeSDKKey, "", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"hashKey":"hk_other"}]}`)
	})

	res, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1/hk_gone"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("ErrorCode = %v, want NotFound", res.ErrorCode)
	}
}

func TestSDKKeyDelete_UsesHashKeyPathAndIsIdempotent(t *testing.T) {
	p := ffResource(t, typeSDKKey, "", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/v1/projects/prj_1/feature-flags/sdk-keys/hk_1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"gone"}}`)
	})

	res, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "prj_1/hk_1"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v, want Success", res.ProgressResult.OperationStatus)
	}
}

// The API has no PATCH for SDK keys: an update must be rejected rather than
// silently issued against a route that does not exist.
func TestSDKKeyUpdate_IsRejected(t *testing.T) {
	p := rest.New(ffDef(t, typeSDKKey), nil, "")
	res, err := p.Update(context.Background(), &resource.UpdateRequest{NativeID: "prj_1/hk_1"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("ErrorCode = %v, want InvalidRequest", res.ProgressResult.ErrorCode)
	}
}

func TestSDKKeyList_ScopedToProject(t *testing.T) {
	p := ffResource(t, typeSDKKey, "prj_1", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v10/projects" {
			t.Error("a project-scoped List must not enumerate projects")
		}
		if r.URL.Path != "/v1/projects/prj_1/feature-flags/sdk-keys" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":[{"hashKey":"hk_1"},{"hashKey":"hk_2"}]}`)
	})

	res, err := p.List(context.Background(), &resource.ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.NativeIDs) != 2 || res.NativeIDs[0] != "prj_1/hk_1" {
		t.Errorf("NativeIDs = %v, want [prj_1/hk_1 prj_1/hk_2]", res.NativeIDs)
	}
}

// =============================================================================
// Shape of the group itself
// =============================================================================

// Every feature-flag endpoint lives under the project, creates with PUT and
// pages its collection under `data`. Asserting it here catches a definition
// copied from the POST-shaped resources next door.
func TestFeatureFlagDefinitionsAreWellFormed(t *testing.T) {
	for _, typ := range []string{typeFlag, typeSegment, typeSDKKey} {
		def := ffDef(t, typ)

		if def.Scope != rest.ScopeProject {
			t.Errorf("%s: scope = %v, want ScopeProject", typ, def.Scope)
		}
		if def.ParentProperty != "projectId" {
			t.Errorf("%s: ParentProperty = %q, want projectId", typ, def.ParentProperty)
		}
		if def.CreateMethod != http.MethodPut {
			t.Errorf("%s: CreateMethod = %q, want PUT", typ, def.CreateMethod)
		}
		if def.ListField != "data" {
			t.Errorf("%s: ListField = %q, want data", typ, def.ListField)
		}
		if !strings.HasPrefix(def.CollectionPath, "/v1/projects/{parent}/feature-flags/") {
			t.Errorf("%s: CollectionPath = %q", typ, def.CollectionPath)
		}
		if !registry.Has(typ) {
			t.Errorf("%s is not registered", typ)
		}
		// `label` and `type` are reserved by formae.Resource; a definition may
		// only reach them through a rename.
		for _, field := range def.Fields {
			switch field {
			case "type", "target", "label", "group", "stack":
				t.Errorf("%s: field %q is reserved by formae.Resource", typ, field)
			}
		}
	}
}

func TestFeatureFlagOperations(t *testing.T) {
	// Flags and segments are fully mutable; SDK keys have no PATCH endpoint.
	for typ, wantUpdate := range map[string]bool{typeFlag: true, typeSegment: true, typeSDKKey: false} {
		def := ffDef(t, typ)
		if def.NoUpdate == wantUpdate {
			t.Errorf("%s: NoUpdate = %v, want %v", typ, def.NoUpdate, !wantUpdate)
		}
		if def.NoDelete {
			t.Errorf("%s: every feature-flag resource has a DELETE endpoint", typ)
		}
	}
}
