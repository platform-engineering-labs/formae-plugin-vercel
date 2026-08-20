// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package defs

import "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/rest"

// Vercel's feature-flag endpoints: flag definitions, segments and SDK keys.
// Three things set them apart from every other resource in this package:
//
//   - Create is PUT, not POST. None of the three collections has a POST verb.
//   - Every collection response is enveloped as {"data": [...]}, and the flag
//     and segment collections add a {"pagination": {"next": …}} cursor the
//     engine does not follow yet, so discovery sees the first page only.
//   - They are all project-scoped, so native ids are "{projectId}/{id}".
//
// Per-project feature-flag *settings* are deliberately absent: they live at
// GET/PATCH /v1/projects/{projectIdOrName}/feature-flags/settings, a per-project
// singleton with no id of its own. The engine can express that shape now
// (Singleton), but it is not declared yet.
func featureFlags() []rest.Definition {
	return []rest.Definition{
		flag(),
		featureFlagSettings(),
		flagSegment(),
		flagSDKKey(),
	}
}

func init() { AddGroup(featureFlags()) }

// flag — PUT /v1/projects/{projectIdOrName}/feature-flags/flags, item under
// .../flags/{flagIdOrSlug} for GET, PATCH and DELETE.
//
// https://vercel.com/docs/rest-api/feature-flags/create-a-flag
// https://vercel.com/docs/rest-api/feature-flags/get-a-flag
// https://vercel.com/docs/rest-api/feature-flags/update-a-flag
// https://vercel.com/docs/rest-api/feature-flags/delete-a-flag
// https://vercel.com/docs/rest-api/feature-flags/list-flags
//
// `slug` and `kind` are create-only because the documented PATCH body accepts
// neither: they identify the flag and its value shape.
//
// Three fields the API returns are deliberately unmanaged. `seed` and
// `revision` are server-generated on every write, and `experiment` belongs to
// the experimentation product rather than to the flag's declared state;
// managing any of them would report drift on every sync.
//
// Note the collection is also served by GET /v2/…/flags with a richer cursor;
// the v1 collection is used here because it is the version the item endpoints
// live under.
func flag() rest.Definition {
	return rest.Definition{
		Type:           "VERCEL::FeatureFlags::Flag",
		Scope:          rest.ScopeProject,
		ParentProperty: "projectId",
		CollectionPath: "/v1/projects/{parent}/feature-flags/flags",
		ItemPath:       "/v1/projects/{parent}/feature-flags/flags/{id}",
		CreateMethod:   "PUT",
		BodyHook:       ensureEnvironmentRules,
		ReadHook:       dropEnvironmentRevisions,
		ListField:      "data",
		// Flags and segments page with `?cursor=`; without this the engine
		// saw the first page only.
		PageParam: "cursor",
		Fields: []string{
			"slug", "kind", "variants", "environments",
			"description", "state", "maintainerIds", "permanent", "tags",
		},
		CreateOnly: []string{"slug", "kind"},
	}
}

// flagSegment — PUT /v1/projects/{projectIdOrName}/feature-flags/segments, item
// under .../segments/{segmentIdOrSlug} for GET, PATCH and DELETE.
//
// https://vercel.com/docs/rest-api/feature-flags/create-a-segment
// https://vercel.com/docs/rest-api/feature-flags/get-a-segment
// https://vercel.com/docs/rest-api/feature-flags/update-a-segment
// https://vercel.com/docs/rest-api/feature-flags/delete-a-segment
// https://vercel.com/docs/rest-api/feature-flags/list-segments
//
// The API calls the human-readable name `label`, which formae.Resource
// reserves, so the PKL property is `segmentLabel` and is renamed on the wire.
//
// `slug` is create-only: the documented PATCH body does not accept it.
// `createdBy` is accepted on create but is attribution rather than declared
// state, and `usedByFlags`/`usedBySegments` are read-only back-references, so
// none of them are managed.
func flagSegment() rest.Definition {
	return rest.Definition{
		Type:           "VERCEL::FeatureFlags::Segment",
		Scope:          rest.ScopeProject,
		ParentProperty: "projectId",
		CollectionPath: "/v1/projects/{parent}/feature-flags/segments",
		ItemPath:       "/v1/projects/{parent}/feature-flags/segments/{id}",
		CreateMethod:   "PUT",
		ListField:      "data",
		// Flags and segments page with `?cursor=`; without this the engine
		// saw the first page only.
		PageParam:  "cursor",
		Fields:     []string{"slug", "segmentLabel", "description", "data", "hint"},
		Rename:     map[string]string{"segmentLabel": "label"},
		CreateOnly: []string{"slug"},
	}
}

// flagSDKKey — PUT /v1/projects/{projectIdOrName}/feature-flags/sdk-keys,
// DELETE .../sdk-keys/{hashKey}. There is no single-key GET and no PATCH, so
// Read scans the collection and every field is create-only.
//
// https://vercel.com/docs/rest-api/feature-flags/create-an-sdk-key
// https://vercel.com/docs/rest-api/feature-flags/get-all-sdk-keys
// https://vercel.com/docs/rest-api/feature-flags/delete-an-sdk-key
//
// The id is `hashKey`, not `id`. `label` is renamed for the same reason as on
// the segment.
//
// `sdkKeyType` is the one place where this API contradicts itself: the create
// body requires `sdkKeyType`, while both the create response and the collection
// answer with `type`. A Rename would have to pick one, and picking `type` would
// break create outright — so the field is sent under its create-body name and
// simply never comes back from a read. It is create-only, which keeps the
// mismatch out of the update path, and `type` could not have been a PKL
// property name anyway.
//
// The cleartext secrets (`keyValue`, `tokenValue`, `connectionString`) are
// disclosed exactly once, at creation, and are deliberately not modelled — the
// same call formae makes for VERCEL::Auth::Token's `bearerToken`.
func flagSDKKey() rest.Definition {
	return rest.Definition{
		Type:              "VERCEL::FeatureFlags::SDKKey",
		Scope:             rest.ScopeProject,
		ParentProperty:    "projectId",
		CollectionPath:    "/v1/projects/{parent}/feature-flags/sdk-keys",
		ItemPathDelete:    "/v1/projects/{parent}/feature-flags/sdk-keys/{id}",
		CreateMethod:      "PUT",
		ListField:         "data",
		IDField:           "hashKey",
		ReadViaCollection: true,
		Fields:            []string{"sdkKeyType", "environment", "keyLabel"},
		Rename:            map[string]string{"keyLabel": "label"},
		CreateOnly:        []string{"sdkKeyType", "environment", "keyLabel"},
		NoUpdate:          true,
	}
}

// ensureEnvironmentRules puts back the `rules` key formae elides when a forma
// declares an empty rule list. The endpoint requires the key to be present in
// every environment even when there are no rules:
//
//	400 Invalid request: `environments.production` missing required property `rules`
//
// Recorded off the live API on 2026-08-20. This was the conformance failure that
// stayed undiagnosed: the harness reports a failed apply only as "terminal
// state: Failed", and every unit-test payload includes `rules` by hand.
func ensureEnvironmentRules(body map[string]any) {
	envs, ok := body["environments"].(map[string]any)
	if !ok {
		return
	}
	for name, raw := range envs {
		env, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, present := env["rules"]; !present {
			env["rules"] = []any{}
		}
		envs[name] = env
	}
}

// dropEnvironmentRevisions removes the per-environment `revision` Vercel stamps
// on create and bumps on every change. No forma can usefully set it, and
// reporting it makes every Verify, Extract and Sync fail with "not expected and
// not a provider default" — hasProviderDefault does not reach fields nested
// inside a collection.
func dropEnvironmentRevisions(properties map[string]any) {
	envs, ok := properties["environments"].(map[string]any)
	if !ok {
		return
	}
	for name, raw := range envs {
		env, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		delete(env, "revision")
		envs[name] = env
	}
}

// featureFlagSettings — the per-project feature-flag configuration.
//
//	GET   /v1/projects/{projectIdOrName}/feature-flags/settings
//	PATCH /v1/projects/{projectIdOrName}/feature-flags/settings
//
// A singleton: one per project, no id of its own, so the native id is the
// project id alone. There is no POST — PATCH serves as create and update. This
// is the first Singleton in the plugin; the capability existed but nothing had
// declared it.
//
// There is no DELETE either, and declaring NoDelete turned out to be the wrong
// answer. A type that cannot be deleted makes every stack containing it
// impossible to destroy: formae asks the plugin to delete the resource, the
// plugin refuses, and the destroy fails. The conformance fixture showed this as
// a failed Destroy after Create through Update had all passed.
//
// Deleting these settings therefore means resetting them to the inert state the
// project had before anyone touched them: PATCH enabled=false. That is a real
// operation with a real effect, not a no-op reported as success, and it leaves
// the project exactly as a project that never had settings.
//
// Only `enabled` is modelled. The endpoint also accepts `entities` (custom
// evaluation entities and their attributes, three levels of nesting) and
// `environments`; neither is declared yet, so reads drop them rather than
// reporting state no forma asked for.
func featureFlagSettings() rest.Definition {
	return rest.Definition{
		Type:           "VERCEL::FeatureFlags::Settings",
		Scope:          rest.ScopeProject,
		ParentProperty: "projectId",
		// One URL serves both roles for a singleton; the invariants in
		// defs_test.go want the collection path declared regardless.
		CollectionPath: "/v1/projects/{parent}/feature-flags/settings",
		ItemPath:       "/v1/projects/{parent}/feature-flags/settings",
		Singleton:      true,
		CreateMethod:   "PATCH",
		DeleteMethod:   "PATCH",
		DeleteBody:     map[string]any{"enabled": false},
		Fields:         []string{"enabled"},
	}
}
