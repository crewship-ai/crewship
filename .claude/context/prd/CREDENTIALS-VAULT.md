# Credentials Vault

Crewship's credentials subsystem is a **runtime vault for AI agents**:
secrets are stored encrypted at rest, decrypted only inside the trusted
orchestrator, and mounted into the agent container as either env vars
or files at session start. The positioning is HashiCorp Vault OSS's
injection use case — *not* a general-purpose password manager. There is
no browser autofill, no sharing UI, no native client.

This doc covers the type system, storage shape, and mount behaviour as
of PR `feat/credentials-vault-types` (DB migration v93). Update it when
the type enum changes, when the mount path conventions change, or when
the encryption-at-rest path moves.

## Supported credential types

| Type             | Storage shape                                       | Mount target                                  | Why                                                                                |
|------------------|-----------------------------------------------------|-----------------------------------------------|------------------------------------------------------------------------------------|
| `API_KEY`        | `encrypted_value` only                              | sidecar reverse-proxy injects Authorization header | Anthropic / OpenAI API calls — secret never reaches the agent process              |
| `AI_CLI_TOKEN`   | `encrypted_value`                                   | sidecar proxy (same as API_KEY)               | Claude Code / Codex / Gemini CLI tokens with `sk-ant-oat-` style prefix             |
| `CLI_TOKEN`      | `encrypted_value`                                   | file at `/secrets/<agent>/<envvar>` mode 0400 | git PATs etc. — written to disk because the CLI tool reads it from env var path     |
| `SECRET`         | same as CLI_TOKEN                                   | same as CLI_TOKEN                             | legacy opaque secret type                                                          |
| `OAUTH2`         | `encrypted_value` (access token) + OAuth client cols | sidecar proxy + tokens.json                   | Linear, Google etc. — OAuth refresh flow lives in `internal/llmproxy/monitor.go`    |
| `USERPASS`       | cleartext `username` + `encrypted_value` (password) | 2 files `<envvar>_USERNAME` + `<envvar>_PASSWORD` mode 0400 | Gmail email+password, DB login — matches Bitwarden's `login.username` shape         |
| `SSH_KEY`        | `encrypted_value` = PEM body                        | `/secrets/<agent>/ssh/<envvar>` mode **0600** | OpenSSH refuses world-readable keys; 0600 mirrors `ssh-keygen` defaults             |
| `CERTIFICATE`    | `encrypted_value` = PEM body                        | `/secrets/<agent>/certs/<envvar>.pem` mode 0400 | mTLS clients, signing certs                                                        |
| `GENERIC_SECRET` | `encrypted_value` (opaque)                          | file like CLI_TOKEN                           | webhook secrets, signing keys, custom tokens — no shape validation                  |

Adding a new type:

1. Add the constant to `internal/api/credentials_types.go` and the case
   in `validateCredentialPayload`.
2. Add the mount branch to `buildCredFileScript` in
   `internal/orchestrator/exec_sidecar.go` (or document why the type is
   sidecar-proxy-only).
3. Add the wizard tile + branching in
   `components/features/credentials/add-credential-wizard/`.
4. Update the table above.

## Schema (DB migration v93)

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

`agent_credentials.mount_type` discriminates env-var injection (current
behaviour, default for backward compat) from in-container file mounts.
The existing `env_var_name` column is reinterpreted per `mount_type`:

- `env` → actual env var name (unchanged from pre-v93)
- `file` → basename inside the type-specific subdirectory; a helper env
  var `<NAME>_PATH` is auto-injected so the agent can locate the file
  without hardcoding the path convention

## Mount-time behaviour

`writeCredentialFiles` runs inside the agent container as root (UID 0),
delegating the script construction to the pure `buildCredFileScript`
function (test-covered in `exec_sidecar_credfiles_test.go`). For each
credential it:

1. `mkdir -p` the `ssh/` and `certs/` subdirectories at mode **0700**
   *before* any file write, so an unprivileged process can't race in
   and create the dir with a wider mode.
2. Writes each credential file via `echo '<base64>' | base64 -d > path`
   — base64 round-trip prevents shell interpretation of secret bytes
   (newlines in PEMs, quotes in passwords).
3. `chown 1001:1001` each file to the agent UID.
4. `chmod` to the type-specific mode (see table above).
5. Writes `/secrets/<agent>/.env` mapping each env var name to its file
   path — **the raw value never goes into an env var**, so nothing
   sensitive lands in `/proc/<pid>/environ`.
6. `chown 1001:1001 /secrets/<agent>` (non-recursive) so sibling agents
   sharing UID 1001 can't list each other's secret directories.

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

- DB migration: `internal/database/migrate_consts_v93_credential_vault_types.go`
- Type enum + validator: `internal/api/credentials_types.go`
- API write paths: `internal/api/credentials_mutate.go`
- Internal resolve endpoint: `internal/api/agent_config.go` (`resolveAgentCredentials`)
- IPC bridge: `internal/chatbridge/resolver.go` (`credentialResponse`)
- Container mount: `internal/orchestrator/exec_sidecar.go` (`buildCredFileScript`)
- Tests: `*_test.go` next to each file above + `internal/database/migrate_v93_credential_vault_types_test.go`

The frontend wizard + list/detail update for the four new types is
tracked separately (follow-up PR after `feat/credentials-vault-types`).
