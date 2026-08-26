// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package defs

import "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/rest"

// This file declares Vercel's Drains — the unified pipe that forwards logs,
// traces and audit-log events off the platform.
//
// Log drains, trace drains and audit-log drains look like three resources but
// are one endpoint: all three are `POST /v1/drains`, differing only in the
// `schemas` key they send (`log`, `trace`, `audit_log`) and in the `delivery`
// variant that goes with it. Modelling them as three formae types would give
// three types that create, read and — worse — *discover* the same objects, so
// every drain would appear three times. They are one type here:
// VERCEL::Drains::Drain.
//
// The older `POST /v1/log-drains` "Configurable Log Drain" is deliberately not
// declared; see the note at the bottom of this file.

func init() { AddGroup(drains()) }

func drains() []rest.Definition {
	return []rest.Definition{
		drain(),
	}
}

// drain — the Drains API.
//
//	POST   /v1/drains       https://vercel.com/docs/rest-api/drains/create-a-new-drain
//	GET    /v1/drains       https://vercel.com/docs/rest-api/drains/retrieve-a-list-of-all-drains
//	GET    /v1/drains/{id}  https://vercel.com/docs/rest-api/drains/find-a-drain-by-id
//	PATCH  /v1/drains/{id}  https://vercel.com/docs/rest-api/drains/update-an-existing-drain
//	DELETE /v1/drains/{id}  https://vercel.com/docs/rest-api/drains/delete-a-drain
//
// Every property name below is taken from those pages' own JSON schemas
// (machine-readable copy: https://openapi.vercel.sh/), not from any other
// provider's snake_case attribute names.
//
// Wire facts that shape this definition:
//
//   - The create body requires `name`, `projects` and `schemas`, and declares
//     `additionalProperties: false` — anything undeclared here would be a 400,
//     which is why Fields is exactly the documented set.
//   - The id is `id` on create *and* on read; drains do not switch to `uid` the
//     way DNS records do.
//   - `GET /v1/drains` answers `{"drains": [...]}`.
//   - Two fields are write-only. `projects` ("some" | "all") is accepted on
//     create but absent from every documented response, and the create-side
//     `filter` comes back as `filterV2` — a different key with a different
//     shape. Both are declared so they can be *sent*; the PKL schema marks them
//     `writeOnly` so the missing round-trip is not reported as drift. This is
//     the same shape as `copyEnvVarsFrom` on VERCEL::Projects::CustomEnvironment.
//
// Deliberately not declared, each because it would drift on every sync:
//
//   - `status` ("enabled" | "disabled"): accepted by PATCH and returned by GET,
//     but *not* accepted by POST, and the engine sends one body on create. A
//     drain is created enabled.
//   - `transforms`: accepted by POST and PATCH, absent from every documented
//     response.
//   - `filterV2`, `createdAt`, `updatedAt`, `ownerId`, `teamId`,
//     `firstErrorTimestamp`, `disabledAt`, `disabledBy`, `disabledReason`:
//     read-only server state.
//
// Create-only fields follow the official provider, which gives all three of its
// drain resources no update path at all — every attribute is `RequiresReplace`.
// The REST API is more capable than that (PATCH accepts name, projectIds,
// delivery and sampling), so only the fields that change what the drain *is*
// are marked create-only:
//
//   - `schemas` decides whether this is a log, trace or audit-log drain — the
//     one field that changes which kind of drain this is.
//   - `source` records whether the drain is self-served or owned by an
//     integration; it is provenance, not configuration.
//   - `projects` is never returned, so an in-place PATCH could only ever resend
//     what the forma already says. Replacing is the honest move.
func drain() rest.Definition {
	return rest.Definition{
		Type:           "VERCEL::Drains::Drain",
		Scope:          rest.ScopeAccount,
		CollectionPath: "/v1/drains",
		ItemPath:       "/v1/drains/{id}",
		ListField:      "drains",
		Fields: []string{
			"name",
			"projects",
			"projectIds",
			"schemas",
			"delivery",
			"sampling",
			"filter",
			"source",
		},
		CreateOnly: []string{"projects", "schemas", "source"},
	}
}

// Not declared: the legacy Configurable Log Drain.
//
// `POST /v1/log-drains`, `GET /v1/log-drains`, `GET|DELETE /v1/log-drains/{id}`
// still exist and are documented, but are marked *deprecated* and answer 410
// among their documented statuses. Two things rule them out:
//
//  1. They are a different thing from the drains above, not an older spelling
//     of them. Declaring /v1/log-drains alongside /v1/drains would give a
//     second, divergent log drain rather than one resource.
//  2. Their responses are undocumented where it matters. The create response is
//     published as a bare `{"type": "object"}` with no properties, and the read
//     response documents only `createdFrom`, `clientId`, `configurationId`,
//     `projectsMetadata` and integration presentation fields — no `id`, no
//     `url`, no `sources`. Create could not extract a native id without
//     guessing the key, so the endpoint stays unimplemented rather than
//     invented.
//
// Likewise `POST /v2/integrations/log-drains` is out of scope: it "must be
// called with an OAuth2 client (integration)", which this plugin's personal /
// team access token is not.
