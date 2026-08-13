# Vercel Resource Catalog

Research date: **2026-08-13**. All endpoints verified against
<https://vercel.com/docs/rest-api> (machine-readable spec: <https://openapi.vercel.sh/>).

## API basics

| Fact | Value |
|------|-------|
| Base URL | `https://api.vercel.com` |
| Auth | `Authorization: Bearer <token>` |
| Content type | `application/json` |
| Team scoping | `?teamId=<team_id>` **or** `?slug=<team_slug>` on every request |
| Versioning | Per-endpoint path prefix (`/v9/projects/...`, `/v10/projects/...`, `/v11/projects`) — versions differ per operation on the *same* resource |
| OpenAPI | <https://openapi.vercel.sh/> (single large document, all endpoints) |
| Official Go SDK | **None.** The only official SDK is TypeScript (`@vercel/sdk`). Community Go client `chronark/vercel-go` exists but is unmaintained and partial → we hand-roll a small HTTP client. |

### Error shape

Every non-2xx response is:

```json
{ "error": { "code": "forbidden", "message": "Not authorized" } }
```

Rate-limit responses add a `limit` object:

```json
{ "error": { "code": "rate_limited", "message": "The rate limit of 6 exceeded for '...'",
             "limit": { "remaining": 0, "reset": 1571432075, "total": 6 } } }
```

Note the quirks that drive our error mapping (see `docs/ARCHITECTURE.md`):

- Auth failure is reported as **403 `forbidden`**, not 401 — although 401 also appears in
  the per-endpoint docs, so both must be handled.
- Env var create returns **403** with a "already exists" message when the variable exists,
  and also documents 409. Both must map to `AlreadyExists`.
- `not_modified` is returned by domain PATCH when nothing changed.

### Rate limits

Rate limits are **per endpoint**, scoped to `owner` / `user` / `team` / `project`, and
returned as 429. The tightest limits relevant to our P1 set:

| Action | Limit | Window |
|--------|-------|--------|
| Project environment variable creation | 120 | 60 s |
| Project environment variable updates | 120 | 60 s |
| Project environment variable deletions | 60 | 60 s |
| Project environment variable retrieval | 500 | 60 s |
| Domains record creation / update | 50 | 60 s |
| Domains creation | 120 | 3600 s |
| Global Config writes (paid) | 100 | 3600 s |
| Global Config writes (free) | 250 | 30 days |

The binding constraint is env-var deletion at **1 req/s**. The plugin advertises a
namespace-wide limit of **5 req/s** — comfortably under every per-minute bucket while
still allowing bursts, since limits are per endpoint and not shared.

## Authentication

| Token type | How it is obtained | Scope |
|------------|--------------------|-------|
| Personal access token | <https://vercel.com/account/settings/tokens> | Can be created scoped to the personal account **or** to a specific team. A team-scoped token only reaches that team. |
| OAuth 2.0 token | Integrations flow | Not used by this plugin. |

A token scoped to the personal account still needs `?teamId=`/`?slug=` to act on team
resources. This plugin therefore always sends the team parameter when the target
configures one, and omits it otherwise (personal-account mode).

**Environment variables the plugin reads** (first non-empty wins):

1. `VERCEL_TOKEN` — the Vercel CLI convention.
2. `VERCEL_API_TOKEN` — the official Terraform provider convention
   (`vercel/terraform-provider-vercel`, provider argument `api_token`).

Team is configured on the forma target, not the environment, because a single agent may
manage several teams:

```pkl
config = new vercel.Config {
    teamId = "team_xxxxxxxx"   // or: slug = "my-team"
}
```

## Terraform provider research

| Provider | Status | Verdict |
|----------|--------|---------|
| [`vercel/terraform-provider-vercel`](https://github.com/vercel/terraform-provider-vercel) | **Official**, actively maintained, Plugin Framework, by far the most used | **Ground truth** for field names, create-only fields and update semantics |
| `chronark/vercel` | Community, stale | Ignored |
| `sigmadigitalza/vercel` | Community, stale | Ignored |

### Mapping to the official provider

| Formae resource | Terraform resource | Fidelity |
|-----------------|--------------------|----------|
| `VERCEL::Projects::Project` | `vercel_project` | 1:1 on the implemented subset. Our field names are the API's camelCase (`buildCommand`), Terraform's are snake_case (`build_command`). We implement a subset: name + build settings + framework + node version. Not implemented: `git_repository`, `environment`, protection blocks, `resource_config`. |
| `VERCEL::Projects::EnvironmentVariable` | `vercel_project_environment_variable` | 1:1 on `key`, `value`, `target`, `gitBranch`, `comment`, `customEnvironmentIds`, `projectId`. **Divergence:** Terraform models secrecy as a required boolean `sensitive`; we expose the API's own `type` enum (`plain`/`encrypted`/`sensitive`/`system`) instead, because the REST API round-trips `type` and we want Read to reflect it without translation. |

**Create-only fields — divergences noted:**

- `Project.name`: Terraform marks it **Requires replace**. The REST API *does* accept
  `name` in `PATCH /v9/projects/{idOrName}`, so an in-place rename is technically
  possible — but a rename changes every generated deployment URL. We follow Terraform and
  mark `name` `createOnly`. This is a deliberate divergence from raw API capability.
- `EnvironmentVariable.projectId`: create-only (the API has no move operation).
- `EnvironmentVariable.key`: **not** create-only — `PATCH .../env/{id}` accepts `key`, and
  Terraform does not mark it force-new either.

## Parity matrix — all 50 Terraform provider resources

The goal is 1:1 coverage of `vercel/terraform-provider-vercel`. Status counted
2026-08-13.

**Implemented: 14 / 50.**

Legend — **done**: implemented and unit-tested · **engine**: fits
`pkg/resources/rest`, needs its API request body confirmed and then declared ·
**custom**: needs hand-written Go, the engine's shape does not fit ·
**blocked**: cannot be modelled faithfully yet, reason given.

| # | Terraform resource | Formae type | Status |
|---|---|---|---|
| 1 | `vercel_project` | `VERCEL::Projects::Project` | **done** (hand-written) |
| 2 | `vercel_project_environment_variable` | `VERCEL::Projects::EnvironmentVariable` | **done** (hand-written) |
| 3 | `vercel_custom_environment` | `VERCEL::Projects::CustomEnvironment` | **done** (engine) |
| 4 | `vercel_project_domain` | `VERCEL::Projects::Domain` | **done** (engine) |
| 5 | `vercel_dns_record` | `VERCEL::DNS::Record` | **done** (engine) |
| 6 | `vercel_edge_config` | `VERCEL::GlobalConfig::Config` | **done** (engine) |
| 7 | `vercel_webhook` | `VERCEL::Webhooks::Webhook` | **done** (engine) |
| 8 | `vercel_network` | `VERCEL::Networking::Network` | **done** (engine) — create is async and not yet polled |
| 9 | `vercel_access_group` | `VERCEL::AccessGroups::AccessGroup` | **done** (engine) — update verb is POST, id is `accessGroupId` |
| 10 | `vercel_access_group_project` | `VERCEL::AccessGroups::ProjectAssignment` | **done** (engine) — keyed by projectId |
| 11 | `vercel_user_token` | `VERCEL::Auth::Token` | **done** (engine) — `bearerToken` deliberately not modelled; returned once only |
| 12 | `vercel_vcr_repository` | `VERCEL::VCR::Repository` | **done** (engine) — response unwrapped from `{repository:…}` |
| 13 | `vercel_custom_certificate` | `VERCEL::Certs::Certificate` | **partial** — the *issue* flow (`POST /v8/certs`, `cns`) is **done**; Terraform's resource wraps the separate `PUT /v8/certs` upload of three PEM blobs, whose body is not yet verified |
| 14 | `vercel_blob_store` | `VERCEL::Storage::BlobStore` | engine; no documented list endpoint, so discovery may be impossible |
| 15 | `vercel_alias` | `VERCEL::Deployments::Alias` | **done** (engine) — deployment is a create-time path input, not part of the native id |
| 16 | `vercel_project_route` | `VERCEL::Projects::Route` | engine; `route` is a nested block and `position` is create-time only |
| 17 | `vercel_project_members` | `VERCEL::Projects::Member` | engine (id = uid), `NoUpdate` |
| 18 | `vercel_feature_flag_definition` | `VERCEL::FeatureFlags::Flag` | engine, `CreateMethod: PUT` |
| 19 | `vercel_feature_flag_segment` | `VERCEL::FeatureFlags::Segment` | engine, `CreateMethod: PUT` |
| 20 | `vercel_feature_flag_sdk_key` | `VERCEL::FeatureFlags::SDKKey` | engine, `CreateMethod: PUT`, id = hashKey |
| 21 | `vercel_log_drain` | `VERCEL::Drains::LogDrain` | engine — legacy endpoint, absent from the current reference index; needs research |
| 22 | `vercel_trace_drain` | `VERCEL::Drains::TraceDrain` | engine — endpoint unconfirmed |
| 23 | `vercel_audit_log_drain` | `VERCEL::Drains::AuditLogDrain` | engine — endpoint unconfirmed |
| 24 | `vercel_oauth_app` | `VERCEL::OAuth::App` | engine — endpoint not in the public reference index; needs research |
| 25 | `vercel_oauth_app_client_secret` | `VERCEL::OAuth::ClientSecret` | engine — same |
| 26 | `vercel_project_crons` | `VERCEL::Projects::Crons` | engine — endpoint not in the reference index; needs research |
| 27 | `vercel_team_member` | `VERCEL::Teams::Member` | engine (`/v3/teams/{teamId}/members`); the team id comes from target config, not a property |
| 28 | `vercel_deployment_protection_exception` | `VERCEL::Projects::ProtectionException` | engine — endpoint unconfirmed |
| 29 | `vercel_integration_project_access` | `VERCEL::Integrations::ProjectAccess` | engine (`/v1/integrations/installations/…/connections`) |
| 30 | `vercel_edge_config_token` | `VERCEL::GlobalConfig::Token` | **custom** — delete is a bulk `DELETE .../tokens` with a request body, not an item delete |
| 31 | `vercel_edge_config_item` | `VERCEL::GlobalConfig::Items` | **custom** — one batch PATCH for the whole set; a bag resource like `SUPABASE::Functions::Secrets`. Writes are billed at $10/1K on Pro |
| 32 | `vercel_edge_config_schema` | `VERCEL::GlobalConfig::Schema` | **custom** — singleton child with no id |
| 33 | `vercel_project_environment_variables` | — | **custom** — the plural bulk form of #2; a bag over the same endpoint |
| 34 | `vercel_shared_environment_variable` | `VERCEL::Environment::SharedVariable` | **custom** — `/v1/env` create and update take arrays |
| 35 | `vercel_shared_environment_variable_project_link` | `VERCEL::Environment::SharedVariableLink` | **custom** — link/unlink verbs, not CRUD |
| 36 | `vercel_bulk_redirects` | `VERCEL::Projects::BulkRedirects` | **custom** — project-level bag with staging and promotion versions, no per-item id |
| 37 | `vercel_firewall_config` | `VERCEL::Security::FirewallConfig` | **custom** — singleton per project, `PUT`/`PATCH` only |
| 38 | `vercel_firewall_bypass` | `VERCEL::Security::FirewallBypass` | **custom** — POST/DELETE by body, no id |
| 39 | `vercel_attack_challenge_mode` | `VERCEL::Security::AttackChallengeMode` | **custom** — a toggle, not a resource |
| 40 | `vercel_team_config` | `VERCEL::Teams::Config` | **custom** — singleton, update-only |
| 41 | `vercel_project_deployment_retention` | `VERCEL::Projects::DeploymentRetention` | **custom** — a field of the project object, not its own endpoint |
| 42 | `vercel_project_protection_bypass` | `VERCEL::Projects::ProtectionBypass` | **custom** — `PATCH .../protection-bypass` with generate/revoke semantics |
| 43 | `vercel_project_rolling_release` | `VERCEL::Projects::RollingRelease` | **custom** — singleton child config |
| 44 | `vercel_microfrontend_group` | `VERCEL::Microfrontends::Group` | **custom** — create and update use different path shapes (`/v1/microfrontends/group` vs `/v1/teams/{teamId}/microfrontends/{groupId}`) |
| 45 | `vercel_microfrontend_group_membership` | `VERCEL::Microfrontends::Membership` | **custom** — `PATCH /v1/projects/{projectId}/microfrontends` |
| 46 | `vercel_vcr_repository_permission` | `VERCEL::VCR::Permission` | **custom** — delete takes a body, no item path |
| 47 | `vercel_feature_flag_config` | `VERCEL::FeatureFlags::Settings` | **custom** — singleton per project (`.../feature-flags/settings`) |
| 48 | `vercel_access_group_member` | `VERCEL::AccessGroups::Member` | **custom** — membership is edited through the access group's own update body |
| 49 | `vercel_blob_project_connection` | `VERCEL::Storage::BlobProjectConnection` | **custom** — connect/disconnect verbs |
| 50 | `vercel_deployment` | `VERCEL::Deployments::Deployment` | **blocked** — asynchronous, needs deployment files uploaded via `POST /v2/files` and `readyState` polled. Requires async `Status()` and a file-upload path. Also the only resource here that always costs build minutes |
| 51 | `vercel_blob_object` | `VERCEL::Storage::BlobObject` | **blocked** — not served by `api.vercel.com`; blob objects go to the Blob storage endpoint with a store token |

51 rows: `vercel_blob_object` is listed separately because it is not part of the
REST API this plugin talks to.

### Remaining work, by kind

- **15 engine-fit** left (#14, #16–#29): each needs its request body confirmed against its
  own API reference page, then a `rest.Definition` and a PKL class. Seven of
  them (#21–#26, #28) point at endpoints that are *not* in the public REST
  reference index and need research before anything can be declared.
- **19 custom** (#30–#49): each needs hand-written Go, because the API shape is
  a bag, a singleton, a toggle, or a verb pair rather than CRUD on an id.
- **2 blocked** (#50, #51): need async `Status()` polling and file upload.

### Engine capabilities still missing

Closing the list above also needs these, none of which exist yet:

1. **Async create with `Status()` polling** — for `Network` (already shipped and
   currently reporting success too early) and `Deployment`.
2. **Singleton child resources** — no id; the native id is just the parent.
3. **Bag resources** — one batch write for a whole keyed set.
4. **Verb-pair resources** — link/unlink, connect/disconnect, generate/revoke.
5. **Bulk delete taking a request body.**


## Resource catalog

Naming: `VERCEL::Category::Resource`. Categories follow the REST API's own tag grouping.

### Projects

| Resource Type | API Endpoints | CRUD | Priority |
|---------------|---------------|------|----------|
| `VERCEL::Projects::Project` | `POST /v11/projects`, `GET /v9/projects/{idOrName}`, `PATCH /v9/projects/{idOrName}`, `DELETE /v9/projects/{idOrName}`, `GET /v10/projects` | C R U D L | **P1 — implemented** |
| `VERCEL::Projects::EnvironmentVariable` | `POST /v10/projects/{idOrName}/env`, `GET /v10/projects/{idOrName}/env?decrypt=true`, `PATCH /v9/projects/{idOrName}/env/{id}`, `DELETE /v9/projects/{idOrName}/env/{id}` | C R U D L | **P1 — implemented** |
| `VERCEL::Projects::Domain` | `POST /v10/projects/{idOrName}/domains`, `GET/PATCH/DELETE /v9/projects/{idOrName}/domains/{domain}` | C R U D L | P2 |
| `VERCEL::Projects::CustomEnvironment` | `POST/GET /v9/projects/{idOrName}/custom-environments`, `GET/PATCH/DELETE .../{environmentSlugOrId}` | C R U D L | P2 |
| `VERCEL::Projects::Member` | `GET/POST /v1/projects/{idOrName}/members`, `DELETE .../{uid}` | C R D L | P3 |
| `VERCEL::Projects::Route` | `GET/POST/PUT/DELETE /v1/projects/{projectId}/routes` | C R U D L | P3 |

### DNS & Domains

| Resource Type | API Endpoints | CRUD | Priority |
|---------------|---------------|------|----------|
| `VERCEL::DNS::Record` | `POST /v2/domains/{domain}/records`, `GET /v5/domains/{domain}/records`, `PATCH /v1/domains/records/{recordId}`, `DELETE /v2/domains/{domain}/records/{recordId}` | C R U D L | P2 |
| `VERCEL::Domains::Domain` | `POST /v7/domains`, `GET /v5/domains/{domain}`, `PATCH /v3/domains/{domain}`, `DELETE /v6/domains/{domain}`, `GET /v5/domains` | C R U D L | P2 |

Not P1: both require an apex domain the test account actually controls, and adding a
custom domain on a Hobby account returns `custom_domain_needs_upgrade`. There is also no
single-record GET (`GET /domains/records/{recordId}` exists but is untagged/undocumented),
so Read has to scan the record list.

### Global Config (formerly Edge Config)

| Resource Type | API Endpoints | CRUD | Priority |
|---------------|---------------|------|----------|
| `VERCEL::GlobalConfig::Config` | `POST /v1/global-config`, `GET /v1/global-config/{edgeConfigId}`, `PUT /v1/global-config/{edgeConfigId}`, `DELETE /v1/global-config/{edgeConfigId}`, `GET /v1/global-config` | C R U D L | P2 |
| `VERCEL::GlobalConfig::Items` | `GET /v1/global-config/{edgeConfigId}/items`, `PATCH .../items` (batch) | R U | P2 |
| `VERCEL::GlobalConfig::Token` | `POST /v1/global-config/{edgeConfigId}/token`, `GET .../tokens`, `DELETE .../tokens` | C R D L | P3 |

Note: the API path was renamed `edge-config` → `global-config`, but the path parameter is
still `edgeConfigId`. Items have no per-item write endpoint — the whole item set is one
batch PATCH, so it would be modelled as a single bag resource (same shape as
`SUPABASE::Functions::Secrets`). Writes are metered and billed ($10 per 1K writes on Pro),
so this is *not* a good conformance-test candidate.

### Other categories (P3, catalogued for completeness)

| Resource Type | API Endpoints | CRUD |
|---------------|---------------|------|
| `VERCEL::Deployments::Deployment` | `POST /v13/deployments`, `GET/DELETE /v13/deployments/{idOrUrl}`, `GET /v7/deployments` | C R D L |
| `VERCEL::Deployments::Alias` | `POST /v2/deployments/{id}/aliases`, `GET /v4/aliases`, `DELETE /v2/aliases/{aliasId}` | C R D L |
| `VERCEL::Webhooks::Webhook` | `POST/GET /v1/webhooks`, `GET/DELETE /v1/webhooks/{id}` | C R D L |
| `VERCEL::Drains::Drain` | `POST/GET /v1/drains`, `GET/PATCH/DELETE /v1/drains/{id}` | C R U D L |
| `VERCEL::Storage::BlobStore` | `POST /storage/stores/blob`, `GET /storage/stores/{id}`, `DELETE /storage/stores/blob/{id}` | C R D |
| `VERCEL::Security::FirewallConfig` | `GET/PUT/PATCH /v1/security/firewall/config` | R U |
| `VERCEL::Teams::Team` | `POST /v1/teams`, `GET/PATCH /v2/teams/{teamId}`, `DELETE /v1/teams/{teamId}`, `GET /v2/teams` | C R U D L |
| `VERCEL::Certs::Cert` | `POST/PUT /v8/certs`, `GET/DELETE /v8/certs/{id}` | C R D L |

Deployments are deliberately P3: creating one costs build minutes, is asynchronous, and
is better expressed by a CI pipeline than by declarative IaC.

## P1 selection rationale

`VERCEL::Projects::Project` and `VERCEL::Projects::EnvironmentVariable`:

- **Free.** Creating an empty project (no Git repo, no deployment) bills nothing and burns
  no build minutes. Hobby accounts allow 200 projects.
- **Synchronous.** Both create/update/delete return the final state in one round-trip —
  no async `Status()` polling needed.
- **Fast.** Sub-second per operation, so repeated conformance runs are cheap.
- **Parent/child pair.** Exercises composite native IDs and cross-resource references
  (`envVar.projectId = project.res.id`) in the same slice.
- **Covers both update paths.** `Project` has a real create-only field (`name`) for the
  replace test; `EnvironmentVariable` exercises in-place PATCH.

### Legend

- C: Create · R: Read · U: Update · D: Delete · L: List
- P1: implemented now · P2: next · P3: future / low value

## Known field-level facts (P1)

### `VERCEL::Projects::Project`

`POST /v11/projects` — only `name` is required (max 100 chars). Implemented fields:

| Field | API field | Create-only | Notes |
|-------|-----------|-------------|-------|
| `name` | `name` | **yes** | Lowercase alphanumeric + hyphens, ≤100 chars |
| `framework` | `framework` | no | Nullable enum: `nextjs`, `vite`, `astro`, `svelte`, … (~75 values) |
| `buildCommand` | `buildCommand` | no | Nullable, ≤256 chars, auto-detected when null |
| `devCommand` | `devCommand` | no | Nullable, ≤256 chars |
| `installCommand` | `installCommand` | no | Nullable, ≤256 chars |
| `outputDirectory` | `outputDirectory` | no | Nullable, ≤256 chars |
| `rootDirectory` | `rootDirectory` | no | Nullable, ≤256 chars |
| `nodeVersion` | `nodeVersion` | no | Enum `18.x`/`20.x`/`22.x`/`24.x`/… Absent from the documented `POST /v11/projects` body but present on the project object and settable via PATCH, so the plugin only sends it on update. |
| `id` (read-only) | `id` | — | `prj_…` |
| `accountId` (read-only) | `accountId` | — | |

`GET /v10/projects` returns either a bare array or `{projects: [...], pagination: {count, next}}`
depending on API version negotiation; `pagination.next` is a continuation token/timestamp
passed back as `?from=`. The plugin handles both response shapes.

### `VERCEL::Projects::EnvironmentVariable`

`POST /v10/projects/{idOrName}/env` accepts a single object **or** an array. Required:
`key`, `value`, `type`; plus at least one of `target` / `customEnvironmentIds`.

| Field | API field | Create-only | Notes |
|-------|-----------|-------------|-------|
| `projectId` | (path) | **yes** | Project id or name |
| `key` | `key` | no | Letters, digits, `_` only; ≤256 chars |
| `value` | `value` | no | ≤65536 chars |
| `variableType` | `type` | no | `plain` \| `encrypted` \| `sensitive` \| `system`. Renamed because `formae.Resource` reserves `type`. |
| `targets` | `target` | no | Array of `production` / `preview` / `development`. Renamed because `formae.Resource` reserves `target` for the deployment target. |
| `gitBranch` | `gitBranch` | no | Only valid with `target=preview` |
| `comment` | `comment` | no | ≤500 chars |
| `customEnvironmentIds` | `customEnvironmentIds` | no | Alternative to `target` |
| `id` (read-only) | `id` | — | |

Read gotchas:

- There is no single-env-var GET that returns the plain value for arbitrary types;
  the plugin lists `GET /v10/projects/{id}/env?decrypt=true` and filters by id.
- `type=sensitive` values are **never** returned by the API. Such a variable will read
  back with an empty/redacted value and therefore drift on every sync. Use `plain` or
  `encrypted` for formae-managed values, or accept the drift knowingly.
- The create response is `{created: {...}|[...], failed: [...]}` — a 201 with a non-empty
  `failed` array is still a failure and is treated as one.
