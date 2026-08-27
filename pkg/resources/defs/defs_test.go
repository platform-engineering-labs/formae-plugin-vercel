// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package defs

import (
	"strings"
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/rest"
)

func TestAllRegistered(t *testing.T) {
	for _, def := range All() {
		if !registry.Has(def.Type) {
			t.Errorf("%s not registered", def.Type)
		}
	}
}

func TestOperationsMatchCapabilities(t *testing.T) {
	for _, def := range All() {
		ops := registry.GetOperations(def.Type)
		want := 5
		if def.NoUpdate {
			want--
		}
		if def.NoDelete {
			want--
		}
		if len(ops) != want {
			t.Errorf("%s: %d operations, want %d", def.Type, len(ops), want)
		}
	}
}

// A definition that cannot be executed is worse than a missing one: it fails
// at apply time against a real account instead of here.
func TestDefinitionsAreWellFormed(t *testing.T) {
	for _, def := range All() {
		if strings.Count(def.Type, "::") != 2 {
			t.Errorf("%s: type must be NAMESPACE::Category::Resource", def.Type)
		}
		if !strings.HasPrefix(def.Type, "VERCEL::") {
			t.Errorf("%s: wrong namespace", def.Type)
		}
		if def.CollectionPath == "" {
			t.Errorf("%s: no CollectionPath", def.Type)
		}
		if len(def.Fields) == 0 {
			t.Errorf("%s: no Fields", def.Type)
		}

		// Read needs either an item endpoint or an explicit collection scan.
		if def.ItemPath == "" && !def.ReadViaCollection {
			t.Errorf("%s: no ItemPath and not ReadViaCollection — Read cannot work", def.Type)
		}
		// Delete needs a path unless the resource declares it has none.
		if !def.NoDelete && def.ItemPath == "" && def.ItemPathDelete == "" {
			t.Errorf("%s: deletable but no item or delete path", def.Type)
		}
		// Update needs a path unless the resource declares it has none.
		if !def.NoUpdate && def.ItemPath == "" && def.ItemPathUpdate == "" {
			t.Errorf("%s: updatable but no item or update path", def.Type)
		}
		// A resource whose every field is create-only cannot be updated.
		if !def.NoUpdate && allCreateOnly(def) {
			t.Errorf("%s: every field is createOnly but NoUpdate is not set", def.Type)
		}

		switch def.Scope {
		case rest.ScopeAccount:
			if def.ParentProperty != "" {
				t.Errorf("%s: account-scoped but has ParentProperty", def.Type)
			}
			if strings.Contains(def.CollectionPath, "{parent}") {
				t.Errorf("%s: account-scoped but path templates a parent", def.Type)
			}
		case rest.ScopeProject, rest.ScopeParent:
			if def.ParentProperty == "" {
				t.Errorf("%s: scoped but no ParentProperty", def.Type)
			}
			// The parent normally comes from the path; a resource whose
			// collection is selected by a query parameter instead must say so
			// in ListQuery, or discovery lists the wrong thing — or nothing.
			if !strings.Contains(def.CollectionPath, "{parent}") && !queryTemplatesParent(def.ListQuery) {
				t.Errorf("%s: scoped but neither CollectionPath nor ListQuery carries the parent", def.Type)
			}
		}
		if def.Scope == rest.ScopeParent && def.ParentListPath == "" {
			t.Errorf("%s: parent-scoped but no ParentListPath — discovery cannot enumerate", def.Type)
		}

		// Every createOnly field must actually be a declared field, or the
		// exclusion silently does nothing.
		for _, co := range def.CreateOnly {
			if !contains(def.Fields, co) {
				t.Errorf("%s: createOnly %q is not in Fields", def.Type, co)
			}
		}
	}
}

func TestNoDuplicateTypes(t *testing.T) {
	seen := map[string]bool{}
	for _, def := range All() {
		if seen[def.Type] {
			t.Errorf("duplicate definition for %s", def.Type)
		}
		seen[def.Type] = true
	}
}

func allCreateOnly(def rest.Definition) bool {
	for _, f := range def.Fields {
		if !contains(def.CreateOnly, f) {
			return false
		}
	}
	return true
}

func queryTemplatesParent(query map[string]string) bool {
	for _, v := range query {
		if strings.Contains(v, "{parent}") {
			return true
		}
	}
	return false
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// A renamed field must still be a declared field, and must not collide with a
// name formae.Resource already reserves.
func TestRenamesAreConsistent(t *testing.T) {
	reserved := map[string]bool{"type": true, "target": true, "label": true, "group": true, "stack": true}
	for _, def := range All() {
		for pklName, apiName := range def.Rename {
			if !contains(def.Fields, pklName) {
				t.Errorf("%s: rename source %q is not in Fields", def.Type, pklName)
			}
			if reserved[pklName] {
				t.Errorf("%s: %q is reserved by formae.Resource and cannot be a field name", def.Type, pklName)
			}
			if apiName == "" {
				t.Errorf("%s: rename of %q has an empty target", def.Type, pklName)
			}
		}
		for _, f := range def.Fields {
			if reserved[f] && def.Rename[f] == "" {
				t.Errorf("%s: field %q is reserved by formae.Resource; declare it under another name and Rename it", def.Type, f)
			}
		}
	}
}

// A parent-scoped resource whose paths take a *name* must say so. Vercel's
// domain objects carry both an opaque `id` and a `name`, and every DNS record
// path takes the name — so defaulting ParentIDField to "id" made discovery walk
// paths built from `Qmb4E6…` and quietly find nothing, while Read stayed healthy
// because it takes the domain out of the native id.
//
// The general lesson, which is why this is a test and not a comment: a resource
// can be perfectly readable and wholly undiscoverable, and only the discovery
// phase notices.
func TestDNSRecord_EnumeratesParentsByName(t *testing.T) {
	var def *rest.Definition
	for _, d := range All() {
		if d.Type == "VERCEL::DNS::Record" {
			c := d
			def = &c
		}
	}
	if def == nil {
		t.Skip("VERCEL::DNS::Record is not declared on this branch")
	}
	if def.ParentIDField != "name" {
		t.Errorf("ParentIDField = %q, want name: record paths take the domain name, not its id", def.ParentIDField)
	}
	if !strings.Contains(def.CollectionPath, "{parent}") {
		t.Errorf("CollectionPath = %q, expected a {parent} segment", def.CollectionPath)
	}
}
