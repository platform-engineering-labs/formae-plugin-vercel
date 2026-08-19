// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package projects

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

func strPtr(s string) *string { return &s }

func TestProject_Create(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v11/projects" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "formae-test" {
			t.Errorf("name = %v", body["name"])
		}
		if body["framework"] != "nextjs" {
			t.Errorf("framework = %v", body["framework"])
		}
		// nodeVersion is not part of the documented create body.
		if _, ok := body["nodeVersion"]; ok {
			t.Error("nodeVersion must not be sent on create")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"prj_1","name":"formae-test","accountId":"acc_1","framework":"nextjs"}`)
	})
	p := &Project{Client: c}
	props, _ := json.Marshal(ProjectProperties{
		Name:        "formae-test",
		Framework:   strPtr("nextjs"),
		NodeVersion: strPtr("22.x"),
	})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", res.ProgressResult.OperationStatus, res.ProgressResult.StatusMessage)
	}
	if res.ProgressResult.NativeID != "prj_1" {
		t.Errorf("NativeID = %q, want prj_1", res.ProgressResult.NativeID)
	}
}

func TestProject_Create_RequiresName(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("API must not be called without a name")
	})
	p := &Project{Client: c}
	res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: json.RawMessage(`{}`)})
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("ErrorCode = %v, want InvalidRequest", res.ProgressResult.ErrorCode)
	}
}

func TestProject_Create_NameConflict(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"code":"conflict","message":"A project with that name already exists"}}`)
	})
	p := &Project{Client: c}
	props, _ := json.Marshal(ProjectProperties{Name: "taken"})
	res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeAlreadyExists {
		t.Errorf("ErrorCode = %v, want AlreadyExists", res.ProgressResult.ErrorCode)
	}
}

func TestProject_Read(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v9/projects/prj_1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":"prj_1","name":"formae-test","accountId":"acc_1",
			"framework":"nextjs","buildCommand":null,"nodeVersion":"22.x"}`)
	})
	p := &Project{Client: c}
	res, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1", ResourceType: ResourceTypeProject})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var got ProjectProperties
	if err := json.Unmarshal([]byte(res.Properties), &got); err != nil {
		t.Fatalf("unmarshal properties: %v", err)
	}
	if got.ID != "prj_1" || got.Name != "formae-test" {
		t.Errorf("props = %+v", got)
	}
	if got.BuildCommand != nil {
		t.Errorf("null buildCommand should stay nil, got %q", *got.BuildCommand)
	}
	// Unmanaged API fields must not leak into the property document, or every
	// sync reports drift.
	var raw map[string]any
	_ = json.Unmarshal([]byte(res.Properties), &raw)
	if _, ok := raw["accountId"]; !ok {
		t.Error("accountId should be surfaced as a read-only property")
	}
	if _, ok := raw["latestDeployments"]; ok {
		t.Error("unmanaged fields must not be surfaced")
	}
}

func TestProject_Read_NotFound(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"Project not found"}}`)
	})
	p := &Project{Client: c}
	res, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_gone"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("ErrorCode = %v, want NotFound", res.ErrorCode)
	}
}

// PATCH must be declarative: a field the user dropped has to be cleared, which
// the API expresses as an explicit null.
func TestProject_Update_SendsNullsForClearedFields(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v9/projects/prj_1" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["buildCommand"] != "npm run build" {
			t.Errorf("buildCommand = %v", body["buildCommand"])
		}
		v, present := body["devCommand"]
		if !present || v != nil {
			t.Errorf("devCommand should be present and null, got %v (present=%v)", v, present)
		}
		if _, present := body["name"]; present {
			t.Error("name is createOnly and must not be sent on update")
		}
		_, _ = io.WriteString(w, `{"id":"prj_1","name":"formae-test","buildCommand":"npm run build"}`)
	})
	p := &Project{Client: c}
	props, _ := json.Marshal(ProjectProperties{
		Name:         "formae-test",
		BuildCommand: strPtr("npm run build"),
	})
	res, err := p.Update(context.Background(), &resource.UpdateRequest{NativeID: "prj_1", DesiredProperties: props})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", res.ProgressResult.OperationStatus, res.ProgressResult.StatusMessage)
	}
}

func TestProject_Delete(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v9/projects/prj_1" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	p := &Project{Client: c}
	res, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "prj_1"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v", res.ProgressResult.OperationStatus)
	}
}

func TestProject_Delete_Idempotent(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"gone"}}`)
	})
	p := &Project{Client: c}
	res, _ := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "prj_gone"})
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("deleting a missing project must succeed, got %v", res.ProgressResult.OperationStatus)
	}
}

func TestProject_List_Paginates(t *testing.T) {
	calls := 0
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = io.WriteString(w, `{"projects":[{"id":"prj_1"},{"id":"prj_2"}],"pagination":{"count":2,"next":1700000000}}`)
			return
		}
		if got := r.URL.Query().Get("from"); got != "1700000000" {
			t.Errorf("from = %q, want continuation token", got)
		}
		_, _ = io.WriteString(w, `{"projects":[{"id":"prj_3"}],"pagination":{"count":1,"next":null}}`)
	})
	p := &Project{Client: c}
	res, err := p.List(context.Background(), &resource.ListRequest{ResourceType: ResourceTypeProject})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"prj_1", "prj_2", "prj_3"}
	if len(res.NativeIDs) != len(want) {
		t.Fatalf("NativeIDs = %v, want %v", res.NativeIDs, want)
	}
	for i := range want {
		if res.NativeIDs[i] != want[i] {
			t.Errorf("NativeIDs[%d] = %q, want %q", i, res.NativeIDs[i], want[i])
		}
	}
}

// Older API versions answer with a bare array instead of {projects, pagination}.
func TestProject_List_BareArray(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"id":"prj_a"},{"id":"prj_b"}]`)
	})
	p := &Project{Client: c}
	res, err := p.List(context.Background(), &resource.ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.NativeIDs) != 2 || res.NativeIDs[0] != "prj_a" {
		t.Errorf("NativeIDs = %v", res.NativeIDs)
	}
}

func TestProject_Status_IsSynchronous(t *testing.T) {
	p := &Project{}
	res, err := p.Status(context.Background(), &resource.StatusRequest{NativeID: "prj_1"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v", res.ProgressResult.OperationStatus)
	}
}

// -- git repository ---------------------------------------------------------

func TestProject_Create_SendsGitRepository(t *testing.T) {
	var body map[string]any
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"id":"prj_1","name":"site",
			"link":{"type":"github","org":"vercel","repo":"next.js","repoId":42}}`)
	})
	p := &Project{Client: c}
	props, _ := json.Marshal(ProjectProperties{
		Name:          "site",
		GitRepository: &GitRepository{Type: "github", Repo: "vercel/next.js"},
	})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, _ := body["gitRepository"].(map[string]any)
	if got == nil {
		t.Fatalf("gitRepository missing from create body: %v", body)
	}
	if got["type"] != "github" || got["repo"] != "vercel/next.js" {
		t.Errorf("gitRepository = %v, want {github vercel/next.js}", got)
	}
	// The create response answers with `link`, not `gitRepository`; the
	// declared shape must come back out or formae drifts immediately.
	var out ProjectProperties
	_ = json.Unmarshal([]byte(string(res.ProgressResult.ResourceProperties)), &out)
	if out.GitRepository == nil || out.GitRepository.Repo != "vercel/next.js" {
		t.Errorf("create result gitRepository = %+v", out.GitRepository)
	}
}

// A project with no repository must not grow a phantom one, or every plain
// project reports drift on every sync.
func TestProject_Create_OmitsGitRepositoryWhenUnset(t *testing.T) {
	var body map[string]any
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"id":"prj_1","name":"site"}`)
	})
	p := &Project{Client: c}
	props, _ := json.Marshal(ProjectProperties{Name: "site"})
	res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: props})
	if _, present := body["gitRepository"]; present {
		t.Errorf("unset gitRepository must not be sent: %v", body)
	}
	var out ProjectProperties
	_ = json.Unmarshal([]byte(string(res.ProgressResult.ResourceProperties)), &out)
	if out.GitRepository != nil {
		t.Errorf("gitRepository = %+v, want nil", out.GitRepository)
	}
}

// Read gets `link`, whose shape differs per provider: GitHub splits owner into
// `org`, GitLab uses projectNamespace/projectName, Bitbucket owner/slug. All
// three have to fold back into the single "owner/name" string that was
// declared, or the resource drifts on every sync.
func TestProject_Read_MapsLinkPerProvider(t *testing.T) {
	for _, tc := range []struct {
		name string
		link string
		want GitRepository
	}{
		{"github", `{"type":"github","org":"vercel","repo":"next.js"}`,
			GitRepository{Type: "github", Repo: "vercel/next.js"}},
		{"github-limited", `{"type":"github-limited","org":"acme","repo":"site"}`,
			GitRepository{Type: "github-limited", Repo: "acme/site"}},
		{"gitlab", `{"type":"gitlab","projectNamespace":"group/sub","projectName":"api"}`,
			GitRepository{Type: "gitlab", Repo: "group/sub/api"}},
		{"bitbucket", `{"type":"bitbucket","owner":"team","slug":"svc"}`,
			GitRepository{Type: "bitbucket", Repo: "team/svc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"id":"prj_1","name":"site","link":`+tc.link+`}`)
			})
			p := &Project{Client: c}
			res, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1"})
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			var out ProjectProperties
			_ = json.Unmarshal([]byte(res.Properties), &out)
			if out.GitRepository == nil {
				t.Fatalf("gitRepository nil, want %+v", tc.want)
			}
			if *out.GitRepository != tc.want {
				t.Errorf("gitRepository = %+v, want %+v", *out.GitRepository, tc.want)
			}
		})
	}
}

// gitRepository cannot be changed in place: PATCH /v9/projects/{id} has 44 body
// properties and none of them is the git link. It is createOnly, so a change
// must reach the API as a replace, never as a silently ignored update.
func TestProject_Update_NeverSendsGitRepository(t *testing.T) {
	var body map[string]any
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"id":"prj_1","name":"site"}`)
	})
	p := &Project{Client: c}
	props, _ := json.Marshal(ProjectProperties{
		Name:          "site",
		GitRepository: &GitRepository{Type: "github", Repo: "vercel/next.js"},
	})
	if _, err := p.Update(context.Background(), &resource.UpdateRequest{
		NativeID: "prj_1", DesiredProperties: props,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, present := body["gitRepository"]; present {
		t.Errorf("update must not send gitRepository: %v", body)
	}
}

// A sourceless link is Vercel's "this project once had a repo, it is
// disconnected now". Reporting it as a live repository would fight the user's
// forma forever.
func TestProject_Read_SourcelessLinkIsNoRepository(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"prj_1","name":"site",
			"link":{"type":"github","org":"vercel","repo":"next.js","sourceless":true}}`)
	})
	p := &Project{Client: c}
	res, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1"})
	var out ProjectProperties
	_ = json.Unmarshal([]byte(res.Properties), &out)
	if out.GitRepository != nil {
		t.Errorf("sourceless link must read as no repository, got %+v", out.GitRepository)
	}
}
