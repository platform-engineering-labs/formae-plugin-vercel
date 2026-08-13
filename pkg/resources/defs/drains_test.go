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
	"reflect"
	"strings"
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/rest"
	vercelapi "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/transport/vercel"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// drainsClient wires a Definition to an offline httptest server. Nothing in
// this file may reach api.vercel.com.
func drainsClient(t *testing.T, h http.HandlerFunc) *vercelapi.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := vercelapi.NewClient(vercelapi.Config{BaseURL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func drainProvisioner(t *testing.T, h http.HandlerFunc) *rest.Resource {
	t.Helper()
	return rest.New(drain(), drainsClient(t, h), "")
}

// A log drain as vercel_log_drain would express it: schemas={"log":…},
// delivery.type=http, filter selecting sources and environments.
func logDrainProperties() []byte {
	return drainJSON(map[string]any{
		"name":       "ship-logs",
		"projects":   "some",
		"projectIds": []any{"prj_1"},
		"schemas":    map[string]any{"log": map[string]any{"version": "v1"}},
		"delivery": map[string]any{
			"type":     "http",
			"endpoint": "https://logs.example.com/ingest",
			"encoding": "ndjson",
			"headers":  map[string]any{"x-token": "abc"},
		},
		"sampling": []any{map[string]any{"type": "head_sampling", "rate": 0.5}},
		"filter": map[string]any{
			"version": "v2",
			"filter": map[string]any{
				"type":       "basic",
				"log":        map[string]any{"sources": []any{"lambda", "edge"}},
				"deployment": map[string]any{"environments": []any{"production"}},
			},
		},
		"source": map[string]any{"kind": "self-served"},
	})
}

func drainJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// =============================================================================
// Create
// =============================================================================

func TestDrainCreate_PathAndBody(t *testing.T) {
	var gotMethod, gotPath string
	var body map[string]any

	p := drainProvisioner(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"id":"drain_abc","ownerId":"team_1","name":"ship-logs",
			"projectIds":["prj_1"],"schemas":{"log":{"version":"v1"}},
			"delivery":{"type":"http","endpoint":"https://logs.example.com/ingest","encoding":"ndjson","headers":{"x-token":"abc"}}}`)
	})

	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: logDrainProperties()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", res.ProgressResult.OperationStatus, res.ProgressResult.StatusMessage)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/drains" {
		t.Errorf("request = %s %s, want POST /v1/drains", gotMethod, gotPath)
	}
	// The id lives under "id" on create as well as on read — unlike DNS
	// records, drains do not switch to `uid`.
	if res.ProgressResult.NativeID != "drain_abc" {
		t.Errorf("NativeID = %q, want drain_abc", res.ProgressResult.NativeID)
	}

	// Every documented create-body key must survive, nested objects included.
	for _, key := range []string{"name", "projects", "projectIds", "schemas", "delivery", "sampling", "filter", "source"} {
		if _, ok := body[key]; !ok {
			t.Errorf("create body missing %q: %v", key, body)
		}
	}
	if body["projects"] != "some" {
		t.Errorf("projects = %v, want some", body["projects"])
	}
	schemas, _ := body["schemas"].(map[string]any)
	if _, ok := schemas["log"]; !ok {
		t.Errorf("schemas = %v, want a log entry", body["schemas"])
	}
	delivery, _ := body["delivery"].(map[string]any)
	if delivery["type"] != "http" || delivery["endpoint"] != "https://logs.example.com/ingest" {
		t.Errorf("delivery = %v", body["delivery"])
	}
	// The engine must not smuggle unmanaged keys into the body: POST /v1/drains
	// declares additionalProperties:false and would answer 400.
	for key := range body {
		if !contains(drain().Fields, key) {
			t.Errorf("undeclared key %q in create body", key)
		}
	}
}

// A trace drain differs only in schemas and delivery; the same definition must
// carry it, since vercel_trace_drain is also POST /v1/drains.
func TestDrainCreate_TraceDrainShape(t *testing.T) {
	var body map[string]any
	p := drainProvisioner(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"id":"drain_trace"}`)
	})
	props := drainJSON(map[string]any{
		"name":     "otel",
		"projects": "all",
		"schemas":  map[string]any{"trace": map[string]any{"version": "v1"}},
		"delivery": map[string]any{
			"type":     "otlphttp",
			"endpoint": map[string]any{"traces": "https://otel.example.com/v1/traces"},
			"encoding": "proto",
			"headers":  map[string]any{},
		},
	})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.ProgressResult.NativeID != "drain_trace" {
		t.Errorf("NativeID = %q", res.ProgressResult.NativeID)
	}
	delivery, _ := body["delivery"].(map[string]any)
	endpoint, ok := delivery["endpoint"].(map[string]any)
	if !ok || endpoint["traces"] != "https://otel.example.com/v1/traces" {
		t.Errorf("otlphttp endpoint must stay an object: %v", delivery["endpoint"])
	}
}

// =============================================================================
// Read
// =============================================================================

// Read must narrow to the declared fields. The drain response carries a lot of
// server-owned state (timestamps, owner, disable bookkeeping) plus `filterV2`,
// which is the read-side spelling of the create-side `filter` — none of it may
// reach the property document.
func TestDrainRead_FiltersToDeclaredFields(t *testing.T) {
	p := drainProvisioner(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/drains/drain_abc" {
			t.Errorf("path = %s, want /v1/drains/drain_abc", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{
			"id":"drain_abc","name":"ship-logs","projectIds":["prj_1"],
			"schemas":{"log":{"version":"v1"}},
			"delivery":{"type":"http","endpoint":"https://logs.example.com/ingest","encoding":"ndjson","headers":{}},
			"sampling":[{"type":"head_sampling","rate":0.5}],
			"source":{"kind":"self-served"},
			"createdAt":1,"updatedAt":2,"ownerId":"team_1","teamId":"team_1",
			"status":"enabled","firstErrorTimestamp":0,"disabledAt":0,"disabledBy":"x",
			"disabledReason":"disabled-by-owner",
			"filterV2":{"version":"v2","filter":{"type":"basic"}}}`)
	})

	res, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "drain_abc"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.ErrorCode != "" {
		t.Fatalf("ErrorCode = %v", res.ErrorCode)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(res.Properties), &got); err != nil {
		t.Fatalf("properties: %v", err)
	}
	for _, want := range []string{"id", "name", "projectIds", "schemas", "delivery", "sampling", "source"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	for _, unwanted := range []string{
		"createdAt", "updatedAt", "ownerId", "teamId", "status",
		"firstErrorTimestamp", "disabledAt", "disabledBy", "disabledReason", "filterV2",
	} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("unmanaged field %q leaked into the read document", unwanted)
		}
	}
	// `projects` and `filter` are write-only on this API: they are accepted on
	// create but never echoed. The PKL schema marks them writeOnly so this
	// asymmetry does not read as drift.
	for _, writeOnly := range []string{"projects", "filter"} {
		if _, ok := got[writeOnly]; ok {
			t.Errorf("%q is not returned by the API and must not appear in the read document", writeOnly)
		}
	}
}

func TestDrainRead_NotFound(t *testing.T) {
	p := drainProvisioner(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"drain not found"}}`)
	})
	res, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "drain_gone"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("ErrorCode = %v, want NotFound", res.ErrorCode)
	}
}

// =============================================================================
// Update
// =============================================================================

func TestDrainUpdate_OmitsCreateOnlyFields(t *testing.T) {
	var gotMethod, gotPath string
	var body map[string]any
	p := drainProvisioner(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"id":"drain_abc","name":"renamed"}`)
	})

	res, err := p.Update(context.Background(), &resource.UpdateRequest{
		NativeID:          "drain_abc",
		DesiredProperties: logDrainProperties(),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", res.ProgressResult.OperationStatus, res.ProgressResult.StatusMessage)
	}
	if gotMethod != http.MethodPatch || gotPath != "/v1/drains/drain_abc" {
		t.Errorf("request = %s %s, want PATCH /v1/drains/drain_abc", gotMethod, gotPath)
	}
	for _, createOnly := range drain().CreateOnly {
		if _, present := body[createOnly]; present {
			t.Errorf("createOnly field %q must not be sent on update", createOnly)
		}
	}
	for _, mutable := range []string{"name", "projectIds", "delivery", "sampling"} {
		if _, present := body[mutable]; !present {
			t.Errorf("mutable field %q missing from the update body: %v", mutable, body)
		}
	}
}

// =============================================================================
// Delete
// =============================================================================

func TestDrainDelete(t *testing.T) {
	var gotMethod, gotPath string
	p := drainProvisioner(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{}`)
	})
	res, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "drain_abc"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", res.ProgressResult.OperationStatus, res.ProgressResult.StatusMessage)
	}
	if gotMethod != http.MethodDelete || gotPath != "/v1/drains/drain_abc" {
		t.Errorf("request = %s %s, want DELETE /v1/drains/drain_abc", gotMethod, gotPath)
	}
}

// A drain deleted out of band must not fail the apply — delete converges.
func TestDrainDelete_IdempotentOn404(t *testing.T) {
	p := drainProvisioner(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"drain not found"}}`)
	})
	res, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "drain_abc"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v, want Success on a missing drain", res.ProgressResult.OperationStatus)
	}
}

// =============================================================================
// List
// =============================================================================

func TestDrainList_NativeIDs(t *testing.T) {
	var gotPath string
	p := drainProvisioner(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		// GET /v1/drains answers {"drains": [...]}.
		_, _ = io.WriteString(w, `{"drains":[
			{"id":"drain_1","name":"a"},
			{"id":"drain_2","name":"b"}]}`)
	})
	res, err := p.List(context.Background(), &resource.ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if gotPath != "/v1/drains" {
		t.Errorf("path = %s, want /v1/drains", gotPath)
	}
	// Account-scoped: the native id is the bare drain id, no parent prefix.
	if !reflect.DeepEqual(res.NativeIDs, []string{"drain_1", "drain_2"}) {
		t.Errorf("NativeIDs = %v, want [drain_1 drain_2]", res.NativeIDs)
	}
}

// =============================================================================
// Definition shape
// =============================================================================

func TestDrainsGroupIsWellFormed(t *testing.T) {
	group := drains()
	if len(group) == 0 {
		t.Fatal("drains() declared nothing")
	}
	reserved := map[string]bool{"type": true, "target": true, "label": true, "group": true, "stack": true}

	for _, def := range group {
		if strings.Count(def.Type, "::") != 2 || !strings.HasPrefix(def.Type, "VERCEL::Drains::") {
			t.Errorf("%s: want VERCEL::Drains::<Resource>", def.Type)
		}
		if def.CollectionPath == "" {
			t.Errorf("%s: no CollectionPath", def.Type)
		}
		if def.ItemPath == "" && !def.ReadViaCollection {
			t.Errorf("%s: Read has neither an item path nor a collection scan", def.Type)
		}
		if !def.NoDelete && def.ItemPath == "" && def.ItemPathDelete == "" {
			t.Errorf("%s: deletable but no delete path", def.Type)
		}
		if !def.NoUpdate && def.ItemPath == "" && def.ItemPathUpdate == "" {
			t.Errorf("%s: updatable but no update path", def.Type)
		}
		if len(def.Fields) == 0 {
			t.Errorf("%s: no Fields", def.Type)
		}
		if def.Scope != rest.ScopeAccount {
			t.Errorf("%s: drains hang off the account/team, not a parent", def.Type)
		}
		if def.ParentProperty != "" {
			t.Errorf("%s: account-scoped but declares ParentProperty %q", def.Type, def.ParentProperty)
		}
		for _, co := range def.CreateOnly {
			if !contains(def.Fields, co) {
				t.Errorf("%s: createOnly %q is not a declared field, so the exclusion does nothing", def.Type, co)
			}
		}
		for _, f := range def.Fields {
			if reserved[f] && def.Rename[f] == "" {
				t.Errorf("%s: %q is reserved by formae.Resource and needs a Rename", def.Type, f)
			}
		}
		for pkl, api := range def.Rename {
			if !contains(def.Fields, pkl) {
				t.Errorf("%s: rename source %q is not in Fields", def.Type, pkl)
			}
			if api == "" {
				t.Errorf("%s: rename of %q has an empty target", def.Type, pkl)
			}
		}
	}
}

// The three Terraform drain resources are all POST /v1/drains; what tells them
// apart is the `schemas` key. Losing any of these fields would silently drop a
// whole drain kind.
func TestDrainDeclaresTheFieldsAllThreeKindsNeed(t *testing.T) {
	def := drain()
	for _, want := range []string{"name", "projects", "projectIds", "schemas", "delivery", "sampling", "filter", "source"} {
		if !contains(def.Fields, want) {
			t.Errorf("drain is missing field %q", want)
		}
	}
	if def.ListField != "drains" {
		t.Errorf("ListField = %q, want drains", def.ListField)
	}
	if def.NoUpdate {
		t.Error("PATCH /v1/drains/{id} exists; NoUpdate must stay false")
	}
	if def.NoDelete {
		t.Error("DELETE /v1/drains/{id} exists; NoDelete must stay false")
	}
}
