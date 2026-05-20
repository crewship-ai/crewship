# InstanceSetting

`kind: InstanceSetting` declares instance-wide key/value settings stored in the
`app_settings` table. Unlike every other SPEC-2 kind, the document's
`metadata.name` and `metadata.slug` fields are **advisory only** — they exist
to give the file a human-readable identifier in the manifest, but the real
keys live field-level inside `spec.settings`. Each entry in that map maps 1:1
to one `app_settings` row.

InstanceSetting is admin-scoped: applying it changes settings for the entire
Crewship instance, not for a single workspace. Only users with the ADMIN role
on at least one workspace can write; OWNER/ADMIN can read.

## YAML schema

```yaml
apiVersion: crewship/v1
kind: InstanceSetting
metadata:
  name: SMTP and branding
  slug: smtp-branding             # advisory — not the idempotency key
  description: Outbound mail + product brand strings
spec:
  settings:
    smtp.host: smtp.gmail.com
    smtp.port: "587"
    smtp.user: noreply@crewship.ai
    smtp.password: ${SMTP_PASSWORD}        # ${ENV_VAR} interpolation
    branding.product_name: Crewship
    branding.primary_color: "#3B82F6"
    feature.allow_registration: "true"
```

Every value is a string. Booleans and numbers must be quoted (`"587"`,
`"true"`) — YAML's typed scalars are intentionally not honoured because the
backend stores values as `TEXT` and re-typing at the manifest layer would
introduce drift.

## How Plan works

For each `(key, value)` in `spec.settings`:

1. **Resolve `${ENV_VAR}` placeholders** in the value (see below).
2. **Look up the current remote value** via
   `GET /api/v1/instance/settings/{key}`.
3. **Compare**:
   - If the remote value equals the resolved manifest value, emit an
     **Unchanged** plan item.
   - Otherwise (or if the key is missing remotely), emit an **Update** plan
     item that will `PUT /api/v1/instance/settings/{key}` with the resolved
     value.

In **ApplyReplace** mode (`crewship apply --mode replace`), Plan additionally
enumerates every key present remotely but not declared in `spec.settings` and
emits a **Delete** plan item for each — except for protected keys (see below),
which surface as Unchanged with a "protected; skipped" description.

## Environment variable interpolation

Values may contain `${VAR_NAME}` placeholders that are resolved at plan time
against the process environment (via `os.LookupEnv`). The grammar is strict:

| Form               | Behaviour                                                    |
| ------------------ | ------------------------------------------------------------ |
| `${SMTP_PASSWORD}` | Replaced with the value of `$SMTP_PASSWORD`.                 |
| `${X}suffix`       | Replaced; the literal "suffix" is preserved.                 |
| `${X:-fallback}`   | **Not supported**. The whole token is treated as a typo and the apply fails. |
| `$VAR` (no braces) | Treated as a literal `$VAR` — only the `${...}` form is recognised. |

**Missing variables are a hard error**, never silent. If you reference
`${SMTP_PASSWORD}` and that variable is unset in the apply process's
environment, the apply aborts with a clear message naming the missing
variable. This prevents the common footgun of an empty password being
silently written into the database.

Tip: when scripting an apply, prefer

```sh
SMTP_PASSWORD="$(read-from-vault)" crewship apply --file settings.yaml
```

over `export SMTP_PASSWORD=...; crewship apply ...` so the secret only exists
in the apply process's environment and never lands in the parent shell's
history.

## Sensitive keys — best practices

Keys with these prefixes/suffixes are considered sensitive by the backend
handler:

- Prefix `smtp.password`, `oauth.`, `webhook.`
- Suffix `.password`, `.secret`, `.client_secret`, `.api_key`, `.token`

The backend returns sensitive values as the placeholder `"***"` on read.
Plan treats `"***"` as "unknown" and always emits an Update plan item — the
server's PUT handler is idempotent so a redundant write of the same value is
cheap.

`Document.Warnings()` returns a list of `SensitiveValueWarning` for keys that
match the sensitive shape but carry a **literal** (non-`${ENV}`) value. The
CLI surfaces these so you can rewrap them:

```diff
- smtp.password: hunter2
+ smtp.password: ${SMTP_PASSWORD}
```

The warning is informational only; nothing blocks the apply. There are
legitimate workflows (sealed-secrets, vault-rendered templates) that produce
literal values at write time.

## Protected keys

ApplyReplace mode is destructive — it deletes every remote key that isn't
declared in the manifest. To prevent the apply from bricking the instance,
Plan never emits a Delete for these system-managed keys:

| Key                       | Owner                          |
| ------------------------- | ------------------------------ |
| `instance.bootstrap_at`   | First-boot bootstrap path      |
| `instance.first_user_id`  | First-boot bootstrap path      |
| `schema.version`          | Database migration runner      |

If you declare any of these in `spec.settings`, the value you specify is
written normally — the protection only kicks in for the ApplyReplace prune
pass. The backend handler enforces the same whitelist as the ultimate
gatekeeper; the manifest mirror exists so dry-run output shows clean
"skipped (protected)" lines instead of opaque 403s during apply.

## Endpoint contract

| Method | Path                                | Purpose                  | Required role |
| ------ | ----------------------------------- | ------------------------ | ------------- |
| GET    | `/api/v1/instance/settings`         | List all key/value pairs | OWNER/ADMIN   |
| GET    | `/api/v1/instance/settings/{key}`   | Read one key             | OWNER/ADMIN   |
| PUT    | `/api/v1/instance/settings/{key}`   | Upsert; body `{value}`   | ADMIN         |
| DELETE | `/api/v1/instance/settings/{key}`   | Remove key (non-protected) | ADMIN       |

The manifest layer gracefully handles `404 Not Found` on the list endpoint by
treating remote state as empty — this lets `crewship apply --dry-run` produce
useful plans even on instances where the handler is not yet deployed.

## Export

`crewship export workspace` calls `ExportInstanceSettings`, which produces a
single `InstanceSetting` document with every server-side key collapsed into
one `spec.settings` map. Sensitive values appear as `"***"` placeholders —
this is by design: dropping them would round-trip wrong (a re-apply in
ApplyReplace mode would emit a delete for the omitted key).

After export, **hand-edit** the file to replace each `"***"` with the
appropriate `${ENV_VAR}` reference before checking it into version control.

## CLI surface (related)

```sh
crewship instance settings list                 # tabular view of all keys
crewship instance settings get <key>            # one key
crewship instance settings set <key> <value>    # upsert without a manifest
crewship instance settings delete <key>         # delete (subject to protection)
```

These commands hit the same endpoints the manifest layer uses; the manifest
form is preferred for anything that should be version-controlled.

## Worked examples

### Minimal — upsert one key

```yaml
apiVersion: crewship/v1
kind: InstanceSetting
metadata: { name: branding, slug: branding }
spec:
  settings:
    branding.product_name: "Acme Internal"
```

### Mix of literal and env-interpolated values

```yaml
apiVersion: crewship/v1
kind: InstanceSetting
metadata: { name: smtp, slug: smtp }
spec:
  settings:
    smtp.host: smtp.sendgrid.net
    smtp.port: "587"
    smtp.user: apikey
    smtp.password: ${SENDGRID_API_KEY}
```

### ApplyReplace — declarative full state

```sh
crewship apply --file settings.yaml --mode replace --yes
```

With the manifest above, ApplyReplace will:

- Update the four `smtp.*` keys to the declared values.
- Delete every other non-protected key in `app_settings`.
- Skip `instance.bootstrap_at`, `instance.first_user_id`, `schema.version`
  (protected) with an "Unchanged (protected)" line in the plan.
