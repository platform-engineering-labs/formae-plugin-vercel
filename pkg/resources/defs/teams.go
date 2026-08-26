// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package defs

import "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/rest"

// This group was opened for team membership and grew a second inhabitant: the
// certificate *upload* flow, which completes the custom-certificate story
// started by certificate() in defs.go.
//
// # Why VERCEL::Teams::Member is not here
//
// Every team-members endpoint carries the team in the path:
//
//	GET    /v3/teams/{teamId}/members        (list-team-members)
//	POST   /v2/teams/{teamId}/members        (invite-a-user)
//	PATCH  /v1/teams/{teamId}/members/{uid}  (update-a-team-member)
//	DELETE /v1/teams/{teamId}/members/{uid}  (remove-a-team-member)
//
// In this plugin the team is *not* resource state. It is target configuration
// (`vercel.Config.teamId` / `.slug`), held privately by the transport client and
// injected as a `?teamId=` query parameter on every request. It is deliberately
// kept out of native ids so a resource cannot appear to move between teams. rest.Definition's path templates can only interpolate
// `{parent}` and `{id}` from the native id and `{prop:…}` from the resource's own
// properties, and rest.New is handed only the client and the project scope — so
// there is no expression that puts the target's team into the path.
//
// The two ways out, neither of which is available from this file:
//
//  1. Engine + wiring change: teach rest.Definition a `{team}` placeholder fed
//     from the target config, which means changing rest.New's signature and the
//     factory in defs.go. Cleanest, and it keeps the team out of resource state.
//  2. Restate the team as a resource property (rest.ScopeParent with
//     ParentProperty "teamId", parents enumerated from GET /v2/teams). This
//     would duplicate target config into every resource, put the team into the
//     native id, and make discovery walk every team the token can see while the
//     client keeps stamping the *target's* team onto the query string. Members of teams the
//     target is not scoped to would be reported as discovered.
//
// Two further mismatches would remain even after (1) or (2), so this is not a
// one-line fix:
//
//   - invite-a-user documents its request body as a JSON *array* of
//     `{email, role, projects}` objects. rest.Resource.body() can only emit a
//     JSON object.
//   - PATCH and DELETE both answer `{"id": "<team id>"}` — the team's id, not
//     the member's — so the engine's Update would return an empty property
//     document.
//
// Until one of those is resolved, declaring the type would produce a definition
// that fails at apply time against a real account rather than here.
func teams() []rest.Definition {
	return []rest.Definition{
		uploadedCertificate(),
	}
}

func init() { AddGroup(teams()) }

// =============================================================================
// Certificates — the upload flow
// =============================================================================

// uploadedCertificate — PUT /v8/certs uploads a certificate you already hold,
// as three PEM blobs. A different endpoint from the issue flow that
// certificate() in defs.go declares (POST /v8/certs, which asks Vercel to obtain a
// certificate for a set of common names). Same collection, same item routes,
// two different verbs and two different request bodies — hence two types.
//
// https://vercel.com/docs/rest-api/certs/upload-a-cert
// https://vercel.com/docs/rest-api/certs/get-cert-by-id
// https://vercel.com/docs/rest-api/certs/remove-cert
// https://vercel.com/docs/rest-api/certs/get-certs
//
// Wire names are the reference page's own: `cert`, `ca`, `key` (all required)
// and the optional `skipValidation`.
//
// All three PEM fields are write-only. The documented 200 body of both PUT
// /v8/certs and GET /v8/certs/{id} is `{id, createdAt, expiresAt, autoRenew,
// cns}` — no key material comes back — and none of those read-only fields are
// declared here, so Read answers with the id alone and there is nothing for a
// sync to diff. `cns` in particular is derived by Vercel from the uploaded
// certificate rather than supplied, so declaring it would report drift on every
// sync.
//
// The API has no PATCH for certs: a new certificate is a new upload. Every
// field is therefore create-only and NoUpdate is set.
//
// Discovery is off (`discoverable = false` in schema/pkl/core/teams.pkl):
// GET /v8/certs returns issued and uploaded certificates in one undifferentiated
// list, and VERCEL::Certs::Certificate already discovers it. Leaving both on
// would report every certificate twice, under two types.
func uploadedCertificate() rest.Definition {
	return rest.Definition{
		Type:           "VERCEL::Certs::UploadedCertificate",
		Scope:          rest.ScopeAccount,
		CollectionPath: "/v8/certs",
		ItemPath:       "/v8/certs/{id}",
		CreateMethod:   "PUT",
		ListField:      "certs",
		Fields:         []string{"cert", "ca", "key", "skipValidation"},
		CreateOnly:     []string{"cert", "ca", "key", "skipValidation"},
		NoUpdate:       true,
	}
}
