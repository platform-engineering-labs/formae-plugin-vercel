// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

// Package vercel is a minimal HTTP client for the Vercel REST API.
//
// Scope is deliberately narrow: Bearer-token auth, JSON in / JSON out,
// automatic team scoping, status-code-driven errors. Resource logic lives in
// pkg/resources/*.
//
// There is no official Vercel Go SDK (the official SDK is TypeScript), and the
// OpenAPI document is a single ~400-endpoint file whose response schemas are
// heavily oneOf-shaped — hand-written structs for the fields we manage are
// smaller and more predictable than generated ones.
package vercel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// DefaultBaseURL is the public Vercel REST API endpoint.
	DefaultBaseURL = "https://api.vercel.com"
	defaultTimeout = 30 * time.Second
	defaultUA      = "formae-plugin-vercel"
)

// Config configures the HTTP client.
type Config struct {
	BaseURL string
	Token   string
	// TeamID and Slug scope every request to a Vercel team. At most one is
	// sent; leaving both empty targets the token's personal account.
	TeamID     string
	Slug       string
	HTTPClient *http.Client
	UserAgent  string
}

// Client talks to the Vercel REST API.
type Client struct {
	baseURL   string
	token     string
	teamID    string
	slug      string
	http      *http.Client
	userAgent string
}

// NewClient constructs a client. Token is required.
func NewClient(cfg Config) (*Client, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("vercel: Token is required")
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	ua := cfg.UserAgent
	if ua == "" {
		ua = defaultUA
	}
	return &Client{
		baseURL:   base,
		token:     cfg.Token,
		teamID:    cfg.TeamID,
		slug:      cfg.Slug,
		http:      hc,
		userAgent: ua,
	}, nil
}

// TeamScope returns the configured team identifier, for logging and for
// deciding whether a target's client needs rebuilding.
func (c *Client) TeamScope() (teamID, slug string) { return c.teamID, c.slug }

// Request describes one API call.
type Request struct {
	Method string
	Path   string // begins with "/", e.g. "/v9/projects/prj_x"
	Body   any    // marshalled to JSON if non-nil
	Query  map[string]string
}

// Do executes a request. On 2xx it decodes the JSON body into out (if non-nil
// and the body is non-empty). On non-2xx it returns *APIError.
func (c *Client) Do(ctx context.Context, req Request, out any) error {
	var bodyReader io.Reader
	if req.Body != nil {
		b, err := json.Marshal(req.Body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, c.baseURL+req.Path, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", c.userAgent)
	if req.Body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	q := httpReq.URL.Query()
	for k, v := range req.Query {
		q.Set(k, v)
	}
	switch {
	case c.teamID != "":
		q.Set("teamId", c.teamID)
	case c.slug != "":
		q.Set("slug", c.slug)
	}
	httpReq.URL.RawQuery = q.Encode()

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code, msg := decodeErrorEnvelope(respBody)
		return &APIError{
			StatusCode: resp.StatusCode,
			Code:       code,
			Message:    msg,
			Body:       string(respBody),
		}
	}

	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// decodeErrorEnvelope extracts code and message from Vercel's error envelope:
// {"error": {"code": "...", "message": "..."}}. Returns empty strings when the
// body is not JSON or does not carry the envelope.
func decodeErrorEnvelope(body []byte) (code, message string) {
	if len(body) == 0 {
		return "", ""
	}
	var probe struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return "", ""
	}
	return probe.Error.Code, probe.Error.Message
}
