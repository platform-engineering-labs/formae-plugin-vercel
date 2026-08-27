// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package defs

import "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/rest"

// Project membership: who may see and change one project, and in what role.
//
//	GET    /v1/projects/{idOrName}/members
//	POST   /v1/projects/{idOrName}/members
//	DELETE /v1/projects/{idOrName}/members/{uid}
//
// Three details make this resource unlike the rest of the package:
//
//  1. The create response is documented as `{id}` — the *project's* id, not the
//     new member's. The member is addressed by `uid` everywhere else, so the
//     native id comes from the desired properties via CreateIDFromProperty
//     rather than from the response.
//  2. The POST accepts `uid` *or* `username` *or* `email` (a oneOf, with `role`
//     required alongside). Only `uid` is modelled: DELETE addresses the member
//     by uid, so a forma that identified someone by email would create a
//     membership the plugin could not then address. Resolving an email to a uid
//     would mean a lookup this resource has no endpoint for.
//  3. There is no PATCH. A role change is a replace, which for a membership is
//     the honest shape: remove the member, add them back in the new role.
//
// Read scans the collection because there is no per-member GET.
func projectMember() rest.Definition {
	return rest.Definition{
		Type:                 "VERCEL::Projects::Member",
		Scope:                rest.ScopeProject,
		ParentProperty:       "projectId",
		CollectionPath:       "/v1/projects/{parent}/members",
		ItemPathDelete:       "/v1/projects/{parent}/members/{id}",
		ListField:            "members",
		IDField:              "uid",
		CreateIDFromProperty: "uid",
		ReadViaCollection:    true,
		Fields:               []string{"uid", "role"},
		CreateOnly:           []string{"uid", "role"},
		NoUpdate:             true,
	}
}

func init() { AddGroup([]rest.Definition{projectMember()}) }
