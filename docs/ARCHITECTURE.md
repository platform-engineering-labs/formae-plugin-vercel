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
    │   ├── results.go          #   canned Success/Fail result builders
    │   └── projects.go         #   paged project-id lookup, shared by both layers
    ├── registry/registry.go    # resourceType -> Factory map, TargetConfig
    ├── rest/                   # the generic REST engine
    │   ├── definition.go       #   Definition: one declarative resource description
    │   ├── resource.go         #   the CRUD+List engine over a Definition
    │   ├── async.go            #   AsyncSpec status polling
    │   ├── singleton.go        #   Singleton child resources (no id of their own)
    │   └── bag.go              #   BagSpec: one keyed set, one atomic write
    ├── defs/                   # Definitions, grouped per file, self-registering
    │   ├── defs.go             #   AddGroup/All + the core group
    │   ├── teams.go, drains.go, featureflags.go
    └── projects/               # hand-written VERCEL::Projects::* provisioners
        ├── project.go
        └── envvar.go
```

`vercel.go` side-effect-imports the resource packages and does nothing but look up a
factory and delegate — the same shape as `formae-plugin-supabase` and
`formae-plugin-k8s`.

Two ways in, and the choice is not stylistic:

- **Declarative** (`pkg/resources/defs`) — the default. A resource whose API is plain
  CRUD on an id is one `rest.Definition` literal plus a PKL class; the engine in
  `pkg/resources/rest` does the rest. Each file in `defs` contributes a group from its
  own `init()`, so definitions can be added from several directions without any file
  editing another.
- **Hand-written** (`pkg/resources/projects`) — for resources the engine cannot
  express. `Project` (three API versions across five operations, two list response
  shapes, paged discovery) and `EnvironmentVariable` (create takes an object *or* an
  array, its 201 carries a `failed` array, Read has to list-and-filter) are both here.

The declarative path was not the original design — the plugin started with two
hand-written provisioners on the grounds that a definition-driven registry only pays for
itself at scale. It reached that scale: `rest.Definition` now backs 17 of the 19 types.
The engine's capability set (`Async`, `Singleton`, `Bag`, `Wrap`/`Unwrap`, `ItemQuery`,
`DeleteBody`, `ParentInBody`, `ParentFromField`, `CreateIDFromProperty`) grew one
capability per resource that needed it, each inert unless a Definition declares it —
which is what keeps a new capability from changing the behaviour of the resources
already shipped. `docs/RESOURCES.md` records which remaining Terraform resources fit the
engine and which still need hand-written Go.

## Native ID format

Account-scoped resources use the API's own id. Resources that live under a parent use
`{parentId}/{id}`, joined and split by `prov.NativeID`; a `Singleton` child has no id of
its own, so its native id is the parent alone.

| Resource | Native ID | Example |
|----------|-----------|---------|
| `VERCEL::Projects::Project` | `{projectId}` | `prj_abc123` |
| `VERCEL::Projects::EnvironmentVariable` | `{projectId}/{envId}` | `prj_abc123/EnvVarId` |
| `VERCEL::Webhooks::Webhook` | `{webhookId}` | `account_hook_abc123` |
| `VERCEL::DNS::Record` | `{domain}/{recordId}` | `example.com/rec_abc123` |

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

Most Vercel operations are synchronous: `POST`/`PATCH`/`DELETE` return the final
state in one round-trip, so `Status()` reports Success immediately.

Some are not. A Secure Compute network reports `status: create_in_progress` and
only later becomes `ready`. A Definition declares that with an `AsyncSpec`:

```go
Async: &rest.AsyncSpec{
    StatusField: "status",
    Pending:     []string{"create_in_progress", "delete_in_progress"},
    Failed:      []string{"error"},
    Ready:       []string{"ready"},
}
```

Create and Update then return `InProgress` with a `RequestID`, and `Status()`
re-reads the resource until it settles. Two deliberate choices: a create
response *missing* the status field is polled rather than assumed ready, and a
status value nobody enumerated is reported `InProgress` rather than Success —
reporting success for a state we do not understand is the failure mode that
breaks everything downstream.

Deployments (still unimplemented) would need the same treatment plus file
upload.

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
