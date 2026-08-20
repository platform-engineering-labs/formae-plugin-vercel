// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package rest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func pagedDef() Definition {
	d := drainDef()
	d.PageParam = "cursor"
	return d
}

// A collection that paginates must be read to the end. Before PageParam the
// engine issued one GET and returned page one, silently.
func TestList_FollowsPaginationCursor(t *testing.T) {
	var cursors []string
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		cursors = append(cursors, r.URL.Query().Get("cursor"))
		switch r.URL.Query().Get("cursor") {
		case "":
			_, _ = io.WriteString(w, `{"drains":[{"id":"d1"},{"id":"d2"}],"pagination":{"next":"c1"}}`)
		case "c1":
			_, _ = io.WriteString(w, `{"drains":[{"id":"d3"}],"pagination":{"next":"c2"}}`)
		default:
			_, _ = io.WriteString(w, `{"drains":[{"id":"d4"}],"pagination":{"next":null}}`)
		}
	})
	res, err := New(pagedDef(), c, "").List(context.Background(), &resource.ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.NativeIDs) != 4 {
		t.Errorf("NativeIDs = %v, want all four across three pages", res.NativeIDs)
	}
	if len(cursors) != 3 || cursors[1] != "c1" || cursors[2] != "c2" {
		t.Errorf("cursors sent = %v", cursors)
	}
}

// A definition that declares no page parameter cannot ask for more, even if the
// response offers a cursor — no guessing at parameter names.
func TestList_WithoutPageParamReadsOnePass(t *testing.T) {
	var calls int32
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = io.WriteString(w, `{"drains":[{"id":"d1"}],"pagination":{"next":"c1"}}`)
	})
	res, err := New(drainDef(), c, "").List(context.Background(), &resource.ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if calls != 1 || len(res.NativeIDs) != 1 {
		t.Errorf("calls=%d ids=%v", calls, res.NativeIDs)
	}
}

// A throttle is transient, so it is retried rather than reported as an empty
// account.
func TestList_RetriesThrottle(t *testing.T) {
	var calls int32
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":"rate_limited","message":"slow down"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"drains":[{"id":"d1"}]}`)
	})
	res, err := New(drainDef(), c, "").List(context.Background(), &resource.ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if calls != 3 || len(res.NativeIDs) != 1 {
		t.Errorf("calls=%d ids=%v", calls, res.NativeIDs)
	}
}

// The important one. A throttle that outlasts the retries must fail, not return
// an empty list: formae acts on an empty list, and "no resources" is how managed
// resources come to look deleted.
func TestList_TransientFailureIsAnError(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"code":"bad_gateway","message":"upstream"}}`)
	})
	res, err := New(drainDef(), c, "").List(context.Background(), &resource.ListRequest{})
	if err == nil {
		t.Fatalf("a 502 must fail List, got %v", res.NativeIDs)
	}
}

// A 403 is a real answer: this token cannot see this resource type, and that
// must not stop the other types from being discovered.
func TestList_PermissionDeniedIsAnEmptyList(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusUnauthorized, http.StatusNotFound} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			c := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
				_, _ = io.WriteString(w, `{"error":{"code":"forbidden","message":"nope"}}`)
			})
			res, err := New(drainDef(), c, "").List(context.Background(), &resource.ListRequest{})
			if err != nil {
				t.Fatalf("%d should read as empty, not fail: %v", code, err)
			}
			if len(res.NativeIDs) != 0 {
				t.Errorf("ids = %v", res.NativeIDs)
			}
		})
	}
}

// A parent the token cannot see is skipped; a parent that fails transiently is
// not, because skipping it under-reports resources that do exist.
func TestList_ProjectScope_SkipsForbiddenParentButFailsOnTransient(t *testing.T) {
	for _, tc := range []struct {
		name      string
		code      int
		wantIDs   int
		wantError bool
	}{
		{"forbidden parent is skipped", http.StatusForbidden, 1, false},
		{"transient parent fails the list", http.StatusServiceUnavailable, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v10/projects":
					_, _ = io.WriteString(w, `{"projects":[{"id":"prj_1"},{"id":"prj_2"}],"pagination":{"count":2,"next":null}}`)
				case "/v9/projects/prj_1/custom-environments":
					_, _ = io.WriteString(w, `{"environments":[{"id":"env_1"}]}`)
				default:
					w.WriteHeader(tc.code)
					_, _ = io.WriteString(w, `{"error":{"code":"x","message":"y"}}`)
				}
			})
			res, err := New(customEnvDef(), c, "").List(context.Background(), &resource.ListRequest{})
			if tc.wantError {
				if err == nil {
					t.Fatalf("want error, got ids %v", res.NativeIDs)
				}
				return
			}
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(res.NativeIDs) != tc.wantIDs {
				t.Errorf("ids = %v", res.NativeIDs)
			}
		})
	}
}
