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
		projectDomain(),
		dnsRecord(),
		domain(),
		network(),
		accessGroup(),
		accessGroupProject(),
		authToken(),
		alias(),
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

// dnsRecord — the least regular of the batch: create is POST /v2 and answers
// `{"uid": …}`, update is PATCH /v1 with the record id and *no* domain in the
// path, delete is DELETE /v2 with the domain, and the only read is the /v5
// collection.
func dnsRecord() rest.Definition {
	return rest.Definition{
		Type:            "VERCEL::DNS::Record",
		Scope:           rest.ScopeParent,
		ParentProperty:  "domain",
		ParentListPath:  "/v5/domains",
		ParentListField: "domains",
		// Parents are keyed by name, not by id. A domain object carries both —
		// `id` is an opaque `Qmb4E6…` handle, `name` is "example.com" — and every
		// record path takes the name. Defaulting to `id` made discovery walk
		// /v2/domains/Qmb4E6…/records and find nothing, while Read stayed
		// healthy because it takes the domain from the native id instead. A
		// resource can be perfectly readable and wholly undiscoverable.
		ParentIDField:     "name",
		CollectionPath:    "/v2/domains/{parent}/records",
		ItemPathUpdate:    "/v1/domains/records/{id}",
		ItemPathDelete:    "/v2/domains/{parent}/records/{id}",
		ListField:         "records",
		CreateIDField:     "uid",
		ReadViaCollection: true,
		// No in-place update, despite the API documenting a PATCH.
		//
		// PATCH /v1/domains/records/{id} does not edit the record: it replaces
		// it and answers with a different `rec_…` id. formae requires a native
		// id to be stable across an update — the conformance harness rejects a
		// change outright ("NativeID should NOT change during update") — so an
		// endpoint that reissues the id is, in formae's terms, not an update at
		// all. NoUpdate makes a change destroy and recreate the record, which
		// is what the API does anyway, and keeps the id honest.
		NoUpdate: true,
		Fields:   []string{"name", "recordType", "value", "ttl", "comment", "mxPriority", "srv"},
		Rename:   map[string]string{"recordType": "type"},
		// Every field, because there is no update: a field that is neither
		// updatable nor createOnly could never be changed at all, and formae
		// would attempt an update the plugin has to refuse.
		CreateOnly: []string{"name", "recordType", "value", "ttl", "comment", "mxPriority", "srv"},
	}
}

// domain — POST /v7/domains registers a domain on the account or team.
//
// This is the step that DNS::Record and Projects::Domain both presuppose:
// records need a domain the account holds, and attaching a domain to a project
// does not bring the domain itself under management.
//
// Addressed by name everywhere — GET /v5/domains/{domain},
// DELETE /v6/domains/{domain} — so the native id is the domain name, not the
// `dom_…` id the object also carries.
//
// NoUpdate is deliberate. PATCH /v3/domains/{domain} is op-based
// (`{op, zone, renew, customNameservers}` or `{op, destination}` to move the
// domain out), the `op` values are not enumerated in the spec, and the 200 has
// no response body — nothing that can be driven declaratively. Only `name` is
// modelled, and it is createOnly, so there is nothing to update: everything
// else the domain object returns (nameservers, verified, expiresAt) is
// Vercel's to decide, not the forma's.
func domain() rest.Definition {
	return rest.Definition{
		Type:           "VERCEL::Domains::Domain",
		Scope:          rest.ScopeAccount,
		CollectionPath: "/v7/domains",
		ItemPath:       "/v5/domains/{id}",
		ItemPathDelete: "/v6/domains/{id}",
		ListPath:       "/v5/domains",
		ListField:      "domains",
		Unwrap:         "domain",
		IDField:        "name",
		Fields:         []string{"name"},
		CreateOnly:     []string{"name"},
		NoUpdate:       true,
	}
}

// network — Secure Compute network, POST /v1/connect/networks.
//
// Creation is asynchronous: the documented `status` enum is
// create_in_progress | delete_in_progress | error | ready. The Async spec makes
// Create return InProgress and Status() poll until the network is actually
// usable — reporting success early would let dependent resources run against a
// network that does not exist yet.
func network() rest.Definition {
	return rest.Definition{
		Type:           "VERCEL::Networking::Network",
		Scope:          rest.ScopeAccount,
		CollectionPath: "/v1/connect/networks",
		ItemPath:       "/v1/connect/networks/{id}",
		ListField:      "networks",
		Fields:         []string{"name", "cidr", "region", "awsAvailabilityZoneIds"},
		CreateOnly:     []string{"cidr", "region", "awsAvailabilityZoneIds"},
		Async: &rest.AsyncSpec{
			StatusField: "status",
			Pending:     []string{"create_in_progress", "delete_in_progress"},
			Failed:      []string{"error"},
			Ready:       []string{"ready"},
		},
	}
}

// accessGroup — POST /v1/access-groups. Two oddities: the id is
// `accessGroupId`, not `id`, and the update verb is POST, not PATCH.
//
// `projects` and `membersToAdd` are accepted on create but the read response
// only returns counts, so managing them here would drift on every sync. They
// are modelled as their own resources instead (ProjectAssignment below, and
// membership, which is still pending).
func accessGroup() rest.Definition {
	return rest.Definition{
		Type:           "VERCEL::AccessGroups::AccessGroup",
		Scope:          rest.ScopeAccount,
		CollectionPath: "/v1/access-groups",
		ItemPath:       "/v1/access-groups/{id}",
		UpdateMethod:   "POST",
		IDField:        "accessGroupId",
		Fields:         []string{"name"},
	}
}

// accessGroupProject — POST /v1/access-groups/{accessGroupIdOrName}/projects.
// Keyed by the project id rather than an id of its own.
func accessGroupProject() rest.Definition {
	return rest.Definition{
		Type:           "VERCEL::AccessGroups::ProjectAssignment",
		Scope:          rest.ScopeParent,
		ParentProperty: "accessGroupId",
		ParentListPath: "/v1/access-groups",
		ParentIDField:  "accessGroupId",
		CollectionPath: "/v1/access-groups/{parent}/projects",
		ItemPath:       "/v1/access-groups/{parent}/projects/{id}",
		IDField:        "projectId",
		Fields:         []string{"projectId", "role"},
		CreateOnly:     []string{"projectId"},
	}
}

// authToken — POST /v3/user/tokens, wrapped as {"token": {...}}. Read is /v5,
// delete is /v3, list is /v6: three versions for one resource.
//
// The token's actual value (`bearerToken`) is returned exactly once, at
// creation, and is deliberately not modelled: formae would report it as drift
// on the very next read.
func authToken() rest.Definition {
	return rest.Definition{
		Type:           "VERCEL::Auth::Token",
		Scope:          rest.ScopeAccount,
		CollectionPath: "/v3/user/tokens",
		ItemPath:       "/v5/user/tokens/{id}",
		ItemPathDelete: "/v3/user/tokens/{id}",
		ListPath:       "/v6/user/tokens",
		ListField:      "tokens",
		Unwrap:         "token",
		Fields:         []string{"name", "expiresAt", "projectId"},
		CreateOnly:     []string{"name", "expiresAt", "projectId"},
		NoUpdate:       true,
	}
}

// alias — created under a deployment (POST /v2/deployments/{id}/aliases) but
// read, listed and deleted account-wide, so the deployment is a create-time
// path input rather than part of the native id. The create response keys the id
// as `uid`.
func alias() rest.Definition {
	return rest.Definition{
		Type:           "VERCEL::Deployments::Alias",
		Scope:          rest.ScopeAccount,
		CreatePath:     "/v2/deployments/{prop:deploymentId}/aliases",
		CollectionPath: "/v4/aliases",
		ItemPath:       "/v4/aliases/{id}",
		ItemPathDelete: "/v2/aliases/{id}",
		ListField:      "aliases",
		IDField:        "uid",
		CreateIDField:  "uid",
		Fields:         []string{"alias", "redirect", "deploymentId"},
		CreateOnly:     []string{"alias", "redirect", "deploymentId"},
		NoUpdate:       true,
	}
}
