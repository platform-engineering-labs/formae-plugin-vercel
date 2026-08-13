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
//
// Beyond plain CRUD, a Definition can also declare the shapes Vercel keeps
// reaching for. Each is inert unless declared, so a definition pays only for
// what it uses:
//
//   - Async (async.go): the write is accepted before the resource exists, so
//     Create and Update report InProgress and Status() polls.
//   - Singleton (singleton.go): no id of its own, a fixed path under a parent.
//   - Bag (bag.go): a whole keyed set written in one batch call.
//   - CreateIDFromProperty, DeleteMethod, DeleteBody: association resources
//     whose lifecycle is a verb pair (link/unlink, connect/disconnect) and
//     deletes that name what to remove in a request body.
//   - Wrap / WrapExclude: the request body nests the managed fields under a
//     key while the response returns them flat.
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

	// CreateMethod defaults to POST, UpdateMethod to PATCH, DeleteMethod to
	// DELETE. A DeleteMethod is needed for association resources whose
	// lifecycle is a verb pair rather than CRUD: connect/disconnect and
	// link/unlink are usually two POSTs.
	CreateMethod string
	UpdateMethod string
	DeleteMethod string

	// DeleteBody is a request-body template sent with the delete call, for the
	// APIs that name what to remove in the body instead of the path — Global
	// Config tokens are removed by DELETE /v1/global-config/{id}/tokens with
	// {"tokens": [...]}, and project routes by a DELETE to the collection with
	// {"routeIds": [...]}. The strings "{id}" and "{parent}" are substituted
	// anywhere they appear, including inside nested objects and arrays. Nil —
	// the default — sends no body at all.
	//
	// The path is still ItemPathDelete (or ItemPath); a bulk delete simply
	// points it at the collection and leaves out the "{id}" placeholder.
	DeleteBody map[string]any

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

	// Wrap is the mirror of Unwrap for the request body: the declared fields
	// are nested under this key on create and update. A project route must be
	// written as {"route": {name, …}, "position": {…}} but answers — and lists
	// — with those same fields flat, so the two directions genuinely disagree
	// on shape and each needs its own knob. Empty means the body is flat.
	Wrap string
	// WrapExclude are fields that stay at the top level of the request body,
	// beside the wrapper rather than inside it — a route's `position`, which is
	// a placement instruction rather than part of the route. Ignored when Wrap
	// is empty. A wrapper left with no fields at all is omitted: an empty
	// object is a different request from an absent one.
	WrapExclude []string

	// IDField is the response field holding the resource id. Defaults to "id".
	IDField string
	// CreateIDField overrides IDField for the create response only. Vercel is
	// not always consistent: creating a DNS record answers with `uid` while
	// listing records returns `id`.
	CreateIDField string

	// CreateIDFromProperty takes the resource id from a declared property of
	// the desired document instead of from the create response, for APIs whose
	// response carries no usable id: POST /v1/projects/{id}/members "responds
	// with the project ID on success" — the parent's id, not the member's —
	// and association endpoints (link, connect) often answer with nothing at
	// all. The property must be a non-empty string, checked before the write so
	// nothing is created that cannot then be addressed.
	CreateIDFromProperty string

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

	// ItemQuery adds query parameters to every request against the item path —
	// Read, Update and Delete — templated with {id} and {parent}. Some
	// endpoints need the parent to identify the resource even though it is not
	// in the path: both GET and DELETE on a container registry repository
	// require ?projectId=… and answer "400 missing required property
	// projectId" without it.
	ItemQuery map[string]string

	// ParentInBody sends the parent property in the create body as well as
	// using it in the native id. Normally the parent is a path segment and is
	// excluded from the body; a few endpoints take it as a body field instead.
	ParentInBody bool

	// ParentFromField makes List a single flat pass, taking each item's parent
	// from this response field rather than walking a parent collection. For
	// resources listed account-wide but addressed per-parent — a VCR repository
	// listing returns every repository with its projectId attached.
	ParentFromField string

	// Singleton declares a child resource with no id of its own, living at a
	// fixed path under its parent — a project's rolling-release config at
	// /v1/projects/{parent}/rolling-release/config, an Edge Config's schema.
	// The native id is the parent id alone, Create and Update are usually the
	// same call (set CreateMethod to PATCH or PUT), and List enumerates parents
	// rather than items. A singleton must be project- or parent-scoped: with no
	// parent there is nothing left to key it by. See singleton.go.
	Singleton bool

	// Bag declares a resource that is a whole keyed set written in one batch
	// call — Global Config items, where the API has no per-item endpoint at
	// all. Like a singleton it has no id of its own, so the native id is the
	// parent id. Nil means the resource is an ordinary item. See bag.go.
	Bag *BagSpec

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

func (d Definition) deleteMethod() string {
	if d.DeleteMethod != "" {
		return d.DeleteMethod
	}
	return "DELETE"
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
	if !d.hasOwnID() {
		return d.singletonPath()
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
	return containsString(d.CreateOnly, field)
}

// isWrapExcluded reports whether a field stays outside the request-body
// wrapper.
func (d Definition) isWrapExcluded(field string) bool {
	return containsString(d.WrapExclude, field)
}
