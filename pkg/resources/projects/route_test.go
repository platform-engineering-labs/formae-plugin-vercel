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
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func intPtr(i int) *int   { return &i }
func boolPtr(b bool) *bool { return &b }

func redirectProps(t *testing.T) json.RawMessage {
	t.Helper()
	props, err := json.Marshal(RouteProperties{
		ProjectID: "prj_1",
		Name:      "old-blog",
		Enabled:   boolPtr(true),
		Route: &Match{
			Src:    "/blog/:path*",
			Dest:   strPtr("/posts/:path*"),
			Status: intPtr(308),
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return props
}

// The reason this resource is hand-written. POST only stages a version; the
// route is not serving traffic until the version is promoted. A create that
// skips the promote is a green apply over an unchanged production.
func TestRoute_Create_PromotesStagedVersion(t *testing.T) {
	var calls []string
	var promoteBody map[string]any
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/v1/projects/prj_1/routes":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			route, _ := body["route"].(map[string]any)
			if route["name"] != "old-blog" {
				t.Errorf("route.name = %v", route["name"])
			}
			match, _ := route["route"].(map[string]any)
			if match["src"] != "/blog/:path*" || match["dest"] != "/posts/:path*" {
				t.Errorf("route.route = %v", match)
			}
			if match["status"] != float64(308) {
				t.Errorf("status = %v", match["status"])
			}
			_, _ = io.WriteString(w, `{"route":{"id":"route_1","name":"old-blog","staged":true,
				"route":{"src":"/blog/:path*","dest":"/posts/:path*","status":308}},
				"version":{"id":"ver_1","isStaging":true,"isLive":false}}`)
		case "/v1/projects/prj_1/routes/versions":
			_ = json.NewDecoder(r.Body).Decode(&promoteBody)
			_, _ = io.WriteString(w, `{"version":{"id":"ver_1","isLive":true}}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	p := &Route{Client: c}
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: redirectProps(t)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(calls) != 2 || calls[1] != "POST /v1/projects/prj_1/routes/versions" {
		t.Fatalf("create must stage then promote, got %v", calls)
	}
	if promoteBody["id"] != "ver_1" || promoteBody["action"] != "promote" {
		t.Errorf("promote body = %v", promoteBody)
	}
	if res.ProgressResult.NativeID != "prj_1/route_1" {
		t.Errorf("NativeID = %q", res.ProgressResult.NativeID)
	}
}

// A failed promote must fail the operation. The rule exists but is staged, and
// reporting Success would hide that production is unchanged.
func TestRoute_Create_FailsWhenPromoteFails(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/projects/prj_1/routes/versions" {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":{"code":"conflict","message":"staging version changed"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"route":{"id":"route_1","name":"old-blog","route":{"src":"/a"}},
			"version":{"id":"ver_1","isStaging":true}}`)
	})
	p := &Route{Client: c}
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: redirectProps(t)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusFailure {
		t.Fatalf("status = %v, want Failed", res.ProgressResult.OperationStatus)
	}
	if res.ProgressResult.StatusMessage == "" {
		t.Error("a staged-but-unpromoted route must say so in the message")
	}
}

// A version that is already live needs no promote call. Sending one anyway
// would either error or publish something nobody staged.
func TestRoute_Create_SkipsPromoteWhenAlreadyLive(t *testing.T) {
	var calls int
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/v1/projects/prj_1/routes/versions" {
			t.Error("must not promote a version that is already live")
		}
		_, _ = io.WriteString(w, `{"route":{"id":"route_1","name":"old-blog","route":{"src":"/a"}},
			"version":{"id":"ver_1","isLive":true}}`)
	})
	p := &Route{Client: c}
	if _, err := p.Create(context.Background(), &resource.CreateRequest{Properties: redirectProps(t)}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

// Delete is a bulk operation on the collection with the ids in the body, and it
// stages too — an unpromoted delete leaves the rule serving traffic.
func TestRoute_Delete_SendsIDsInBodyAndPromotes(t *testing.T) {
	var calls []string
	var delBody map[string]any
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodDelete {
			_ = json.NewDecoder(r.Body).Decode(&delBody)
			_, _ = io.WriteString(w, `{"deletedCount":1,"version":{"id":"ver_2","isStaging":true}}`)
			return
		}
		_, _ = io.WriteString(w, `{"version":{"id":"ver_2","isLive":true}}`)
	})
	p := &Route{Client: c}
	res, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "prj_1/route_1"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	ids, _ := delBody["routeIds"].([]any)
	if len(ids) != 1 || ids[0] != "route_1" {
		t.Errorf("routeIds = %v", delBody["routeIds"])
	}
	if len(calls) != 2 || calls[0] != "DELETE /v1/projects/prj_1/routes" {
		t.Errorf("calls = %v", calls)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v", res.ProgressResult.OperationStatus)
	}
}

func TestRoute_Update_PatchesItemPathAndPromotes(t *testing.T) {
	var calls []string
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/v1/projects/prj_1/routes/route_1" {
			_, _ = io.WriteString(w, `{"route":{"id":"route_1","name":"old-blog","route":{"src":"/b"}},
				"version":{"id":"ver_3","isStaging":true}}`)
			return
		}
		_, _ = io.WriteString(w, `{"version":{"id":"ver_3","isLive":true}}`)
	})
	p := &Route{Client: c}
	res, err := p.Update(context.Background(), &resource.UpdateRequest{
		NativeID: "prj_1/route_1", DesiredProperties: redirectProps(t),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(calls) != 2 || calls[0] != "PATCH /v1/projects/prj_1/routes/route_1" {
		t.Errorf("calls = %v", calls)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v", res.ProgressResult.OperationStatus)
	}
}

// There is no per-route GET, so Read scans the collection.
func TestRoute_Read_ScansCollection(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/prj_1/routes" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"routes":[
			{"id":"route_0","name":"other","route":{"src":"/x"}},
			{"id":"route_1","name":"old-blog","staged":false,"routeType":"redirect",
			 "rawSrc":"/blog/:path*","route":{"src":"/blog/:path*","dest":"/posts/:path*","status":308}}]}`)
	})
	p := &Route{Client: c}
	res, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1/route_1"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var out RouteProperties
	_ = json.Unmarshal([]byte(res.Properties), &out)
	if out.Name != "old-blog" || out.Route == nil || out.Route.Src != "/blog/:path*" {
		t.Fatalf("read %+v", out)
	}
	if out.ProjectID != "prj_1" {
		t.Errorf("projectId = %q, want prj_1 (recovered from the native id)", out.ProjectID)
	}
	// rawSrc, routeType and staged are API-side extras. Reporting them would be
	// drift on every sync, since no forma sets them.
	var raw map[string]any
	_ = json.Unmarshal([]byte(res.Properties), &raw)
	for _, k := range []string{"rawSrc", "routeType", "staged"} {
		if _, present := raw[k]; present {
			t.Errorf("%q must not be reported as state", k)
		}
	}
}

func TestRoute_Read_NotFound(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"routes":[{"id":"route_0","name":"other","route":{"src":"/x"}}]}`)
	})
	p := &Route{Client: c}
	res, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "prj_1/route_1"})
	if res.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("ErrorCode = %v, want NotFound", res.ErrorCode)
	}
}

func TestRoute_List_WalksProjects(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v10/projects":
			_, _ = io.WriteString(w, `{"projects":[{"id":"prj_1"},{"id":"prj_2"}],"pagination":{"count":2,"next":null}}`)
		default:
			_, _ = io.WriteString(w, `{"routes":[{"id":"route_1","name":"r","route":{"src":"/a"}}]}`)
		}
	})
	p := &Route{Client: c}
	res, err := p.List(context.Background(), &resource.ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"prj_1/route_1", "prj_2/route_1"}
	if len(res.NativeIDs) != 2 || res.NativeIDs[0] != want[0] || res.NativeIDs[1] != want[1] {
		t.Errorf("NativeIDs = %v, want %v", res.NativeIDs, want)
	}
}

// Target config can narrow discovery to one project, the same guard the env-var
// provisioner needs on accounts with many projects.
func TestRoute_List_HonoursProjectScope(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v10/projects" {
			t.Error("a scoped target must not enumerate every project")
		}
		_, _ = io.WriteString(w, `{"routes":[{"id":"route_1","name":"r","route":{"src":"/a"}}]}`)
	})
	p := &Route{Client: c, ProjectScope: "prj_9"}
	res, _ := p.List(context.Background(), &resource.ListRequest{})
	if len(res.NativeIDs) != 1 || res.NativeIDs[0] != "prj_9/route_1" {
		t.Errorf("NativeIDs = %v", res.NativeIDs)
	}
}

func TestRoute_Create_RequiresProjectAndSrc(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the API with an incomplete route")
		w.WriteHeader(http.StatusOK)
	})
	p := &Route{Client: c}
	for _, props := range []RouteProperties{
		{Name: "x", Route: &Match{Src: "/a"}},           // no projectId
		{ProjectID: "prj_1", Route: &Match{Src: "/a"}},   // no name
		{ProjectID: "prj_1", Name: "x"},                  // no route
		{ProjectID: "prj_1", Name: "x", Route: &Match{}}, // no src
	} {
		raw, _ := json.Marshal(props)
		res, _ := p.Create(context.Background(), &resource.CreateRequest{Properties: raw})
		if res.ProgressResult.OperationStatus != resource.OperationStatusFailure {
			t.Errorf("%+v should fail validation", props)
		}
	}
}
