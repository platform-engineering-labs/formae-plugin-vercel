# Vercel Plugin for Formae

[![CI](https://github.com/platform-engineering-labs/formae-plugin-vercel/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/platform-engineering-labs/formae-plugin-vercel/actions/workflows/ci.yml)

Formae plugin for managing Vercel resources.

## Supported Resources

| Resource Type | Description |
|---------------|-------------|
| `VERCEL::Projects::Project` | Projects, build settings, and the connected Git repository |
| `VERCEL::Projects::EnvironmentVariable` | Environment variables, per deployment target |
| `VERCEL::Projects::CustomEnvironment` | Custom deployment environments |
| `VERCEL::Projects::Route` | Redirects, rewrites and status rules |
| `VERCEL::Projects::Domain` | A domain or subdomain attached to a project |
| `VERCEL::DNS::Record` | DNS records in a domain Vercel is authoritative for. No in-place update — the API's PATCH replaces the record and reissues its id, so a change is a replace |
| `VERCEL::GlobalConfig::Config` | Global Config stores (formerly Edge Config) |
| `VERCEL::Webhooks::Webhook` | Account webhooks |
| `VERCEL::VCR::Repository` | Container registry repositories |
| `VERCEL::FeatureFlags::Flag` | Feature flag definitions |
| `VERCEL::FeatureFlags::Segment` | Reusable audiences for flag rules |
| `VERCEL::FeatureFlags::SDKKey` | Flags SDK keys, per environment |
| `VERCEL::FeatureFlags::Settings` | Per-project feature flag configuration |
| `VERCEL::Domains::Domain` | Registers a domain on the account — the prerequisite for DNS records and for attaching a domain to a project. No update; adding does not verify |
| `VERCEL::Drains::Drain` | Log, trace and audit-log drains. One type covers all three: same endpoint, differing only by `schemas` and delivery type |
| `VERCEL::AccessGroups::AccessGroup` | Access groups. Update verb is POST; id is `accessGroupId` |
| `VERCEL::AccessGroups::ProjectAssignment` | A project's role inside an access group |
| `VERCEL::Auth::Token` | Account access tokens. The token value is returned once at create and deliberately not stored as state |
| `VERCEL::Projects::Member` | Who may work on a project, and in what role. No update endpoint, so a role change replaces |
| `VERCEL::Networking::Network` | Secure Compute networks. **Asynchronous** — create polls until `status: ready` |
| `VERCEL::Deployments::Alias` | Created under a deployment, but read, listed and deleted account-wide |
| `VERCEL::Certs::UploadedCertificate` | Your own certificate: three PEM blobs, all write-only. Discovery is off on purpose |

## Configuration

Configure a Vercel target in your Forma file. Every field is optional; a bare
config manages the token's personal account.

```pkl
new formae.Target {
    label = "vercel"
    config = new vercel.Config {
        teamId = "team_abc123"   // or slug = "my-team"; omit for personal scope
        baseUrl = null           // override https://api.vercel.com
        projectId = null          // scope env-var discovery to one project
    }
}
```

Authentication reads an access token from the environment, never from a Forma.
Create one at <https://vercel.com/account/settings/tokens>:

- `VERCEL_TOKEN` — the Vercel CLI's name. Checked first.
- `VERCEL_API_TOKEN` — the other conventional name. Fallback.

A personal-account token can act on a team when `teamId`/`slug` is set; a
team-scoped token reaches only that team.

## Examples

See [examples/](examples/) for usage patterns:

- `basic/` - A project and an environment variable
- `private-repo/` - Deploying from a private repository, and the manual step it needs
- `full-stack/` - Project, environment variables, custom environment and routes
- `supabase-vercel/` - A Supabase database wired into a Vercel frontend across two plugins
- `discover/` - Adopting resources that already exist

## Notes

**Connecting a Git repository needs a one-time manual step.** Setting
`gitRepository` requires a Git provider already connected to the Vercel account
— an OAuth / App-install flow that no documented endpoint can perform. Without
it, project creation fails with `You need to add a Login Connection to your
GitHub account first`, for public repositories as much as private ones. See
[`examples/private-repo/`](examples/private-repo/).

**Two fields are renamed** because `formae.Resource` reserves the API's names:
`EnvironmentVariable.variableType` is the API's `type`, and
`EnvironmentVariable.targets` is its `target`.

**Use `encrypted`, not `sensitive`, for secret environment variables.** Vercel
never returns a `sensitive` value, so a Forma-managed one reports drift on every
sync and is not discoverable. `encrypted` round-trips.

**An environment variable is a first-class secret.** Its value is read back
decrypted on every plugin call, so other resources reference it through the
uniform accessor — `envVar.res.secretValue`, or `.json("path")` to reach into a
JSON payload — rather than holding a copy. A `sensitive` variable has no value
to resolve.

**Let formae draw and rotate the value.** Bind the variable to a generator
instead of writing a secret into the forma: `value = pw.gen.value` against a
`formae.PasswordGenerator` in the same stack. With no `rotation` the value is
drawn once; with one, formae turns it over on schedule and moves the variable
with it. `UploadedCertificate.key` takes the private half of a
`formae.KeyPairGenerator` the same way. See
[`examples/full-stack/`](examples/full-stack/).

**`Project.name` and `Project.gitRepository` are immutable.** Changing either
replaces the project, which changes every generated deployment URL.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for building, testing and the conformance
suite.

## License

FSL-1.1-ALv2
