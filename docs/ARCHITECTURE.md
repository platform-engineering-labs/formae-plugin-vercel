# Plugin Architecture

Companion to [`RESOURCES.md`](RESOURCES.md), which holds the API research this design
rests on.

## Transport layer

There is no official Vercel Go SDK, and the community one is stale — so the plugin ships
a ~150-line HTTP client over `net/http` in `pkg/transport/vercel`.

```go
type Client struct { baseURL, token string; http *http.Client; userAgent string }

type Request struct {
    Method string
    Path   string             // "/v9/projects/prj_x"
    Body   any                // marshalled to JSON when non-nil
    Query  map[string]string  // merged with the team scope
}

func (c *Client) Do(ctx context.Context, req Request, out any) error
```

Responsibilities, and nothing else:

- Bearer auth header, JSON in / JSON out.
- Team scoping: the client is constructed with a `TeamID` (or `Slug`); when set, it is
  injected as `teamId=` / `slug=` into every request's query string. Resource code never
  thinks about it.
- Non-2xx → `*APIError{StatusCode, Code, Message, Body}`, with `Code`/`Message` decoded
  from Vercel's `{"error":{"code","message"}}` envelope.

Rationale for a bespoke client over generating from the OpenAPI spec: the spec is a single
very large document covering ~400 endpoints, most of which are irrelevant, and its
response schemas are heavily `oneOf`-shaped (see the three alternative shapes of
`GET /v10/projects`). Hand-written structs for the handful of fields we manage are
smaller and more predictable than generated ones.

## Auth & config resolution

Credentials never live in a forma. Resolution order at request time:

1. `VERCEL_TOKEN` (Vercel CLI convention)
2. `VERCEL_API_TOKEN` (official Terraform provider convention)

Missing token → the operation fails with `InvalidCredentials`, not a panic.

Everything else comes from the forma target:

```pkl
new formae.Target {
  label = "vercel"
  config = new vercel.Config {
    teamId = "team_xxxx"   // optional; omit for personal-account scope
    slug   = null          // alternative to teamId
    baseUrl = null         // optional override, default https://api.vercel.com
  }
}
```

The target config is **re-parsed on every request** rather than cached at first use. The
agent multiplexes operations for different targets through one plugin process (discovery
sync right after a CRUD apply, extract across two teams, …); caching the first target
silently binds the process to stale team scoping. The HTTP client itself is only rebuilt
when the base URL or team scope actually changes.

## Package layout

```
vercel.go                       # ResourcePlugin impl: config methods + dispatch only
main.go                         # SDK entry point — never modified
pkg/
├── transport/vercel/
│   ├── client.go               # Do(), team-scope injection
│   └── errors.go               # APIError, IsNotFound, ClassifyStatus/ClassifyError
└── resources/
    ├── prov/                   # Provisioner interface + shared helpers
    │   ├── provisioner.go      #   the 6-method contract
    │   ├── nativeid.go         #   composite id join/split
    │   └── results.go          #   canned Success/Fail result builders
    ├── registry/registry.go    # resourceType -> Factory map, TargetConfig
    └── projects/               # VERCEL::Projects::* provisioners
        ├── project.go
        └── envvar.go
```

Same shape as `formae-plugin-supabase` and `formae-plugin-k8s`: resource files
self-register in `init()`, `vercel.go` side-effect-imports the packages and does nothing
but look up a factory and delegate. Adding a resource type touches exactly one new file.

A configuration-driven registry (the OVH/AWS `ResourceDefinition` + transformers approach)
is deliberately **not** used here. It pays for itself at 50+ resource types; at two, it is
pure overhead.

## Native ID format

| Resource | Native ID | Example |
|----------|-----------|---------|
| `VERCEL::Projects::Project` | `{projectId}` | `prj_abc123` |
| `VERCEL::Projects::EnvironmentVariable` | `{projectId}/{envId}` | `prj_abc123/EnvVarId` |

Team is *not* part of the native ID: it is target-level configuration, and a resource
cannot move between teams without being recreated. This matches how the Supabase plugin
keeps the org out of its IDs.

## HTTP status → Formae error code

| HTTP | Formae `OperationErrorCode` | Note |
|------|-----------------------------|------|
| 400, 422 | `InvalidRequest` | |
| 401 | `InvalidCredentials` | |
| 402 | `AccessDenied` | payment required — e.g. custom domain on Hobby |
| 403 | `AccessDenied`, **or** `AlreadyExists` when the message says the resource exists | Vercel returns 403 both for "not authorized" and for "env var already exists" |
| 404 | `NotFound` | |
| 409 | `AlreadyExists` | |
| 429 | `Throttling` | |
| 5xx | `ServiceInternalError` | |
| other | `InternalFailure` | |

`IsNotFound(err)` is separate from the status mapping because Delete treats a missing
resource as success (idempotent delete) and Read must report `NotFound` so the agent can
converge after an out-of-band deletion.

## Sync vs async

Every P1 operation is synchronous: `POST`/`PATCH`/`DELETE` return the final state in one
round-trip. `Status()` is therefore implemented as an immediate `Success` and no operation
ever returns `InProgress`. If deployments (P3) are added later, that is the resource type
that will need real polling — `GET /v13/deployments/{id}` exposes a `readyState` field for
exactly that.

## Discovery

`List()` returns native IDs only.

- Project: pages `GET /v10/projects` via `?from=<pagination.next>`, handling both the bare
  array and the `{projects, pagination}` response shapes.
- EnvironmentVariable: needs a project to enumerate under. With `Config.projectId` set it
  lists that project's vars; without it, it walks every project the token can see. The
  conformance target sets `projectId` so discovery stays inside the harness's timeout
  window regardless of how many projects the account holds — the same guard the Supabase
  plugin needed.

`DiscoveryFilters()` returns nil (nothing to exclude). `LabelConfig` uses `$.name` by
default and `$.key` for environment variables.

## Rate limiting

`RateLimit()` advertises 5 requests/second, namespace-scoped. Derived in `RESOURCES.md`:
the tightest relevant bucket is env-var deletion at 60/minute, and buckets are per
endpoint, so 5 rps across a mixed workload stays well inside every limit while leaving
headroom for bursts. Vercel returns 429 with `code: "rate_limited"`, which maps to
`Throttling` so the agent backs off and retries rather than failing the apply.
