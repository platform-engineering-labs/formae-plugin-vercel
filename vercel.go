// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	// Side-effect import: registers the VERCEL::Projects::* resource types.
	_ "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/defs"
	_ "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/projects"

	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/registry"
	vercelapi "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/transport/vercel"
	"github.com/platform-engineering-labs/formae/pkg/model"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// tokenEnvVars are read in order; the first non-empty one wins. VERCEL_TOKEN is
// the Vercel CLI's name, VERCEL_API_TOKEN the official Terraform provider's.
var tokenEnvVars = []string{"VERCEL_TOKEN", "VERCEL_API_TOKEN"}

// ErrNotImplemented is returned for resource types this plugin does not handle.
var ErrNotImplemented = errors.New("resource type not implemented")

// Plugin implements plugin.ResourcePlugin. CRUD/Status/List all delegate to the
// per-resource Provisioner registered by the side-effect import above.
type Plugin struct {
	mu     sync.Mutex
	client *vercelapi.Client
	target *registry.TargetConfig
}

var _ plugin.ResourcePlugin = &Plugin{}

// =============================================================================
// Configuration
// =============================================================================

// RateLimit caps requests across the whole namespace.
//
// Vercel's limits are per endpoint; the tightest one we touch is env-var
// deletion at 60/minute. 5 rps across a mixed workload stays inside every
// bucket while leaving room for bursts. See docs/RESOURCES.md.
func (p *Plugin) RateLimit() model.RateLimitConfig {
	return model.RateLimitConfig{
		Scope:                            model.RateLimitScopeNamespace,
		MaxRequestsPerSecondForNamespace: 5,
	}
}

// DiscoveryFilters returns nil: nothing about a Vercel project or environment
// variable makes it undiscoverable.
func (p *Plugin) DiscoveryFilters() []model.MatchFilter { return nil }

func (p *Plugin) LabelConfig() model.LabelConfig {
	return model.LabelConfig{
		DefaultQuery: "$.name",
		// Overrides for every resource whose identity is not `name`.
		ResourceOverrides: map[string]string{
			"VERCEL::Projects::EnvironmentVariable":   "$.key",
			"VERCEL::Projects::CustomEnvironment":     "$.slug",
			"VERCEL::GlobalConfig::Config":            "$.slug",
			"VERCEL::Webhooks::Webhook":               "$.url",
			"VERCEL::Deployments::Alias":              "$.alias",
			"VERCEL::AccessGroups::ProjectAssignment": "$.projectId",
			"VERCEL::FeatureFlags::Flag":              "$.slug",
			"VERCEL::FeatureFlags::Segment":           "$.slug",
			"VERCEL::FeatureFlags::SDKKey":            "$.keyLabel",
			"VERCEL::Certs::Certificate":              "$.cns[0]",
			"VERCEL::Certs::UploadedCertificate":      "$.id",
		},
	}
}

// =============================================================================
// Dispatch
// =============================================================================

func parseTargetConfig(data json.RawMessage) (*registry.TargetConfig, error) {
	var cfg registry.TargetConfig
	if len(data) == 0 {
		return &cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid target config: %w", err)
	}
	return &cfg, nil
}

func apiToken() string {
	for _, name := range tokenEnvVars {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

// getDeps re-parses the target config on every request. The agent issues
// operations for potentially different targets through the same plugin process
// (discovery sync right after a CRUD apply, extract across two teams, …), so
// caching the first target would silently pin the process to stale team
// scoping. The HTTP client is only rebuilt when the connection parameters
// actually change.
func (p *Plugin) getDeps(targetCfg json.RawMessage) (*vercelapi.Client, *registry.TargetConfig, error) {
	cfg, err := parseTargetConfig(targetCfg)
	if err != nil {
		return nil, nil, err
	}
	token := apiToken()
	if token == "" {
		return nil, nil, fmt.Errorf("one of %v must be set", tokenEnvVars)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client == nil || p.target == nil ||
		p.target.BaseURL != cfg.BaseURL ||
		p.target.TeamID != cfg.TeamID ||
		p.target.Slug != cfg.Slug {
		c, err := vercelapi.NewClient(vercelapi.Config{
			BaseURL: cfg.BaseURL,
			Token:   token,
			TeamID:  cfg.TeamID,
			Slug:    cfg.Slug,
		})
		if err != nil {
			return nil, nil, err
		}
		p.client = c
	}
	p.target = cfg
	return p.client, p.target, nil
}

// dispatch resolves the Provisioner for a resource type. On failure it returns
// an error code the caller embeds in a failure result.
func (p *Plugin) dispatch(resourceType string, targetCfg json.RawMessage) (prov.Provisioner, resource.OperationErrorCode, error) {
	factory, ok := registry.GetFactory(resourceType)
	if !ok {
		return nil, resource.OperationErrorCodeInvalidRequest,
			fmt.Errorf("%w: %q", ErrNotImplemented, resourceType)
	}
	c, t, err := p.getDeps(targetCfg)
	if err != nil {
		return nil, resource.OperationErrorCodeInvalidCredentials, err
	}
	return factory(c, t), "", nil
}

// =============================================================================
// CRUD
// =============================================================================

func (p *Plugin) Create(ctx context.Context, req *resource.CreateRequest) (*resource.CreateResult, error) {
	pr, code, err := p.dispatch(req.ResourceType, req.TargetConfig)
	if err != nil {
		return prov.FailCreate(code, err.Error()), hardError(err)
	}
	return pr.Create(ctx, req)
}

func (p *Plugin) Read(ctx context.Context, req *resource.ReadRequest) (*resource.ReadResult, error) {
	pr, code, err := p.dispatch(req.ResourceType, req.TargetConfig)
	if err != nil {
		return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: code}, hardError(err)
	}
	return pr.Read(ctx, req)
}

func (p *Plugin) Update(ctx context.Context, req *resource.UpdateRequest) (*resource.UpdateResult, error) {
	pr, code, err := p.dispatch(req.ResourceType, req.TargetConfig)
	if err != nil {
		return prov.FailUpdate(code, err.Error()), hardError(err)
	}
	return pr.Update(ctx, req)
}

func (p *Plugin) Delete(ctx context.Context, req *resource.DeleteRequest) (*resource.DeleteResult, error) {
	pr, code, err := p.dispatch(req.ResourceType, req.TargetConfig)
	if err != nil {
		return prov.FailDelete(code, err.Error()), hardError(err)
	}
	return pr.Delete(ctx, req)
}

func (p *Plugin) Status(ctx context.Context, req *resource.StatusRequest) (*resource.StatusResult, error) {
	pr, code, err := p.dispatch(req.ResourceType, req.TargetConfig)
	if err != nil {
		return prov.FailStatus(code, err.Error()), hardError(err)
	}
	return pr.Status(ctx, req)
}

func (p *Plugin) List(ctx context.Context, req *resource.ListRequest) (*resource.ListResult, error) {
	pr, _, err := p.dispatch(req.ResourceType, req.TargetConfig)
	if err != nil {
		// Discovery asks every plugin about every type it knows, so a type this
		// plugin does not handle is genuinely "nothing here" — stay quiet.
		if errors.Is(err, ErrNotImplemented) {
			return &resource.ListResult{NativeIDs: []string{}}, nil
		}
		// Anything else — above all a missing token — is a misconfiguration,
		// not an empty account. Returning an empty list here makes the two
		// indistinguishable: discovery silently finds nothing and there is no
		// way to tell why. Surface it.
		return &resource.ListResult{NativeIDs: []string{}}, err
	}
	return pr.List(ctx, req)
}

// hardError surfaces only "resource type not implemented" as a returned error,
// so an invalid resource type stops a reconcile loop. Credentials and target
// config problems are reported through the result's ErrorCode instead, which
// the SDK already treats as a failure.
func hardError(err error) error {
	if errors.Is(err, ErrNotImplemented) {
		return err
	}
	return nil
}
