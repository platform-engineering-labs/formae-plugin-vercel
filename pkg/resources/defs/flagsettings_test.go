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
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/rest"
	vercelapi "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/transport/vercel"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// settingsClient wires a Definition to an offline httptest server. Nothing in
// this file may reach api.vercel.com.
func settingsClient(t *testing.T, h http.HandlerFunc) *vercelapi.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := vercelapi.NewClient(vercelapi.Config{BaseURL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// -- feature-flag settings: the plugin's first singleton --------------------

// A singleton has no id of its own: the native id is the project, and both
// create and update are the same PATCH against a fixed path.
func TestFeatureFlagSettings_SingletonShape(t *testing.T) {
	var calls []string
	c := settingsClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		_, _ = io.WriteString(w, `{"enabled":true,"projectId":"prj_1","ownerId":"acc_1","typeName":"settings"}`)
	})
	p := rest.New(featureFlagSettings(), c, "")
	props, _ := json.Marshal(map[string]any{"projectId": "prj_1", "enabled": true})

	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := res.ProgressResult.NativeID; got != "prj_1" {
		t.Errorf("NativeID = %q, want the project id alone", got)
	}
	if calls[0] != "PATCH /v1/projects/prj_1/feature-flags/settings" {
		t.Errorf("create call = %q; there is no POST for settings", calls[0])
	}

	read, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(read.Properties), &got)
	if got["enabled"] != true {
		t.Errorf("read = %v", got)
	}
	if _, present := got["id"]; present {
		t.Error("a singleton has no id of its own; reporting one would drift")
	}
	for _, leaked := range []string{"ownerId", "typeName"} {
		if _, present := got[leaked]; present {
			t.Errorf("undeclared field %q leaked into state", leaked)
		}
	}
}

// The endpoint has no DELETE, and NoDelete would make every stack containing
// these settings impossible to destroy — formae asks the plugin to delete, the
// plugin refuses, the destroy fails. Deleting therefore resets the settings to
// the state a project has before anyone touches them.
func TestFeatureFlagSettings_DeleteResetsToDisabled(t *testing.T) {
	var method, path string
	var body map[string]any
	c := settingsClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"enabled":false,"projectId":"prj_1"}`)
	})
	p := rest.New(featureFlagSettings(), c, "")
	res, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "prj_1"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if method != http.MethodPatch || path != "/v1/projects/prj_1/feature-flags/settings" {
		t.Errorf("delete called %s %s", method, path)
	}
	if body["enabled"] != false {
		t.Errorf("delete body = %v, want enabled=false", body)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v", res.ProgressResult.OperationStatus)
	}
}

// List enumerates projects: every project has settings, whether or not anyone
// has touched them.
func TestFeatureFlagSettings_ListEnumeratesProjects(t *testing.T) {
	c := settingsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v10/projects" {
			_, _ = io.WriteString(w, `{"projects":[{"id":"prj_1"},{"id":"prj_2"}],"pagination":{"count":2,"next":null}}`)
			return
		}
		t.Errorf("unexpected path %s", r.URL.Path)
	})
	p := rest.New(featureFlagSettings(), c, "")
	res, err := p.List(context.Background(), &resource.ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.NativeIDs) != 2 || res.NativeIDs[0] != "prj_1" || res.NativeIDs[1] != "prj_2" {
		t.Errorf("NativeIDs = %v, want the two project ids", res.NativeIDs)
	}
}
