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

// Global Config items: no per-item endpoint, the whole set is one batch PATCH
// of {operation, key, value} entries.
func itemsDef() Definition {
	return Definition{
		Type:           "VERCEL::GlobalConfig::Items",
		Scope:          ScopeParent,
		ParentProperty: "edgeConfigId",
		ParentListPath: "/v1/global-config",
		CollectionPath: "/v1/global-config/{parent}/items",
		ListField:      "items",
		Fields:         []string{"items"},
		Bag: &BagSpec{
			Property: "items",
		},
	}
}

// decodeOps reads the batch body the engine sent.
func decodeOps(t *testing.T, r *http.Request) []map[string]any {
	t.Helper()
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body.Items
}

func TestBag_CreateIsOneBatchWrite(t *testing.T) {
	writes := 0
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		writes++
		if r.Method != http.MethodPatch || r.URL.Path != "/v1/global-config/ecfg_1/items" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		ops := decodeOps(t, r)
		if len(ops) != 2 {
			t.Fatalf("ops = %v", ops)
		}
		// Deterministic order keeps the request diffable across applies.
		if ops[0]["key"] != "alpha" || ops[1]["key"] != "beta" {
			t.Errorf("ops not sorted by key: %v", ops)
		}
		if ops[0]["operation"] != "upsert" || ops[0]["value"] != "1" {
			t.Errorf("op = %v", ops[0])
		}
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	p := New(itemsDef(), c, "")
	props, _ := json.Marshal(map[string]any{
		"edgeConfigId": "ecfg_1",
		"items":        map[string]any{"beta": "2", "alpha": "1"},
	})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if writes != 1 {
		t.Errorf("%d writes, want exactly 1", writes)
	}
	pr := res.ProgressResult
	if pr.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", pr.OperationStatus, pr.StatusMessage)
	}
	if pr.NativeID != "ecfg_1" {
		t.Errorf("NativeID = %q, want the parent id", pr.NativeID)
	}
}

func TestBag_CreateRequiresEntries(t *testing.T) {
	p := New(itemsDef(), nil, "")
	props, _ := json.Marshal(map[string]any{"edgeConfigId": "ecfg_1", "items": map[string]any{}})
	res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("ErrorCode = %v, want InvalidRequest", res.ProgressResult.ErrorCode)
	}
}

func TestBag_CreateRejectsNonObjectProperty(t *testing.T) {
	p := New(itemsDef(), nil, "")
	props, _ := json.Marshal(map[string]any{"edgeConfigId": "ecfg_1", "items": "not-a-map"})
	res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("ErrorCode = %v, want InvalidRequest", res.ProgressResult.ErrorCode)
	}
}

func TestBag_CreateReportsAPIFailure(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":"forbidden","message":"nope"}}`)
	})
	p := New(itemsDef(), c, "")
	props, _ := json.Marshal(map[string]any{"edgeConfigId": "ecfg_1", "items": map[string]any{"a": "1"}})
	res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeAccessDenied {
		t.Errorf("ErrorCode = %v, want AccessDenied", res.ProgressResult.ErrorCode)
	}
}

func TestBag_ReadCollapsesTheSetIntoOneProperty(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/global-config/ecfg_1/items" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"items":[
			{"key":"alpha","value":"1","updatedAt":1},
			{"key":"beta","value":{"nested":true},"updatedAt":2}]}`)
	})
	p := New(itemsDef(), c, "")
	res, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "ecfg_1"})
	if res.ErrorCode != "" {
		t.Fatalf("ErrorCode = %v", res.ErrorCode)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(res.Properties), &got)
	if got["edgeConfigId"] != "ecfg_1" {
		t.Errorf("parent property not restored: %v", got)
	}
	items, ok := got["items"].(map[string]any)
	if !ok {
		t.Fatalf("items = %v", got["items"])
	}
	if items["alpha"] != "1" {
		t.Errorf("items = %v", items)
	}
	if nested, ok := items["beta"].(map[string]any); !ok || nested["nested"] != true {
		t.Errorf("non-string values must survive: %v", items["beta"])
	}
}

func TestBag_ReadWhenParentIsGone(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"gone"}}`)
	})
	p := New(itemsDef(), c, "")
	res, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "ecfg_1"})
	if res.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("ErrorCode = %v, want NotFound", res.ErrorCode)
	}
}

// An empty set is an absent bag: there is nothing left for formae to manage,
// and reporting it as present would leave a phantom resource in inventory.
func TestBag_ReadEmptySetIsNotFound(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"items":[]}`)
	})
	p := New(itemsDef(), c, "")
	res, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "ecfg_1"})
	if res.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("ErrorCode = %v, want NotFound", res.ErrorCode)
	}
}

// Removals and upserts go in the same call: the whole set changes atomically or
// not at all.
func TestBag_UpdateUpsertsAndDeletesInOneCall(t *testing.T) {
	writes := 0
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"items":[{"key":"alpha","value":"1"},{"key":"gone","value":"x"}]}`)
			return
		}
		writes++
		ops := decodeOps(t, r)
		if len(ops) != 3 {
			t.Fatalf("ops = %v", ops)
		}
		var deleted, upserted int
		for _, op := range ops {
			switch op["operation"] {
			case "delete":
				deleted++
				if op["key"] != "gone" {
					t.Errorf("wrong key deleted: %v", op)
				}
				if _, hasValue := op["value"]; hasValue {
					t.Errorf("a delete carries no value: %v", op)
				}
			case "upsert":
				upserted++
			default:
				t.Errorf("unexpected operation %v", op)
			}
		}
		if deleted != 1 || upserted != 2 {
			t.Errorf("ops = %v", ops)
		}
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	p := New(itemsDef(), c, "")
	desired, _ := json.Marshal(map[string]any{
		"edgeConfigId": "ecfg_1",
		"items":        map[string]any{"alpha": "1", "beta": "2"},
	})
	res, err := p.Update(context.Background(), &resource.UpdateRequest{NativeID: "ecfg_1", DesiredProperties: desired})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if writes != 1 {
		t.Errorf("%d writes, want exactly 1", writes)
	}
	pr := res.ProgressResult
	if pr.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", pr.OperationStatus, pr.StatusMessage)
	}
	if pr.NativeID != "ecfg_1" {
		t.Errorf("NativeID = %q", pr.NativeID)
	}
}

// Removals are computed against what the API actually holds, not against the
// prior document, so a key added out of band is still cleaned up.
func TestBag_UpdateFailsWhenCurrentStateIsUnreadable(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("must not write before knowing the current set, got %s", r.Method)
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"code":"boom","message":"down"}}`)
	})
	p := New(itemsDef(), c, "")
	desired, _ := json.Marshal(map[string]any{"edgeConfigId": "ecfg_1", "items": map[string]any{"a": "1"}})
	res, _ := p.Update(context.Background(), &resource.UpdateRequest{NativeID: "ecfg_1", DesiredProperties: desired})
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeServiceInternalError {
		t.Errorf("ErrorCode = %v", res.ProgressResult.ErrorCode)
	}
}

func TestBag_DeleteRemovesEveryEntry(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"items":[{"key":"alpha"},{"key":"beta"}]}`)
			return
		}
		ops := decodeOps(t, r)
		if len(ops) != 2 || ops[0]["operation"] != "delete" || ops[0]["key"] != "alpha" {
			t.Errorf("ops = %v", ops)
		}
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	p := New(itemsDef(), c, "")
	res, _ := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "ecfg_1"})
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v", res.ProgressResult.OperationStatus)
	}
}

func TestBag_DeleteOfAnAlreadyEmptyBagWritesNothing(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("nothing to delete, but issued %s", r.Method)
		}
		_, _ = io.WriteString(w, `{"items":[]}`)
	})
	p := New(itemsDef(), c, "")
	res, _ := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "ecfg_1"})
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v", res.ProgressResult.OperationStatus)
	}
}

func TestBag_DeleteIsIdempotentWhenParentIsGone(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"gone"}}`)
	})
	p := New(itemsDef(), c, "")
	res, _ := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "ecfg_1"})
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v, want Success", res.ProgressResult.OperationStatus)
	}
}

// One bag per parent, so discovery enumerates parents.
func TestBag_ListEnumeratesParents(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/global-config" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"configs":[{"id":"ecfg_1"},{"id":"ecfg_2"}]}`)
	})
	p := New(itemsDef(), c, "")
	res, _ := p.List(context.Background(), &resource.ListRequest{})
	if len(res.NativeIDs) != 2 || res.NativeIDs[0] != "ecfg_1" {
		t.Errorf("NativeIDs = %v", res.NativeIDs)
	}
}

// The entry shape is declarable: a bag whose API takes {name,value} pairs and
// no per-entry verb writes exactly that.
func TestBag_CustomEntryShape(t *testing.T) {
	def := itemsDef()
	def.Bag = &BagSpec{
		Property:        "values",
		ItemsField:      "secrets",
		KeyField:        "name",
		ValueField:      "value",
		UpsertOperation: "create",
		DeleteOperation: "remove",
		OperationField:  "action",
		WriteMethod:     "POST",
	}
	def.Fields = []string{"values"}
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		var body struct {
			Secrets []map[string]any `json:"secrets"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Secrets) != 1 {
			t.Fatalf("body = %v", body)
		}
		if body.Secrets[0]["name"] != "TOKEN" || body.Secrets[0]["action"] != "create" {
			t.Errorf("entry = %v", body.Secrets[0])
		}
		_, _ = io.WriteString(w, `{}`)
	})
	p := New(def, c, "")
	props, _ := json.Marshal(map[string]any{"edgeConfigId": "ecfg_1", "values": map[string]any{"TOKEN": "s3cr3t"}})
	res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v: %s", res.ProgressResult.OperationStatus, res.ProgressResult.StatusMessage)
	}
}
