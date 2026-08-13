// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

// Package rest turns a declarative Definition into a prov.Provisioner.
//
// Most Vercel resources are the same shape: POST to a collection path, GET/
// PATCH/DELETE an item path, GET the collection to list. Writing ~200 lines of
// Go per resource for fifty of those is not worth it, so those are declared
// here instead and the CRUD is executed generically.
//
// Resources with genuine quirks — VERCEL::Projects::Project (declarative PATCH
// with explicit nulls) and VERCEL::Projects::EnvironmentVariable (the
// {created, failed} create envelope) — stay hand-written in
// pkg/resources/projects. The engine is for the regular ones, not a universal
// solvent.
package rest

import "strings"

// Scope says what a resource hangs off, which decides both the native-id shape
// and how List enumerates.
type Scope int

const (
	// ScopeAccount: the resource belongs to the token's account or team.
	// Native id is just the resource id.
	ScopeAccount Scope = iota
	// ScopeProject: the resource belongs to a project. Native id is
	// "{projectId}/{id}" and List walks projects.
	ScopeProject
	// ScopeParent: the resource belongs to another account-level resource
	// (an access group, an Edge Config, a repository). Native id is
	// "{parentId}/{id}" and List walks the parent collection.
	ScopeParent
)

// Definition declares one resource type. Paths are templates: "{parent}" is
// replaced with the parent id and "{id}" with the resource id.
type Definition struct {
	// Type is the formae resource type, e.g. "VERCEL::Drains::Drain".
	Type string

	// Scope decides native-id shape and List enumeration.
	Scope Scope

	// ParentProperty is the property carrying the parent id — "projectId" for
	// ScopeProject, or e.g. "edgeConfigId" for ScopeParent. Empty for
	// ScopeAccount.
	ParentProperty string

	// ParentListPath is the collection used to enumerate parents during
	// discovery when Scope is ScopeParent, e.g. "/v1/global-config".
	// ScopeProject uses the project list and ignores this.
	ParentListPath string
	// ParentListField is the key holding the array in ParentListPath's
	// response; empty means the response is a bare array.
	ParentListField string
	// ParentIDField is the key holding a parent's id in that collection.
	// Defaults to "id"; access groups, for instance, key theirs
	// `accessGroupId`.
	ParentIDField string

	// CollectionPath is POSTed to for Create and GETed for List.
	CollectionPath string

	// CreatePath overrides CollectionPath for Create, for resources created
	// under one parent but addressed elsewhere afterwards: an alias is created
	// under a deployment (POST /v2/deployments/{id}/aliases) but read, listed
	// and deleted account-wide. It may contain "{prop:<field>}" placeholders,
	// filled from the desired properties.
	CreatePath string
	// ItemPath is used for Read, Update and Delete. Empty means the API has
	// no item endpoint: Read then filters the collection by id and Update and
	// Delete are unsupported unless ItemPathUpdate/ItemPathDelete are set.
	ItemPath string
	// ItemPathUpdate and ItemPathDelete override ItemPath when the API uses a
	// different route or version for those verbs (Vercel does this often).
	ItemPathUpdate string
	ItemPathDelete string

	// CreateMethod defaults to POST, UpdateMethod to PATCH.
	CreateMethod string
	UpdateMethod string

	// ListField is the key holding the array in CollectionPath's response;
	// empty means the response is a bare array.
	ListField string

	// ListPath overrides CollectionPath for List. Aliases are created under a
	// deployment but enumerated account-wide from /v4/aliases; when ListPath
	// has no {parent} placeholder, List makes a single flat pass instead of
	// walking parents.
	ListPath string

	// Unwrap is a response envelope key to descend into before reading the
	// object. Vercel wraps some payloads: creating a repository answers
	// {"repository": {...}} and creating a token answers {"token": {...}}.
	Unwrap string

	// IDField is the response field holding the resource id. Defaults to "id".
	IDField string
	// CreateIDField overrides IDField for the create response only. Vercel is
	// not always consistent: creating a DNS record answers with `uid` while
	// listing records returns `id`.
	CreateIDField string

	// Fields are the managed property names as they appear in the PKL schema.
	// Read returns only these (plus the id and parent), so unmanaged
	// server-side fields never surface as drift.
	Fields []string

	// Rename maps a PKL property name to its API field name, for the cases
	// where they cannot match. formae.Resource reserves `type`, `target`,
	// `label`, `group` and `stack`, and Vercel uses some of those names, so a
	// rename is the only way to model e.g. a DNS record's `type`.
	Rename map[string]string

	// CreateOnly are fields sent on create but never on update.
	CreateOnly []string

	// ReadViaCollection forces Read to GET CollectionPath and filter by id,
	// for APIs with no usable single-item GET.
	ReadViaCollection bool

	// Query is merged into every request for this resource (e.g.
	// {"decrypt": "true"}).
	Query map[string]string

	// Async declares that the API accepts a write before the resource is
	// usable, so Create and Update report InProgress and the agent polls
	// Status(). Nil — the default — means every write returns the final state
	// and Status() answers Success immediately. See async.go.
	Async *AsyncSpec

	// Operations the resource supports. Create/Read/List are assumed; set
	// NoUpdate or NoDelete when the API has no such verb.
	NoUpdate bool
	NoDelete bool
}

func (d Definition) createMethod() string {
	if d.CreateMethod != "" {
		return d.CreateMethod
	}
	return "POST"
}

func (d Definition) updateMethod() string {
	if d.UpdateMethod != "" {
		return d.UpdateMethod
	}
	return "PATCH"
}

func (d Definition) idField() string {
	if d.IDField != "" {
		return d.IDField
	}
	return "id"
}

func (d Definition) createIDField() string {
	if d.CreateIDField != "" {
		return d.CreateIDField
	}
	return d.idField()
}

func (d Definition) createPath() string {
	if d.CreatePath != "" {
		return d.CreatePath
	}
	return d.CollectionPath
}

func (d Definition) parentIDField() string {
	if d.ParentIDField != "" {
		return d.ParentIDField
	}
	return "id"
}

// listPath returns the path List should GET, and whether it is a flat pass
// (no parent walking).
func (d Definition) listPath() (p string, flat bool) {
	if d.ListPath != "" {
		return d.ListPath, !strings.Contains(d.ListPath, "{parent}")
	}
	return d.CollectionPath, d.Scope == ScopeAccount
}

func (d Definition) itemPathFor(op string) string {
	switch op {
	case "update":
		if d.ItemPathUpdate != "" {
			return d.ItemPathUpdate
		}
	case "delete":
		if d.ItemPathDelete != "" {
			return d.ItemPathDelete
		}
	}
	return d.ItemPath
}

// apiName returns the wire name for a PKL property.
func (d Definition) apiName(field string) string {
	if api, ok := d.Rename[field]; ok {
		return api
	}
	return field
}

// isCreateOnly reports whether a field must not be sent on update.
func (d Definition) isCreateOnly(field string) bool {
	for _, f := range d.CreateOnly {
		if f == field {
			return true
		}
	}
	return false
}
