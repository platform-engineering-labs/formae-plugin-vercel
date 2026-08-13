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

// A Secure Compute network: POST answers `status: create_in_progress` and only
// later becomes `ready`.
func networkDef() Definition {
	return Definition{
		Type:           "VERCEL::Networking::Network",
		Scope:          ScopeAccount,
		CollectionPath: "/v1/connect/networks",
		ItemPath:       "/v1/connect/networks/{id}",
		ListField:      "networks",
		Fields:         []string{"name", "cidr", "region"},
		CreateOnly:     []string{"cidr", "region"},
		Async: &AsyncSpec{
			StatusField: "status",
			Pending:     []string{"create_in_progress", "update_in_progress"},
			Failed:      []string{"create_failed", "error"},
			Ready:       []string{"ready"},
		},
	}
}

func createNetwork(t *testing.T, p *Resource) *resource.CreateResult {
	t.Helper()
	props, _ := json.Marshal(map[string]any{"name": "net", "cidr": "10.0.0.0/16", "region": "iad1"})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return res
}

// A create the API has only accepted is not a create that finished. Reporting
// Success here is what breaks every resource that depends on this one.
func TestAsyncCreate_PendingReportsInProgress(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"net_1","name":"net","status":"create_in_progress"}`)
	})
	res := createNetwork(t, New(networkDef(), c, ""))
	pr := res.ProgressResult
	if pr.OperationStatus != resource.OperationStatusInProgress {
		t.Fatalf("status = %v, want InProgress", pr.OperationStatus)
	}
	if pr.NativeID != "net_1" {
		t.Errorf("NativeID = %q", pr.NativeID)
	}
	// Status() is called with the RequestID; without one the agent cannot poll.
	if pr.RequestID != "net_1" {
		t.Errorf("RequestID = %q, want net_1", pr.RequestID)
	}
}

func TestAsyncCreate_ReadyReportsSuccess(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"net_1","name":"net","status":"ready"}`)
	})
	res := createNetwork(t, New(networkDef(), c, ""))
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v, want Success", res.ProgressResult.OperationStatus)
	}
}

func TestAsyncCreate_FailedStatusFailsTheCreate(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"net_1","name":"net","status":"create_failed"}`)
	})
	res := createNetwork(t, New(networkDef(), c, ""))
	pr := res.ProgressResult
	if pr.OperationStatus != resource.OperationStatusFailure {
		t.Fatalf("status = %v, want Failure", pr.OperationStatus)
	}
	if pr.StatusMessage == "" {
		t.Error("a failed create must say which state it observed")
	}
}

// A create response with no status field at all proves nothing about
// readiness, so it must be polled rather than assumed ready.
func TestAsyncCreate_MissingStatusIsPolled(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"net_1","name":"net"}`)
	})
	res := createNetwork(t, New(networkDef(), c, ""))
	if res.ProgressResult.OperationStatus != resource.OperationStatusInProgress {
		t.Fatalf("status = %v, want InProgress", res.ProgressResult.OperationStatus)
	}
}

// Polling is driven by the agent calling Status() again, never by sleeping.
func TestAsyncStatus_PollsUntilReady(t *testing.T) {
	calls := 0
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/connect/networks/net_1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		calls++
		if calls == 1 {
			_, _ = io.WriteString(w, `{"id":"net_1","status":"create_in_progress"}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"net_1","name":"net","status":"ready"}`)
	})
	p := New(networkDef(), c, "")

	first, err := p.Status(context.Background(), &resource.StatusRequest{NativeID: "net_1", RequestID: "net_1"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if first.ProgressResult.OperationStatus != resource.OperationStatusInProgress {
		t.Fatalf("first poll = %v, want InProgress", first.ProgressResult.OperationStatus)
	}

	second, _ := p.Status(context.Background(), &resource.StatusRequest{NativeID: "net_1", RequestID: "net_1"})
	if second.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("second poll = %v, want Success", second.ProgressResult.OperationStatus)
	}
	// The freshly read state is what the agent stores.
	var got map[string]any
	_ = json.Unmarshal(second.ProgressResult.ResourceProperties, &got)
	if got["name"] != "net" {
		t.Errorf("properties = %v", got)
	}
}

func TestAsyncStatus_FailedState(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"net_1","status":"error"}`)
	})
	p := New(networkDef(), c, "")
	res, _ := p.Status(context.Background(), &resource.StatusRequest{NativeID: "net_1"})
	if res.ProgressResult.OperationStatus != resource.OperationStatusFailure {
		t.Fatalf("status = %v, want Failure", res.ProgressResult.OperationStatus)
	}
}

// The resource vanished between create and poll: report it, do not spin.
func TestAsyncStatus_GoneReportsNotFound(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"gone"}}`)
	})
	p := New(networkDef(), c, "")
	res, _ := p.Status(context.Background(), &resource.StatusRequest{NativeID: "net_1"})
	pr := res.ProgressResult
	if pr.OperationStatus != resource.OperationStatusFailure {
		t.Fatalf("status = %v, want Failure", pr.OperationStatus)
	}
	if pr.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("ErrorCode = %v, want NotFound", pr.ErrorCode)
	}
}

// Status is also reachable with only the RequestID from the create result.
func TestAsyncStatus_FallsBackToRequestID(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/connect/networks/net_1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":"net_1","status":"ready"}`)
	})
	p := New(networkDef(), c, "")
	res, _ := p.Status(context.Background(), &resource.StatusRequest{RequestID: "net_1"})
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v", res.ProgressResult.OperationStatus)
	}
}

// An unlisted state is not success: it is something we do not understand yet,
// and only the poll timeout may end it.
func TestAsyncStatus_UnknownStateKeepsPolling(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"net_1","status":"suspended"}`)
	})
	p := New(networkDef(), c, "")
	res, _ := p.Status(context.Background(), &resource.StatusRequest{NativeID: "net_1"})
	if res.ProgressResult.OperationStatus != resource.OperationStatusInProgress {
		t.Fatalf("status = %v, want InProgress", res.ProgressResult.OperationStatus)
	}
}

// With no Ready list, anything that is neither pending nor failed is ready —
// so a definition need only name the states it must wait on.
func TestAsyncStatus_NoReadyListMeansAnythingElseIsReady(t *testing.T) {
	def := networkDef()
	def.Async.Ready = nil
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"net_1","status":"whatever"}`)
	})
	p := New(def, c, "")
	res, _ := p.Status(context.Background(), &resource.StatusRequest{NativeID: "net_1"})
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v, want Success", res.ProgressResult.OperationStatus)
	}
}

// Update is asynchronous for the same reason create is.
func TestAsyncUpdate_PendingReportsInProgress(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"net_1","name":"net2","status":"update_in_progress"}`)
	})
	p := New(networkDef(), c, "")
	props, _ := json.Marshal(map[string]any{"name": "net2"})
	res, _ := p.Update(context.Background(), &resource.UpdateRequest{NativeID: "net_1", DesiredProperties: props})
	pr := res.ProgressResult
	if pr.OperationStatus != resource.OperationStatusInProgress {
		t.Fatalf("status = %v, want InProgress", pr.OperationStatus)
	}
	if pr.RequestID != "net_1" {
		t.Errorf("RequestID = %q", pr.RequestID)
	}
}

// A resource that scans a collection instead of an item endpoint still polls.
func TestAsyncStatus_ViaCollection(t *testing.T) {
	def := networkDef()
	def.ReadViaCollection = true
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/connect/networks" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"networks":[{"id":"net_1","status":"ready"}]}`)
	})
	p := New(def, c, "")
	res, _ := p.Status(context.Background(), &resource.StatusRequest{NativeID: "net_1"})
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v", res.ProgressResult.OperationStatus)
	}
}

func TestAsyncStatus_BadNativeIDIsRejected(t *testing.T) {
	def := customEnvDef()
	def.Async = &AsyncSpec{StatusField: "status", Pending: []string{"pending"}}
	p := New(def, nil, "")
	res, _ := p.Status(context.Background(), &resource.StatusRequest{NativeID: "no-slash"})
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("ErrorCode = %v, want InvalidRequest", res.ProgressResult.ErrorCode)
	}
}
