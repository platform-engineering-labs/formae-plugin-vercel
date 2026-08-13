// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package rest

import (
	"fmt"
)

// Singleton and bag resources share one property: they have no id of their own.
// A project has exactly one rolling-release config and exactly one set of Edge
// Config items, addressed by a fixed path under the parent, so the parent id is
// the whole native id and there is no second segment to split off.

// hasOwnID reports whether the resource is keyed by an id of its own. Singletons
// and bags are not: they are keyed by their parent.
func (d Definition) hasOwnID() bool {
	return !d.Singleton && d.Bag == nil
}

// requireParent is the check a definition without an id of its own must pass:
// the parent is the only thing left to identify it by, so an account-scoped
// singleton or bag has no native id at all. Failing here rather than at the API
// keeps a definition bug out of the apply log.
func (d Definition) requireParent() error {
	if d.hasOwnID() {
		return nil
	}
	if d.Scope == ScopeAccount || d.ParentProperty == "" {
		return fmt.Errorf("%s: a resource with no id of its own must be project- or parent-scoped", d.Type)
	}
	return nil
}

// singletonPath is the fixed path the resource lives at. ItemPath and
// CollectionPath are usually the same URL for a singleton; declaring only one
// is enough.
func (d Definition) singletonPath() string {
	if d.ItemPath != "" {
		return d.ItemPath
	}
	return d.CollectionPath
}
