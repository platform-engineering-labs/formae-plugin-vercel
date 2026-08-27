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
