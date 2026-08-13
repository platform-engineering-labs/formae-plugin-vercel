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
			if !strings.Contains(def.CollectionPath, "{parent}") {
				t.Errorf("%s: scoped but CollectionPath has no {parent}", def.Type)
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

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
