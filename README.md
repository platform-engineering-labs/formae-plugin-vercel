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

An initial vertical slice: projects and their environment variables. Both implement
Create, Read, Update, Delete and List. Every operation is synchronous — nothing polls.

| Resource Type | Description |
|---------------|-------------|
| `VERCEL::Projects::Project` | A Vercel project. An empty project (no Git repository, no deployment) is free and provisions instantly. `name` is immutable — changing it replaces the project. |
| `VERCEL::Projects::EnvironmentVariable` | An environment variable on a project. Reference the project with `project.res.id`. |

See [`docs/RESOURCES.md`](docs/RESOURCES.md) for the full catalog of Vercel resource
types, their endpoints, priorities, and how the implemented ones map to the official
`vercel/terraform-provider-vercel` schemas. Design notes live in
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

## Credentials

The plugin reads an access token from the environment — never from a forma. Create one
at <https://vercel.com/account/settings/tokens>.

| Variable | Description |
|----------|-------------|
| `VERCEL_TOKEN` | Access token (Vercel CLI convention). Checked first. |
| `VERCEL_API_TOKEN` | Same thing under the official Terraform provider's name. Fallback. |

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
| `TEST` | Filter test cases by name, e.g. `TEST=project` |
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

Every test resource is named `formae-sdk-test-*`;
[`scripts/ci/clean-environment.sh`](scripts/ci/clean-environment.sh) deletes anything
matching that prefix before and after each run. It requires `jq`.

## License

FSL-1.1-ALv2. See [LICENSE](LICENSE).
