# Credentials Vault

Crewship's credentials subsystem is a **runtime vault for AI agents**:
secrets are stored encrypted at rest, decrypted only inside the trusted
orchestrator, and mounted into the agent container as either env vars
or files at session start. The positioning is HashiCorp Vault OSS's
injection use case — *not* a general-purpose password manager. There is
no browser autofill, no sharing UI, no native client.

This contract covers current type policy and native file delivery. The policy
source is `internal/credpolicy/credpolicy.go`; an unknown type is withheld.
Keeper is an access policy, not another credential type. Only `SECRET` is gated
by the current table; changing type to avoid a required gate is not onboarding.
Adapter-specific login behavior is documented in `docs/guides/credentials.mdx`.

## Supported credential types

`D` below is the per-run `$CREWSHIP_SECRETS_DIR`, currently
`/secrets/<agent-slug>/<run-id>`. A slot is the resolver's delivered name.

| Type | Delivery |
| --- | --- |
| `API_KEY` | Sidecar provider injection where the adapter has a routable endpoint; supported direct fallbacks expose an env value. |
| `AI_CLI_TOKEN` | Adapter-specific subscription derivative: token env or rendered login file; not generally secretless proxy delivery. |
| `PROVIDER_LOGIN` | Adapter-specific derivative of a subscription or metered login; stored refresh-token parts remain server-side. |
| `CLI_TOKEN` | `D/<SLOT>` mode 0400 **and the direct token value in `<SLOT>`** for standard CLI token conventions. |
| `SECRET` | `D/<SLOT>` mode 0400, path in `<SLOT>`; withheld when Keeper is enabled. |
| `GENERIC_SECRET` | `D/<SLOT>` mode 0400, path in `<SLOT>`; not Keeper-gated by current policy. |
| `USERPASS` | Two mode-0400 files; `<SLOT>_USERNAME` and `<SLOT>_PASSWORD` contain paths, not their values. |
| `SSH_KEY` | Unencrypted PEM/OpenSSH private key in `D/ssh/<SLOT>` mode 0600; `<SLOT>_PATH` locates it. |
| `CERTIFICATE` | X.509 PEM certificate chain in `D/certs/<SLOT>.pem` mode 0400; `<SLOT>_PATH` locates it. A private key is separate material. |
| `OAUTH2` | A granted primary access token is exported as the value of its explicit consumer slot. Specialized provider login delivery remains adapter-specific; refresh tokens and MCP client companions are separate channels. |
| `ENDPOINT_URL` | Provider endpoint configuration, with separately encrypted optional authentication material. |

File aliases are exported to the adapter process and inherited by tool/child
processes. Explicit MCP plaintext env references remain a separate, policy-gated
channel; a generic file slot explicitly consumed by MCP uses `<SLOT>_PATH` for
its file. Reserved names and collisions refuse delivery. The diagnostic `.env`
map is not a shell setup step and must not be sourced: it would replace direct
token values with paths. Env/file delivery is readable by the agent process;
masking logs or UI does not make it inaccessible.

Before activating an upgrade, the candidate full-server CLI supports offline
`credential upgrade-preflight --database ...`. It opens retained SQLite data
read-only, reuses the complete resolver snapshot and the runtime file-alias
naming contract, and never decrypts values or applies migrations. Its report
covers potential metadata conflicts and operator-declared legacy Codex sandbox
configuration; incomplete metadata/configuration yields `UNKNOWN`, not runtime
readiness. See `docs/cli/credential.mdx` for the explicit configuration flags,
the machine-readable gate and the limits of MCP inspection.

The internal resolver marks a granted primary OAuth access token with
`oauth_primary`. MCP client IDs, client secrets and synthetic access-token
selectors do not receive this marker. An absent marker stays MCP-only; runtime
delivery does not infer a token's role from its slot suffix. Expired or malformed
cached grant leases and handle-only snapshots cannot enter native OAuth env
delivery. Invalid names, reserved runtime names and conflicting slot owners
refuse delivery.

Adding a new type:

1. Add the constant to `internal/api/credentials_types.go` and the case
   in `validateCredentialPayload`.
2. Add an explicit row to `internal/credpolicy/credpolicy.go` and extend
   the corresponding delivery plan/selectors; an unclassified type stays withheld.
3. Extend the type handling in
   `components/features/credentials/credential-form.tsx`.
4. Update the table above.

## Historical vault-type schema (DB migration v94)

```sql
ALTER TABLE credentials
    ADD COLUMN username TEXT;

ALTER TABLE agent_credentials
    ADD COLUMN mount_type TEXT NOT NULL DEFAULT 'env';
```

`credentials.username` is cleartext on purpose — usernames are
identifiers, not secrets. Bitwarden's vault encrypts only the password
half of a login record for the same reason: cleartext identifiers let
the UI search/sort without a per-row AEAD decrypt and shrink the GCM
surface area.

### Multi-part credentials (`credential_fields`)

A credential may carry any number of named parts on top of the two columns
above — `credential_fields (credential_id, key, value, encrypted_value,
is_secret, ordinal)`, PRD-CREDENTIALS-V2-2026 §2.2. `is_secret` decides which
value column holds the data, and a CHECK constraint makes exactly one of them
non-NULL, so a handler bug that wrote a secret into the cleartext column is
rejected by the engine rather than by the code under suspicion. Non-secret
fields (`region`, `account_id`, `host`) are cleartext for the same reason
`username` is.

Nothing was backfilled: `encrypted_value` and `username` stay authoritative for
every existing reader, and the field table holds only the *additional* parts.
The keys `value`, `password` and `username` are refused for that reason — two
writable copies of one datum with no owner drift apart silently.

**Delivery.** A field reaches the agent as `<SLOT>_<KEY UPCASED>`, where SLOT is
whatever the *credential* resolved to for that agent (explicit grant
`env_var_name`, binding slot, or the legacy `credentials.name`). The prefix is
never derived from the field, because the slot is what §2.5b uses to keep ten
accounts of one provider apart — a field with its own prefix would collapse
them back into one.

Names are resolved once, in `loadDeliveredCredentials`, over the whole delivered
set: every credential's slot is claimed before any field is considered, then
fields claim in delivery order. A derived name that is already claimed, is
reserved by the runtime (`HOME`, `PATH`, `HTTP_PROXY`, `CREWSHIP_*`,
`CLAUDE_CODE_OAUTH_TOKEN`, provider base URLs), is not a legal env var, or has
no slot to hang off, causes the FIELD to be dropped and a warning to be logged —
never a rename, never an overwrite. Fail closed: a missing `AWS_REGION` is
diagnosable, an `AWS_REGION` holding somebody else's value is not.

Fields ride as a sub-list on the delivered credential rather than as synthetic
sibling credentials. A sibling would inherit `type` and then be picked up as a
credential in its own right by the OAuth-token selector, the `USERPASS` file
layout and the sidecar CredStore's provider mapping. Secret fields are decrypted
through the caller's own opener (the same one that opened the credential's
value) and follow its channel; non-secret fields are delivered as cleartext env
vars regardless, since no channel here can carry an identifier.

User-facing docs: `docs/guides/credentials.mdx` → "Custom Fields".
Code: `internal/api/credential_fields.go` (storage/CRUD),
`internal/api/credential_field_delivery.go` (naming + collisions),
`internal/api/credential_delivery.go` (the chokepoint), migration
`internal/database/migrations/20260728135322_credential_fields.sql`.

`agent_credentials.mount_type` discriminates env-var injection (current
behaviour, default for backward compat) from in-container file mounts.
The existing `env_var_name` column is reinterpreted per `mount_type`:

- `env` → actual env var name (unchanged from pre-v93)
- `file` → basename inside the type-specific subdirectory; a helper env
  var `<NAME>_PATH` is auto-injected so the agent can locate the file
  without hardcoding the path convention

## Mount-time behaviour

`writeCredentialFiles` executes as agent UID/GID 1001, not root. The pure
`buildCredFilePlan` supplies both file writes and process path aliases. The
writer creates private directories, unlinks existing file entries, writes a
base64 round-trip, sets mode 0600 for SSH and 0400 for other files, and writes
a mode-0400 diagnostic `.env` map. Ownership follows its UID; no chown capability
is required. This does not isolate another process with that same UID.

Only SSH/certificate text receives CRLF-to-LF conversion and a missing final LF
at delivery, including legacy stored values. The stored value is unchanged;
opaque values and named fields remain byte-identical. Imports validate actual
SSH/X.509 parsing and reject unsupported binary/NUL/non-UTF-8 input. Certificate
primary values contain a certificate chain, not a private-key bundle.

After verified native file preflight, `DELIVERED` records the credential,
agent and run with authentication unknown. Audit failure prevents launch.
This event does not change `USE` or `last_used_at`; existing sidecar fetch USE
is crew-scoped and cannot prove which agent authenticated successfully.

For agent proxy requests with an authenticated current run, `PROXY_ADMITTED`
records the credential, agent, crew and active run after scope and budget
admission. Its fixed metadata describes the proxy channel, admitted stage and
unknown authentication. Audit persistence failure refuses admission. The event
proves neither forwarding nor upstream authentication and does not update
`USE` or `last_used_at`. Legacy requests without a run retain admission behavior
without an invented run attribution; crew-only requests cannot claim a run or
credential. A shared sidecar's boot run is never used as the current caller.

The Docker crew container is the current filesystem trust boundary. Agents
sharing UID 1001 can read each other's reachable delivered files. Separate
crews are required for mutually untrusted agents; product grant checks alone
are not same-UID isolation.

## Native CLI regression evidence

`TestNativeCLIShellEnvironment` drives actual Claude Code, Codex and OpenCode
shell tools with fresh homes and isolated local providers. Every adapter must
verify raw token delivery, exact opaque file/password bytes, native path aliases,
child-shell inheritance, standard pinned-host OpenSSH authentication, mTLS with
standard `curl`, and a synthetic USERPASS HTTP basic login through curl's stdin
configuration. A CLI token authenticates through curl's stdin header input.
The mTLS private key is the certificate's secret `key_pem` field;
the certificate primary is a valid X.509 chain. No password is passed to curl
as a process argument. The basic-login fixture uses a deliberately simple
synthetic value, not a general curl-configuration serializer.
The CI runner requires each adapter subtest to start and pass; a parent package
success, missing CLI, skipped subtest or final model response cannot satisfy it.
The CLI trio runs without root; the separate qualified Codex Docker fixture
retains its stricter runtime/artifact/kernel-control checks. No paid model or
production credential is used. These checks do not qualify every protocol,
tmux/routine lifecycle, Gemini runtime or a production Windows login.

`TestNativeVaultCLIToStandardSSH` separately follows the built Crewship CLI
through the actual API, encrypted SQLite Vault, grant, native IPC resolver,
writer and standard SSH authentication. It also imports a multiline secret
field through native CLI stdin, checks encryption/exact bytes/0400 mode and
authenticates with that field's native file alias. This closes the first-line
scanner truncation path without adding an onboarding API workaround.

## What this is NOT

- Not Bitwarden / Vaultwarden. No browser extension, no native client,
  no sharing, no copy-to-clipboard UX.
- Not HashiCorp Vault Enterprise. No dynamic secrets, no PKI engine,
  no transit, no namespaces — Crewship is the injection layer only.
- Not a password rotation system. SSH/cert/userpass rotation is manual
  today; the planned CLI token rotation pool
  (`project_cli_rotation_pool` in memory) is the only automated
  rotation, and only for AI provider tokens.

## Related code

- DB migration: `internal/database/migrate_consts_v94_credential_vault_types.go`
- Type enum + validator: `internal/api/credentials_types.go`
- API write paths: `internal/api/credentials_mutate.go`
- Internal resolve endpoint: `internal/api/agent_config.go` (`resolveAgentCredentials`)
- IPC bridge: `internal/chatbridge/resolver.go` (`credentialResponse`)
- Container mount: `internal/orchestrator/exec_sidecar.go` (`buildCredFileScript`)
- Tests: `*_test.go` next to each file above + `internal/database/migrate_v94_credential_vault_types_test.go`

The frontend wizard + list/detail update for the four new types is
tracked separately (follow-up PR after `feat/credentials-vault-types`).

## Credential retirement and audit history

Deleting a credential retains its row with `deleted_at` set. Live selection,
secret delivery, reveal, refresh and rotation exclude tombstones; forensic audit
reads retain access to their historical identity. Credential audit foreign keys
refuse a hard delete while history references the credential. Configured audit
retention and whole-workspace erasure remain separate operations. Hard workspace
erasure removes its credential history, including legacy rows whose audit
workspace attribution is NULL; soft deletion never performs this cleanup.

Reusing a retired name creates a new credential ID. Within the replacement
transaction, the old name gains ` [retired:<unchanged credential ID>]`; the full
original name remains before that exact suffix, without truncation. A name
collision or failed replacement rolls back the rename. Old assignments cannot
become grants to the replacement ID. Retirement preserves credential IDs and
historical audit rows, but irreversibly clears encrypted secret and refresh-token
values, OAuth configuration, account/username metadata, description, tags, error
and IP hints, custom fields and pending OAuth state/PKCE verifiers. The migration
also scrubs existing tombstones. Native guards apply the same erasure to later
retirement and historical restore/fork inserts, including child rows inserted
after their retired parent. Active credentials and their fields/state remain
unchanged. A refused scrub rolls back the retirement rather than reporting it
complete. Integration removal retires its unshared OAuth credentials and records
each REVOKE in the same transaction; storage, scrub or audit failure rolls back
the integration removal and its bindings.

For GDPR identity erasure, a retired credential's `created_by` becomes NULL;
its actor and approver attribution are cleared too. It needs no live custodian.
A live credential still requires a valid user custodian and keeps the existing
transfer-or-refuse behavior. Native constraints reject NULL creators on live
inserts/updates and reject reactivation of a creatorless tombstone. The nullable
creator keeps its users foreign key with NO ACTION and its ownership index.
Workspace normal/fork backups preserve nullable retired creators by column name,
including across the migration's changed column order, while keeping IDs and
audit bindings (or the ordinary fork ID mapping).

Workspace backups retain erased tombstones and workspace-attributed history;
legacy audit rows with NULL workspace attribution remain outside that scope. A replacement restore that would orphan
those rows is refused and rolled back, rather than deleting history absent from
the bundle. The separate active-secret export excludes tombstones.

## Keeper run attribution

Native Keeper execution reaches the crew sidecar before the server starts its
child command. That child receives the canonical upper/lower-case HTTP proxy
variables, loopback-only `NO_PROXY` and Node proxy opt-in, plus the selected
credential. Container-image HOME/PATH/tool defaults are preserved; the agent
shell's environment and other credentials are not forwarded. Body-selected,
assigned and derived credential names that replace platform runtime, loader or
proxy settings are rejected before approval consumption or secret retrieval.
Proxy names are recognized case-insensitively by their `_proxy` suffix. Provider
token names such as `OPENAI_API_KEY` remain valid for an explicitly gated child;
the main model adapter reserves its own authentication slots separately.

Having proxy variables is not proof of authentication or kernel egress
enforcement. The real-crew Keeper fixture checks actual standard-tool proxy
allow/deny behavior against an isolated service; its surrounding shared fixture
container is free-network, so restricted kernel enforcement still needs its own
runtime qualification.

The sidecar derives `requesting_run_id` from the verified current v2 calling-run
identity, never from the request body or its boot IPC run. Legacy identities
without a verified run leave attribution unknown and explicitly clear unrelated
inherited Journal run context while preserving request cancellation. The host admits new requests
against the durable journal start/terminal records for the same workspace, crew
and agent. Approval consumption, run admission and the new pending request and
ledger are committed in one transaction; failed admission or persistence rolls
back the spend. Both paths recheck after judging and record an unavailable run
as a system denial before optional lease issuance. Execute also rechecks run
liveness before container execution. A run may still end
concurrently with an already admitted container operation; this is not a claim
of an atomic kernel-level cancellation boundary.

Keeper requests have immutable nullable run attribution. Every ledger transition
copies it, including a later human/reference decision after the run has ended.
Admin `keeper requests` and `keeper history` JSON expose `requesting_run_id` when known.
Credential-bound non-pending transitions also append `KEEPER_DECISION` to Vault
Activity in the same transaction. Metadata contains only request/run identifiers,
decision, request type and actor attribution; no credential value, command, intent
or free-form reason. These events do not update `last_used_at` and do not prove
that a standard tool authenticated. Historical rows are not backfilled with
a guessed boot run.
