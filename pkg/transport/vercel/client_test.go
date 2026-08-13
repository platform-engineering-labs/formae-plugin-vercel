// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package vercel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(t *testing.T, cfg Config, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg.BaseURL = srv.URL
	if cfg.Token == "" {
		cfg.Token = "tok"
	}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestNewClient_RequiresToken(t *testing.T) {
	if _, err := NewClient(Config{}); err == nil {
		t.Fatal("expected error when Token is empty")
	}
}

func TestNewClient_DefaultBaseURL(t *testing.T) {
	c, err := NewClient(Config{Token: "tok"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, DefaultBaseURL)
	}
}

func TestDo_SetsAuthHeaders(t *testing.T) {
	c := newTestClient(t, Config{Token: "secret"}, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("User-Agent not set")
		}
		_, _ = io.WriteString(w, `{}`)
	})
	if err := c.Do(context.Background(), Request{Method: "GET", Path: "/v9/projects/x"}, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
}

// Team scoping is transport-level: resource code must never have to remember it.
func TestDo_InjectsTeamID(t *testing.T) {
	c := newTestClient(t, Config{TeamID: "team_123"}, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("teamId"); got != "team_123" {
			t.Errorf("teamId = %q, want team_123", got)
		}
		if got := r.URL.Query().Get("decrypt"); got != "true" {
			t.Errorf("per-request query lost: decrypt = %q", got)
		}
		_, _ = io.WriteString(w, `{}`)
	})
	err := c.Do(context.Background(), Request{
		Method: "GET", Path: "/v10/projects/p/env", Query: map[string]string{"decrypt": "true"},
	}, nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
}

func TestDo_InjectsSlug(t *testing.T) {
	c := newTestClient(t, Config{Slug: "my-team"}, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("slug"); got != "my-team" {
			t.Errorf("slug = %q", got)
		}
		if r.URL.Query().Has("teamId") {
			t.Error("teamId should not be sent when only slug is configured")
		}
		_, _ = io.WriteString(w, `{}`)
	})
	if err := c.Do(context.Background(), Request{Method: "GET", Path: "/v2/teams"}, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
}

func TestDo_NoTeamScopeWhenUnset(t *testing.T) {
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("teamId") || r.URL.Query().Has("slug") {
			t.Errorf("unexpected team scope: %v", r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{}`)
	})
	if err := c.Do(context.Background(), Request{Method: "GET", Path: "/v10/projects"}, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
}

func TestDo_SendsJSONBody(t *testing.T) {
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["name"] != "demo" {
			t.Errorf("body name = %v", body["name"])
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"prj_1","name":"demo"}`)
	})
	var out struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	err := c.Do(context.Background(), Request{
		Method: "POST", Path: "/v11/projects", Body: map[string]any{"name": "demo"},
	}, &out)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out.ID != "prj_1" {
		t.Errorf("out.ID = %q", out.ID)
	}
}

func TestDo_DecodesErrorEnvelope(t *testing.T) {
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"Could not find the project"}}`)
	})
	err := c.Do(context.Background(), Request{Method: "GET", Path: "/v9/projects/nope"}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.StatusCode != 404 || apiErr.Code != "not_found" || apiErr.Message != "Could not find the project" {
		t.Errorf("apiErr = %+v", apiErr)
	}
	if !IsNotFound(err) {
		t.Error("IsNotFound = false, want true")
	}
}

func TestDo_ErrorWithoutEnvelope(t *testing.T) {
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `<html>bad gateway</html>`)
	})
	err := c.Do(context.Background(), Request{Method: "GET", Path: "/x"}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.StatusCode != 502 || apiErr.Body == "" {
		t.Errorf("apiErr = %+v", apiErr)
	}
}

// DELETE endpoints answer 204 with no body; decoding into out must not fail.
func TestDo_EmptyBody(t *testing.T) {
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	var out map[string]any
	if err := c.Do(context.Background(), Request{Method: "DELETE", Path: "/v9/projects/x"}, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
}
