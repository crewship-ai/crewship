# Agent credential validation — 2026-09-08

Scope: user requested an isolated dev3 crew/devcontainer with PostgreSQL,
agent-created credentials, inbox approval/supply, frontend creator attribution,
and subsequent use/recall. This is a partial validation, not acceptance.

## Environment and blocker

- Clone/dev3 HEAD: `3fd5376753669ba6353e2299f42f0b5f72d0fd56`.
- `./dev.sh status`: Go 8083 and Next.js 3013 running; `/api/health`: ok.
- Docker responds, server version 29.3.0.
- `/tmp/crewship-3-dev --server http://localhost:8083 whoami --format json`
  returns HTTP 401 `session_invalid`.
- No authentication bypass, database writes, credential extraction, new crew,
  container or paid model run was performed. Existing user WIP is preserved.
- Requested CLI reauthentication and identification of the authorized model
  account/test budget. Live acceptance requires these prerequisites.

## Executed tests

Both commands used fresh `TMPDIR` directories under `/dev/shm` and exited 0:

```bash
go test ./internal/api ./internal/manifest ./internal/sidecar -run 'Test.*(CredentialProposal|PendingCredential|CredentialAsk|CredentialSupply|ResolveCreateAttribution|Attribution|AutoManaged|Postgres|StockDatastore|CredentialCreate|CallerSignature|CredentialsV98)' -count=1 -timeout 10m -json
go test ./internal/api -run 'Test(SupplyEscalationCredential|WaitForEscalation_Credential|CreateEscalation_InboxPayload|EscalationApprove|AutoAssign_ExcludesPending|RedactedMetadata)' -count=1 -timeout 10m -json
```

61 top-level tests, 91 passing test/subtest records, zero failures or skips.
Logs: `/tmp/credential-agent-check.jsonl` and
`/tmp/credential-inbox-check.jsonl`. Includes proposal staging/approval/rejection,
human supply, no-value inbox/wait responses, attribution checks, signed caller
guards and manifest generation/reapply. The manifest test named EndToEnd uses
a fake API client: it does NOT prove a running PostgreSQL or an actual AI run.
No full Go suite was rerun in this diagnostic turn; no product code changed.

## Findings from code inspection

1. Agent proposals persist `created_by_actor_type=agent` and the originating
   agent ID in `internal/api/escalation_credential.go`. API credential list/detail
   exposes actor type/ID (`internal/api/credentials.go`).
2. Current frontend `app`, `components`, `hooks`, and `lib` contain no consumers
   of `created_by_actor_type`, `created_by_actor_id`, or
   `provisioned_for_service`. The credential detail does not declare these fields.
   Thus the requested clear creator/source display is missing in this checkout;
   no authenticated browser validation was possible.
3. Automatic service credentials use `created_by_actor_type=system`, not the
   lead agent, in `internal/manifest/plan.go`. The lead-agent attribution comment
   in `auto_managed.go` is stale. Distinguish generator from requesting agent.
4. Security gap: auto-managed values are encrypted in the credential row but
   are also copied into literal service environment/command configuration
   (`auto_managed.go`), and service JSON is stored by crew create/update handlers.
   Do not describe this path as exclusively encrypted secret storage. No real
   secret was read to establish this finding. Keep remediation details internal.

## Pending live acceptance

After authorized login: use a uniquely named, isolated test crew and private
PostgreSQL service (no host-published port); verify vault metadata/source and
actual database connectivity; exercise both an agent-generated proposal and a
human-supplied request through inbox; inspect browser attribution; repeat a run
to check access survives without disclosing the value. Memory should record only
credential reference, service and purpose, never secret material. Record exact
test resource IDs and agree cleanup/retention. Until then this is NOT a completed
live crew, PostgreSQL, inbox, or memory acceptance test.
