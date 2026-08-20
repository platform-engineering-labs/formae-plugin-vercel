# Vercel Plugin for formae

A [formae](https://github.com/platform-engineering-labs/formae) plugin for managing
[Vercel](https://vercel.com/) resources through the Vercel REST API
(`https://api.vercel.com`).

## Installation

```bash
make install
```

Installs the binary, schema and manifest to `~/.pel/formae/plugins/vercel/v<version>/`.

## Supported Resources

23 resource types. Each implements Create, Read, Update, Delete and List
unless noted.

| Resource Type | Notes |
|---------------|-------|
| `VERCEL::Projects::Project` | An empty project (no Git repo, no deployment) is free and instant. Set `gitRepository` to connect a repo so pushes deploy. `name` and `gitRepository` are immutable — changing either replaces the project. |
| `VERCEL::Projects::EnvironmentVariable` | Reference the project with `project.res.id`. |
| `VERCEL::Projects::CustomEnvironment` | Named `slug` on the wire, not `name`. |
| `VERCEL::Projects::Domain` | Keyed by the domain name. Custom domains need a paid plan. |
| `VERCEL::Domains::Domain` | Registers a domain on the account — the prerequisite for `DNS::Record` and for attaching a domain to a project. No update (the API's PATCH is op-based); adding does not verify. |
| `VERCEL::DNS::Record` | Record type is `recordType` here — `type` is reserved. |
| `VERCEL::Projects::Route` | Redirects, rewrites and status rules. Every write stages a version and the plugin promotes it, so a rule is live when the apply succeeds. |
| `VERCEL::Projects::Member` | Who may work on a project, and in what role. Identified by `uid`; no update endpoint, so a role change replaces the membership. |
| `VERCEL::FeatureFlags::Settings` | Per-project feature-flag configuration. A singleton keyed by the project. "Delete" resets it to disabled, since the endpoint has no DELETE and a type that cannot be deleted makes its stack undestroyable. |
| `VERCEL::GlobalConfig::Config` | Edge Config's new API name. `slug` is immutable, so changes replace. |
| `VERCEL::Webhooks::Webhook` | No update endpoint; any change replaces. |
| `VERCEL::Networking::Network` | **Asynchronous** — create polls until `status: ready`. |
| `VERCEL::AccessGroups::AccessGroup` | Update verb is POST; id is `accessGroupId`. |
| `VERCEL::AccessGroups::ProjectAssignment` | A project's role inside an access group. |
| `VERCEL::Auth::Token` | The token value is returned once at create and is deliberately not stored as state. |
| `VERCEL::VCR::Repository` | Container registry repository. No update. |
| `VERCEL::Certs::Certificate` | Vercel-issued cert for a set of common names. |
| `VERCEL::Certs::UploadedCertificate` | Your own cert: three PEM blobs, all write-only. |
| `VERCEL::Deployments::Alias` | Created under a deployment, but read/listed/deleted account-wide. |
| `VERCEL::Drains::Drain` | One type covers log, trace **and** audit-log drains — they are all `POST /v1/drains`, differing only by `schemas` and delivery type. |
| `VERCEL::FeatureFlags::Flag` | Create verb is PUT. |
| `VERCEL::FeatureFlags::Segment` | Create verb is PUT. |
| `VERCEL::FeatureFlags::SDKKey` | Keyed by `hashKey`. No update. |

Most operations are synchronous. `Networking::Network` is not: create returns
InProgress and polls until the network is actually usable.

### Scope policy

This plugin uses **only endpoints Vercel publicly documents** — those in the REST
reference and its machine-readable spec at <https://openapi.vercel.sh/>. Several
things Vercel itself supports are reachable only through paths it does not
document (blob stores, OAuth apps, project crons, integration project access), so
they are out of scope: an undocumented path can change without notice, and there
would be no ground to stand on when it does.

Resources are also left out when the API cannot round-trip them. A create
response with no id, a read that returns none of the managed fields, or a
collection with no list endpoint each make a resource undeclarable rather than
merely unwritten.

## Credentials

The plugin reads an access token from the environment — never from a forma. Create one
at <https://vercel.com/account/settings/tokens>.

| Variable | Description |
|----------|-------------|
| `VERCEL_TOKEN` | Access token (Vercel CLI convention). Checked first. |
| `VERCEL_API_TOKEN` | Same thing under an alternative conventional name. Fallback. |

```bash
export VERCEL_TOKEN=...
```

A token created for your personal account can still act on a team's resources — set
`teamId` (or `slug`) on the target for that. A token scoped to one team can only reach
that team.

## Target configuration

Every field is optional. A bare `Config {}` deploys to, and discovers, the token's
personal account:

```pkl
import "@formae/formae.pkl"
import "@vercel/core/vercel.pkl"

new formae.Target {
  label = "vercel"
  config = new vercel.Config {}
}
```

| Field | Purpose |
|-------|---------|
| `teamId` | Act on behalf of a team (`?teamId=`), e.g. `"team_abc123"` |
| `slug` | Same, by team slug (`?slug=`). Ignored when `teamId` is set. |
| `baseUrl` | Override `https://api.vercel.com` |
| `projectId` | Advanced: scope environment-variable `List()`/discovery to one project instead of walking every project the token can see |

## Example usage

```pkl
amends "@formae/forma.pkl"

import "@formae/formae.pkl"
import "@vercel/core/vercel.pkl"

local site = new vercel.Project {
  label = "my-site"
  name = "my-site"
  framework = "nextjs"
  buildCommand = "npm run build"
  outputDirectory = ".next"

  // Optional. Connect a repository so pushes deploy automatically.
  // Immutable: changing it replaces the project.
  gitRepository = new vercel.GitRepository {
    type = "github"
    repo = "my-org/my-site"
  }
}

forma {
  new formae.Stack {
    label = "default"
    description = "Default stack"
  }

  new formae.Target {
    label = "vercel"
    config = new vercel.Config {}
  }

  site

  new vercel.EnvironmentVariable {
    label = "api-url"
    projectId = site.res.id
    key = "NEXT_PUBLIC_API_URL"
    value = "https://api.example.com"
    variableType = "plain"
    targets = new Listing { "production"; "preview" }
  }
}
```

```bash
formae apply --mode reconcile --watch examples/basic/main.pkl
```

The full example is in [`examples/basic/`](examples/basic/).

### Naming notes

Two fields deviate from the Vercel API's own names because `formae.Resource` already
reserves those identifiers:

| Forma field | Vercel API field |
|-------------|------------------|
| `EnvironmentVariable.variableType` | `type` |
| `EnvironmentVariable.targets` | `target` |

### Secret values

`variableType` defaults to `encrypted`, which the plugin reads back with `decrypt=true`,
so values round-trip and formae sees no drift. `sensitive` variables are **never**
returned by the Vercel API — a formae-managed `sensitive` variable will report drift on
every sync and is not discoverable. Use `plain` or `encrypted`, and wrap the value in a
formae secret if it must not appear in the forma:

```pkl
value = formae.value(random.password(32, false)).opaque.setOnce
```

## Testing

```bash
make test-unit           # unit tests (no credentials required)
make lint
make verify-schema
make install             # conformance runs against the INSTALLED binary
make conformance-test
```

Conformance parameters:

| Parameter | Meaning |
|-----------|---------|
| `TEST` | Filter test cases by name, e.g. `TEST=project`. Comma-separated for several. |
| `VERSION` | formae version to test against, as a **bare semver** (`VERSION=0.88.1`). Omit it to let the harness choose from the stable channel. `latest` is **not** valid — the harness parses this with semver and every fixture fails in setup. |
| `TIMEOUT` | Per-operation timeout in **minutes** (bare number, not a Go duration). Harness default is 5. |
| `PARALLEL` | Max parallel test cases |
| `TESTDATA_DIR` | Alternate testdata directory |
| `GOTEST_TIMEOUT` | Wall-clock limit for the whole run, default `60m` |

`TIMEOUT` bounds a single resource operation; `GOTEST_TIMEOUT` bounds the entire
`go test` invocation. They are different things — passing a Go duration such as
`15m` to `TIMEOUT` is not valid.

Conformance tests create and destroy real Vercel projects. They need:

| Variable | Required | Purpose |
|----------|----------|---------|
| `VERCEL_TOKEN` | yes | Access token |
| `VERCEL_TEAM_ID` | no | Run against a team instead of the personal account |
| `VERCEL_PROJECT_ID` | no | Scope env-var discovery to one project on large accounts |

### Conformance in CI

The `conformance-tests` job runs on every push to `main`, on every pull request,
nightly, and on `workflow_dispatch`. Every run creates and destroys real Vercel
resources under the `formae-sdk-test` name prefix.

A pull request from a fork gets no repository secrets, so the job logs a notice
and exits 0 instead of failing on a missing token.

Runs are **serialized repo-wide** through a `concurrency` group. Resource names
carry `FORMAE_TEST_RUN_ID`, but `scripts/ci/clean-environment.sh` deletes
everything matching the shared prefix, before and after each run — two concurrent
runs would delete each other's live projects mid-apply. `cancel-in-progress` is
deliberately `false`: cancelling mid-apply abandons real resources, whereas
letting a run finish lets its own post-test cleanup remove them. Expect a queue
of roughly 8 minutes per run when several land together.

Set these under **Settings → Secrets and variables → Actions**:

| Secret | Required | Purpose |
|--------|----------|---------|
| `VERCEL_TOKEN` | yes | Without it the job logs a notice and exits 0 rather than failing confusingly |
| `VERCEL_TEAM_ID` | no | Run against a team instead of the token's personal account |

The job runs a **filtered set of fixtures** by default — the eight that pass on
an account with the capabilities they need. The remaining three
(`accessgroup`, `authtoken`, `drain`) fail with a `403` from the plan or the
token's scope, and a job that always fails is a job nobody reads. Dispatch with
`test_filter` emptied to run all eleven and see the full picture.

Treat that filter with suspicion, though. Two fixtures sat outside it as
"account capability" failures and were in fact plugin bugs — the filter hid
them exactly as deleting them would have. Re-run the excluded ones against the
API before believing the reason given for any of them.

Use literal fixture names in `test_filter`, comma separated. The harness also
accepts a `/regex/` form, but `TEST` passes through `make`, which treats a bare
`$` as a variable reference — an anchored `/…$/` pattern silently loses its
anchor and then matches nothing.

Each run isolates its resources with `FORMAE_TEST_RUN_ID`, so a nightly and a
manual dispatch cannot clean up each other's projects.

Every test resource is named `formae-sdk-test-*`;
[`scripts/ci/clean-environment.sh`](scripts/ci/clean-environment.sh) deletes anything
matching that prefix before and after each run. It requires `jq`.

## License

FSL-1.1-ALv2. See [LICENSE](LICENSE).
