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
| [`private-repo/`](private-repo/) | Deploying from a private repository, and the one-time manual step it needs | Needs a Git provider connected to the account first — no API can do that |
| [`discover/`](discover/) | A bare target and nothing else — the agent discovers everything the token can see | No |
| [`full-stack/`](full-stack/) | A whole project from one apply: custom environment, three flavours of environment variable, a secret formae draws and rotates for itself, a Global Config store and a webhook, wired together with `site.res.id` | No. Global Config *writes* are metered on paid plans; creating the store is not |
| [`supabase-vercel/`](supabase-vercel/) | A Supabase database and a Vercel frontend in one forma — the API key is created and handed to Vercel in the same apply | Needs both plugins installed and the two repos side by side; a Supabase project is a real database |

All of them evaluate offline with `pkl eval`, and all pass a live
`--simulate` against a running agent:

```bash
cd examples/dns && pkl project resolve && pkl eval main.pkl        # offline
formae apply --mode reconcile --simulate --yes examples/dns/main.pkl  # agent
```

`pkl eval` only proves the forma renders. Simulate is what proves formae will
accept it — it caught three defects these examples would otherwise have shipped
with (an empty stack, an apex DNS record, and unset optional drain blocks).

## Notes that catch people out

- **`recordType`, not `type`**, and **`targets`, not `target`** — `formae.Resource`
  reserves both names, so the schema renames them and the plugin maps them back
  on the wire.
- **A custom environment's name is `slug`**, matching the API's own wire name.
- **Secrets**: use `variableType = "encrypted"`, not `"sensitive"`. Vercel never
  returns a `sensitive` value, so formae reports drift on every sync, the
  variable is not discoverable, and `secretValue` has nothing to resolve.
- **Let formae draw the secret.** Declare a `formae.PasswordGenerator`, put it
  in the forma, and bind the variable to its output — `value = pw.gen.value`.
  Without a `rotation` it is drawn once and never changes, which is what
  replaces minting a password at eval time and pinning it with `setOnce`; with
  one, formae rotates it on schedule and moves the variable with it.
  `full-stack/` rotates, `private-repo/` and `supabase-vercel/` draw once.
  Supply your own instead with `formae.value(...).opaque`.
- **An environment variable is a secret you can reference.** The plugin reads
  values back decrypted, so `envVar.res.secretValue` resolves to the live value
  on every plugin call, and `.json("path")` reaches into a JSON payload. Use it
  to hand one variable's value to another resource without copying it into the
  forma.
- **A stack needs at least one resource.** `discover/` therefore declares a
  target and no stack; discovered resources land on the built-in `unmanaged`
  stack.
- **An apex DNS record is `name = ""`.** formae's required-field check treats an
  empty string as missing, so `DNSRecord.name` is deliberately not marked
  `requiredOnCreate`.
