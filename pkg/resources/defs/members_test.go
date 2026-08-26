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
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/rest"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func memberProps(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"projectId": "prj_1",
		"uid":       "usr_9",
		"role":      "PROJECT_DEVELOPER",
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The create response is the *project's* id, so the native id has to come from
// the desired uid. Taking it from the response would address the project as if
// it were the member, and every later Read and Delete would hit the wrong thing.
func TestProjectMember_NativeIDComesFromUID(t *testing.T) {
	c := drainsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/projects/prj_1/members" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["uid"] != "usr_9" || body["role"] != "PROJECT_DEVELOPER" {
			t.Errorf("body = %v", body)
		}
		if _, present := body["projectId"]; present {
			t.Error("the project is a path segment, not a body field")
		}
		// Documented response: the project id.
		_, _ = io.WriteString(w, `{"id":"prj_1"}`)
	})
	p := rest.New(projectMember(), c, "")
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: memberProps(t)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := res.ProgressResult.NativeID; got != "prj_1/usr_9" {
		t.Errorf("NativeID = %q, want prj_1/usr_9", got)
	}
}

// There is no per-member GET, so Read scans the collection and matches on uid.
func TestProjectMember_ReadScansCollection(t *testing.T) {
	c := drainsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/prj_1/members" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"members":[
			{"uid":"usr_1","role":"ADMIN","email":"a@example.com","avatar":"x"},
			{"uid":"usr_9","role":"PROJECT_DEVELOPER","email":"b@example.com","computedProjectRole":"PROJECT_DEVELOPER"}]}`)
	})
	p := rest.New(projectMember(), c, "")
	res, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1/usr_9"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(res.Properties), &got)
	if got["role"] != "PROJECT_DEVELOPER" || got["projectId"] != "prj_1" {
		t.Errorf("read = %v", got)
	}
	// Fields the forma does not declare must not come back as state.
	for _, leaked := range []string{"email", "avatar", "computedProjectRole"} {
		if _, present := got[leaked]; present {
			t.Errorf("undeclared field %q leaked into state", leaked)
		}
	}
}

func TestProjectMember_DeleteAddressesUID(t *testing.T) {
	var path string
	c := drainsClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = io.WriteString(w, `{"id":"prj_1"}`)
	})
	p := rest.New(projectMember(), c, "")
	if _, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "prj_1/usr_9"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if path != "/v1/projects/prj_1/members/usr_9" {
		t.Errorf("path = %q", path)
	}
}

// A membership has no update endpoint, so a role change must surface as a
// replace rather than a silently ignored PATCH.
func TestProjectMember_HasNoUpdate(t *testing.T) {
	def := projectMember()
	if !def.NoUpdate {
		t.Error("the API has no PATCH for a project member; NoUpdate must be set")
	}
	p := rest.New(def, nil, "")
	res, _ := p.Update(context.Background(), &resource.UpdateRequest{
		NativeID: "prj_1/usr_9", DesiredProperties: memberProps(t),
	})
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("ErrorCode = %v, want InvalidRequest", res.ProgressResult.ErrorCode)
	}
}

// -- feature-flag settings: the plugin's first singleton --------------------

// A singleton has no id of its own: the native id is the project, and both
// create and update are the same PATCH against a fixed path.
func TestFeatureFlagSettings_SingletonShape(t *testing.T) {
	var calls []string
	c := drainsClient(t, func(w http.ResponseWriter, r *http.Request) {
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
	c := drainsClient(t, func(w http.ResponseWriter, r *http.Request) {
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
	c := drainsClient(t, func(w http.ResponseWriter, r *http.Request) {
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
