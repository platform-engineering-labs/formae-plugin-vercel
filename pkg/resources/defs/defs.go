// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

// Package defs declares the Vercel resources that fit the generic REST engine
// and registers them.
//
// Every Definition here was written against the endpoint's own API reference
// page: request-body property names come from the documented JSON schema and
// nothing else. Names cannot be inferred, even within one resource — creating a
// DNS record answers with `uid` while listing records returns `id`, and a custom
// environment is `slug` on the wire, not `name`.
//
// Resources needing behaviour the engine does not model live elsewhere:
// VERCEL::Projects::{Project, EnvironmentVariable, Route} are hand-written in
// pkg/resources/projects.
package defs

import (
	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/rest"
	vercelapi "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/transport/vercel"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// groups holds every declared batch of definitions. Each file in this package
// contributes one group from its own init(), so definitions can be added
// without any file editing another — which is what makes the set safe to grow
// from several directions at once.
var groups [][]rest.Definition

// AddGroup registers a batch of definitions. Call it from a file-local init().
func AddGroup(defs []rest.Definition) {
	groups = append(groups, defs)
	for _, def := range defs {
		register(def)
	}
}

// All returns every declared definition, across all groups. Exported so tests
// can assert on the whole set without reaching into package state.
func All() []rest.Definition {
	var out []rest.Definition
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// core is the first group: the resources declared in this file.
func core() []rest.Definition {
	return []rest.Definition{
		customEnvironment(),
		globalConfig(),
		webhook(),
		vcrRepository(),
	}
}

func init() { AddGroup(core()) }

func register(def rest.Definition) {
	ops := []resource.Operation{
		resource.OperationCreate,
		resource.OperationRead,
		resource.OperationList,
	}
	if !def.NoUpdate {
		ops = append(ops, resource.OperationUpdate)
	}
	if !def.NoDelete {
		ops = append(ops, resource.OperationDelete)
	}
	registry.Register(def.Type, ops, func(c *vercelapi.Client, cfg *registry.TargetConfig) prov.Provisioner {
		return rest.New(def, c, cfg.ProjectID)
	})
}

// =============================================================================
// Projects
// =============================================================================

// customEnvironment — POST /v9/projects/{idOrName}/custom-environments.
// The wire name is `slug`, not `name`; `copyEnvVarsFrom` is a create-time
// instruction rather than stored state.
//
// DeleteBody is an empty object on purpose. The documented DELETE takes an
// *optional* body (`deleteUnassignedEnvironmentVariables`), but the endpoint
// rejects a request that has no body at all:
//
//	DELETE /v9/projects/{id}/custom-environments/{envId}   (no body)
//	400 Invalid JSON
//	DELETE …same… with {}
//	200
//
// Verified against the live API on 2026-08-19. An optional body that is
// mandatory in practice cannot be read off the spec, which is why the
// conformance fixture caught it and unit tests could not: it passed Create,
// Verify, Extract, Sync and Update, then failed at Destroy.
func customEnvironment() rest.Definition {
	return rest.Definition{
		Type:           "VERCEL::Projects::CustomEnvironment",
		Scope:          rest.ScopeProject,
		ParentProperty: "projectId",
		CollectionPath: "/v9/projects/{parent}/custom-environments",
		ItemPath:       "/v9/projects/{parent}/custom-environments/{id}",
		ListField:      "environments",
		Fields:         []string{"slug", "description", "branchMatcher", "copyEnvVarsFrom"},
		CreateOnly:     []string{"slug", "copyEnvVarsFrom"},
		DeleteBody:     map[string]any{},
	}
}

// =============================================================================
// Global Config (the API's new name for Edge Config)
// =============================================================================

// globalConfig — POST /v1/global-config takes `{slug, items}`. `slug` is the
// only durable field and changing it replaces the store, so there is nothing
// left for an in-place update to send. Items are a separate concern the engine
// does not model (one batch PATCH for the whole set).
func globalConfig() rest.Definition {
	return rest.Definition{
		Type:           "VERCEL::GlobalConfig::Config",
		Scope:          rest.ScopeAccount,
		CollectionPath: "/v1/global-config",
		ItemPath:       "/v1/global-config/{id}",
		Fields:         []string{"slug"},
		CreateOnly:     []string{"slug"},
		NoUpdate:       true,
	}
}

// =============================================================================
// Webhooks
// =============================================================================

// webhook — POST /v1/webhooks. The API has no PATCH, so every field is
// create-only and any change replaces the webhook.
func webhook() rest.Definition {
	return rest.Definition{
		Type:           "VERCEL::Webhooks::Webhook",
		Scope:          rest.ScopeAccount,
		CollectionPath: "/v1/webhooks",
		ItemPath:       "/v1/webhooks/{id}",
		Fields:         []string{"url", "events", "projectIds"},
		CreateOnly:     []string{"url", "events", "projectIds"},
		NoUpdate:       true,
	}
}

// =============================================================================
// Container registry
// =============================================================================

// vcrRepository — POST /v1/vcr/repository, wrapped as {"repository": {...}}.
// The documented create body takes only projectId and name, and there is no
// update endpoint, so every field is create-only.
func vcrRepository() rest.Definition {
	return rest.Definition{
		Type:  "VERCEL::VCR::Repository",
		Scope: rest.ScopeProject,
		// The project is part of the identity: both GET and DELETE require
		// ?projectId= ("400 missing required property projectId" without it),
		// so it has to be recoverable from the native id — hence the parent.
		ParentProperty: "projectId",
		// ...but the create body takes projectId as a field, not a path
		// segment, so it must also be sent in the body.
		ParentInBody: true,
		// ...and the list is per project too: the path is flat, but
		// GET /v1/vcr/repository without ?projectId= answers 400 exactly as
		// the item path does, so listing walks projects and asks for each.
		CollectionPath: "/v1/vcr/repository",
		ItemPath:       "/v1/vcr/repository/{id}",
		ItemQuery:      map[string]string{"projectId": "{parent}"},
		ListQuery:      map[string]string{"projectId": "{parent}"},
		Unwrap:         "repository",
		ListField:      "repositories",
		Fields:         []string{"name"},
		CreateOnly:     []string{"name"},
		NoUpdate:       true,
	}
}
