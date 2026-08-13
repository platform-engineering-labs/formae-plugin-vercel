# Examples

Each directory is a self-contained Pkl project. `cd` into it, or pass the path
to `formae apply`:

```bash
export VERCEL_TOKEN=...
formae apply --mode reconcile --watch examples/basic/main.pkl
```

| Example | Shows | Costs anything? |
|---------|-------|-----------------|
| [`basic/`](basic/) | Smallest useful forma: a project and one environment variable | No |
| [`discover/`](discover/) | A bare target and nothing else — the agent discovers everything the token can see | No |
| [`full-stack/`](full-stack/) | A whole project from one apply: custom environment, three flavours of environment variable, a Global Config store and a webhook, wired together with `site.res.id` | No. Global Config *writes* are metered on paid plans; creating the store is not |
| [`dns/`](dns/) | A domain attached to a project, an apex redirect, and A / MX / TXT records | Custom domains need a paid plan — Hobby answers `custom_domain_needs_upgrade` |
| [`drains/`](drains/) | Log and trace drains — one formae type covering what Terraform splits into three resources | Drains are billed per GB delivered |

All of them evaluate offline with `pkl eval`, so you can check a change before
applying it:

```bash
cd examples/dns && pkl project resolve && pkl eval main.pkl
```

## Notes that catch people out

- **`recordType`, not `type`**, and **`targets`, not `target`** — `formae.Resource`
  reserves both names, so the schema renames them and the plugin maps them back
  on the wire.
- **A custom environment's name is `slug`**, matching the API rather than the
  Terraform provider.
- **Secrets**: use `variableType = "encrypted"`, not `"sensitive"`. Vercel never
  returns a `sensitive` value, so formae reports drift on every sync and the
  variable is not discoverable.
- **`formae.value(...).opaque.setOnce`** keeps a secret out of state and logs.
  The `random.password` generator shown in `full-stack/` only resolves under the
  formae agent — bare `pkl eval` refuses the resource it reads.
