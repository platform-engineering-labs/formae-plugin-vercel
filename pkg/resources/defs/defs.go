// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

// Package defs declares the Vercel resources that fit the generic REST engine
// and registers them.
//
// Every Definition here was written against the endpoint's own API reference
// page — request-body property names come from the documented JSON schema, not
// from the Terraform provider's attribute names. The two disagree often enough
// that inferring one from the other is unsafe: creating a DNS record answers
// with `uid` while listing records returns `id`, and a custom environment's
// name is `slug` on the wire.
//
// Resources needing behaviour the engine does not model live elsewhere:
// VERCEL::Projects::Project and ::EnvironmentVariable are hand-written in
// pkg/resources/projects. See docs/RESOURCES.md for what is still missing and
// why.
package defs

import (
	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/rest"
	vercelapi "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/transport/vercel"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// All returns every engine-driven definition. Exported so tests can assert on
// the set without reaching into package state.
func All() []rest.Definition {
	return []rest.Definition{
		customEnvironment(),
		projectDomain(),
		dnsRecord(),
		globalConfig(),
		webhook(),
		network(),
	}
}

func init() {
	for _, def := range All() {
		register(def)
	}
}

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
	}
}

// projectDomain — POST /v10/projects/{idOrName}/domains, item under /v9.
// Keyed by the domain name itself, not a generated id.
func projectDomain() rest.Definition {
	return rest.Definition{
		Type:           "VERCEL::Projects::Domain",
		Scope:          rest.ScopeProject,
		ParentProperty: "projectId",
		CollectionPath: "/v10/projects/{parent}/domains",
		ItemPath:       "/v9/projects/{parent}/domains/{id}",
		ListField:      "domains",
		IDField:        "name",
		Fields:         []string{"name", "gitBranch", "customEnvironmentId", "redirect", "redirectStatusCode"},
		CreateOnly:     []string{"name"},
	}
}

// =============================================================================
// DNS
// =============================================================================

// dnsRecord — the least regular of the batch: create is POST /v2 and answers
// `{"uid": …}`, update is PATCH /v1 with the record id and *no* domain in the
// path, delete is DELETE /v2 with the domain, and the only read is the /v5
// collection.
func dnsRecord() rest.Definition {
	return rest.Definition{
		Type:              "VERCEL::DNS::Record",
		Scope:             rest.ScopeParent,
		ParentProperty:    "domain",
		ParentListPath:    "/v5/domains",
		ParentListField:   "domains",
		CollectionPath:    "/v2/domains/{parent}/records",
		ItemPathUpdate:    "/v1/domains/records/{id}",
		ItemPathDelete:    "/v2/domains/{parent}/records/{id}",
		ListField:         "records",
		CreateIDField:     "uid",
		ReadViaCollection: true,
		Fields:            []string{"name", "type", "value", "ttl", "comment", "mxPriority", "srv"},
		CreateOnly:        []string{"name", "type"},
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
// Networking
// =============================================================================

// network — Secure Compute network, POST /v1/connect/networks.
//
// Caveat: creation is asynchronous (`status` goes create_in_progress → ready)
// and the engine reports success as soon as the API accepts the request. A
// freshly created network may not be usable yet. Modelling that properly needs
// a Status() poll, which is tracked in docs/RESOURCES.md.
func network() rest.Definition {
	return rest.Definition{
		Type:           "VERCEL::Networking::Network",
		Scope:          rest.ScopeAccount,
		CollectionPath: "/v1/connect/networks",
		ItemPath:       "/v1/connect/networks/{id}",
		ListField:      "networks",
		Fields:         []string{"name", "cidr", "region", "awsAvailabilityZoneIds"},
		CreateOnly:     []string{"cidr", "region", "awsAvailabilityZoneIds"},
	}
}
