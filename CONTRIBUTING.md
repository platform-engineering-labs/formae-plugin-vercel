# Contributing

This document covers local development for plugin authors. For user-facing
plugin docs (configuration, supported resources, examples), see
[README.md](README.md).

## Prerequisites

- Go 1.25+
- [Pkl CLI](https://pkl-lang.org/main/current/pkl-cli/index.html)
- Cloud provider credentials (for conformance testing)

## Local Installation

The Hub-facing install path for end users is `formae plugin install
<publisher>/<plugin>` — that pulls signed artifacts from the orbital
repo. For plugin authors building locally, install from source:

```bash
make install
```

This builds the plugin binary and installs it into your local formae
plugin directory so the agent picks it up on the next start.

## Building

```bash
make build      # Build plugin binary
make test       # Run unit tests
make lint       # Run linter
make install    # Build + install locally
```

## Local Testing

```bash
# Install plugin locally
make install

# Start formae agent
formae agent start

# Apply example resources
formae apply --mode reconcile --watch examples/basic/main.pkl
```

## Conformance Testing

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

### In CI

The `conformance-tests` job runs on every push to `main`, on every pull request,
nightly, and on `workflow_dispatch`. Every run creates and destroys real Vercel
resources under the `formae-sdk-test` name prefix.

A pull request from a fork gets no repository secrets, so the job logs a notice
and exits 0 instead of failing on a missing token.

## Discovery Reliability

`List` distinguishes three outcomes rather than two, because "no resources" is
an answer formae acts on:

| Outcome | Behaviour |
|---|---|
| 401 / 403 / 404 | An empty list. A token scoped away from a resource type genuinely sees none, and one such type must not stop the others being discovered. |
| 429 / 5xx / connection failure | Retried with backoff (4 attempts, ~7s), then **failed**. Reporting an empty list here is how formae comes to believe managed resources were deleted. |
| Success | Every page. Collections that paginate declare the cursor parameter their endpoint uses (`PageParam`), and the engine follows `pagination.next` to the end. |

Discovery is the one place the plugin retries on its own: every write goes
through the agent's operator, which has its own retry loop, but `List` is called
straight from the scan loop with nothing above it to try again.

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

The job runs a **filtered set of fixtures** by default — the ten that pass on an
account with the capabilities they need (eleven in the discovery phase, which
also covers the singleton). The remaining three
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

## Publishing to the Hub

The formae Hub accepts plugins under one of these SPDX licenses:

- `Apache-2.0`
- `BSD-3-Clause`
- `MIT`
- `MPL-2.0`

Set the `license` field in `formae-plugin.pkl` to the matching identifier
and copy the corresponding file from `licenses/` to `LICENSE`. Plugins
under any other license can still be built and used locally, but the
Hub registration step will reject them.

For the full publishing flow, see the
[Plugin SDK Documentation](https://docs.formae.io/plugin-sdk).
