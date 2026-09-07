# Provider logins implementation — 2026-09-06

Execution checklist for `docs/prd/provider-logins.md` P-A through P-F, continuing
the session report and PR #2430. The session report remains untracked.

## Acceptance scope

### Provider-first UI follow-up

User's 2026-09-06 screenshots exposed provider selection hidden in the decorative
brand picker and a disappearing empty Providers rail. The active wizard now
offers 12 API-supported provider cards first, then provider-specific inputs and
supported methods. Brand SVGs for Grok/Groq/Kimi/Z.AI/MiniMax use attributed MIT
LobeHub paths. The overview and Providers rails expose counts including zeroes,
plus an all-providers reset. No account is inferred from a secret's display name.

Verification: 520 Vitest tests across 20 files, two isolated Playwright browser
flows at 1280px and 390px (API mocked, no real credentials created), production
build and lint (33 existing warnings, no errors). The browser test pins global
navigation before clicking the vault rail, since its hover overlay can cover it.
Full Go verification passed (exit 0): `/tmp/provider-first-go.log` (API 745.573s,
database 778.035s); vet passed: `/tmp/provider-first-vet.log`.
Deployed only dev3 using `systemctl reload crewship-ws@3`: build
`97df97d7`, 2026-09-06T15:36:30Z. Service is active, `/api/health` says `ok`,
and the public `/credentials` route returns HTTP 200. The binary reports dirty
because the existing unrelated AGENTS.md/CODEX.md/session-report WIP is retained.
No push, demo seeding or real credential creation was performed. Existing-account
counts were not authenticated through the CLI (its session returned 401);
the user's signed-in browser must confirm those data against the updated backend.

### Original implementation checklist

- [x] Integrate backend and device branches; reconcile split credentials with AuthDelivery.
- [ ] Codex: structured failures, API sidecar routing, auth file, refresh and expiry checks.
- [ ] Providers: real data, create/import, owner, assignment, refresh, device sign-in, Pays with.
- [ ] Claude, Cursor and Factory: declared env delivery and supported authentication modes.
- [ ] Gemini: split OAuth import, central refresh, rendered file and expiry handling.
- [ ] OpenCode: provider authentication file and revoke reconciliation.
- [ ] Pool selection, per-run cooldown, persisted quota state and explicit cross-owner opt-in.
- [x] Paymaster attribution per credential; distinguish unknown historical attribution.
- [ ] Copilot: adapter, token delivery and device onboarding.
- [ ] Grok Build and Groq Code: adapters based on upstream command and output contracts.
- [x] Model catalog and verified pricing (conservative tier estimate; see below).
- [x] Full Go tests/vet, frontend lint/build/tests, documentation and migration gates.
- [ ] Browser acceptance on dev3; live provider tests only where credentials and quotas permit.

## Integration findings

The backend branch stores PROVIDER_LOGIN values as access tokens plus encrypted
parts. The device branch's AuthDelivery renderer expects a legacy JSON blob.
Both representations must be handled before the branches can operate together.
The backend refresh implementation also needs provider-specific schedules and
review of claim atomicity, stale-token reads and failure handling.

The external wireframe link could not be retrieved. Existing Providers, detail,
assignment and wizard components plus PRD section 6 provide the available design
baseline; visual comparison against the external artifact remains unverified.

App-server migration is explicitly outside this PRD (section 8.5).

## Paymaster increment (030cc8a2, after integration commit 8d58ec36)

The ledger now carries an optional `credential_id`. Historical rows remain
unattributed; equal plan names never merge accounts whose identities are known.
Codex, Claude and Gemini subscription result envelopes are recorded by the
orchestrator with the selected grant's identity, without marginal dollar cost.
API-key requests carry the credential selected by the sidecar through the
internal cost endpoint, which checks workspace ownership. Paymaster displays
login names from its metadata response and an explicit unknown label for old
rows. This does not yet implement quota windows, pooled selection or new native
CLI adapters.

The CLI subscription view now consumes the actual API field names
(`subscription_plan`, `last_ts`) and includes `credential_id` in human and
machine output. Its old fixtures used different names and masked blank
plan/last-used columns. The UI also includes configured subscription seats
with no observed usage and owner metadata, without inventing a quota balance.

Refresh persistence (79c841d1) rejects results for a changed refresh-token ciphertext
or a deleted login. A stale failure cannot mark the replacement login as
failed. Regression tests exercise both re-import and revocation. This does
not solve the separate limitation of a CLI caching its access token in memory.

Opaque Claude setup-tokens no longer imply a Max plan. The environment and
new ledger events say `Claude (plan unknown)` unless plan metadata was supplied.
Existing historical ledger labels are not rewritten.

## P-D foundation in progress — #2440 (2026-09-07)

`internal/providerpool` adds the metadata-only selection/storage foundation:

- A pool is a named set of accounts, **not a grant**. Creating it does not
  create or alter `credential_bindings`; ordinary scope/slot uniqueness stays.
- Lowest numeric priority first, then least-recently-selected sequence, with
  account ID as a stable tie-breaker. A SQLite write transaction reserves each
  turn; no in-memory cursor is lost on restart. Snapshot reads do not rotate.
- Expired, revoked, cooling-down and intervention-blocked accounts cannot win.
  An entirely unavailable pool returns an error, never the first cooled account.
  Refresh must finish outside the selection transaction and before selection.
- Provider, billing mode and owner are revalidated at every selection. Mixed
  owners require explicit per-pool consent, even when one member is inactive.
  Legacy subscription blobs need importing as `PROVIDER_LOGIN` before pooling;
  their encrypted expiry cannot safely be inferred by this metadata-only store.
- Typed observations persist cooldown deadlines or billing/authentication blocks;
  no raw upstream message, token or invented quota percentage is stored.
  Stale observations cannot shorten a deadline. Clearing uses a revision check
  and retains a tombstone so an old success cannot clear a newer failure.
  Observations also carry a fingerprint of the encrypted access material used
  by the request, checked transactionally; a late failure after refresh or
  re-import cannot block the replacement token. The store hashes ciphertext
  for this provenance check, but never decrypts or returns secret values.
- Membership has tenant checks in both the reader and database triggers,
  including parent workspace changes. Pool definitions/members are included in
  backup; instance-local availability observations are excluded from restore.

**Not wired or shipped as a user feature yet:** pool CRUD/RBAC endpoints,
CLI/UI management, scope/slot pool bindings, run-start authorization/selection,
sidecar grantee reconciliation, provider-event observation producers and measured
quota windows. The store is an internal foundation; it does not establish that
pooling or automatic failover works in dev3. No running agent is switched or
automatically replayed by this change.

Integration boundary for the next slice: despite its name,
`loadDeliveredCredentialsForRun` is also used by the agent-configuration resolver.
Calling `Choose` there would consume turns during configuration loads and would
not guarantee selection for each actual run. Wire authorized selection at the
orchestrator's run boundary, before sidecar/environment/auth-file construction.
Native auth files currently live in the agent's shared HOME; overlapping runs
must not rewrite that file with different pool accounts. Solve that lifecycle
constraint (including detached execs) before enabling native pool bindings.
Read-only payer metadata must identify a pool, not claim its next candidate is
the credential paying for an already-running task.

Verification entry points: `go test -race ./internal/providerpool -count=1`
(selection, concurrent transactions, restart, tenant guards, stale observations),
`go test ./internal/backup -count=1`, and the full Go verification loop. Record
the actual results in the PR rather than treating these commands as evidence
that they have run.

Rate limits are not necessarily per key: OpenAI API limits can be shared by an
organization/project and model family. A different key is not extra quota.
See [OpenAI rate limits](https://developers.openai.com/api/docs/guides/rate-limits).

## Remaining design constraints

P-D is not just wiring existing selection helpers. PRD section 5.4 assumes
several bindings can share a scope and slot, but the shipped unique index
`idx_credential_bindings_slot` in migration `20260728135240` prevents that.
Ordinary secret-slot uniqueness must be preserved while adding an explicit
provider-login pool representation. Native file selection must stay per-run;
cross-owner pooling must require opt-in. No automatic quota-bypass retry has
been introduced. The quota view remains null where no provider measurement
exists; a minute-level rate-limit header is not a measured five-hour seat window.

Copilot OAuth onboarding additionally needs an appropriate configured GitHub
OAuth application; the official SDK setup guide describes an application's own
client ID, not a public client ID to guess or borrow. Native Copilot/Grok adapters,
their tool isolation and container installation remain outstanding. Groq's
noninteractive contract still needs a decision (see source finding below).

Primary-source starting points for P-F:

- [Copilot CLI reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference):
  `-p`, `--output-format=json` (JSONL), `--available-tools` and `--deny-tool`.
  `COPILOT_GITHUB_TOKEN` has precedence over GH_TOKEN/GITHUB_TOKEN; classic
  `ghp_` PATs are explicitly unsupported. The remote-disable flags have an
  account-feature caveat, so they need binary-level verification before pinning.
- [Copilot SDK event definitions](https://github.com/github/copilot-sdk/blob/main/nodejs/src/generated/session-events.ts):
  typed `assistant.message` and `assistant.usage` envelopes have a `data` field.
  This is a parser research lead, not proof that CLI JSONL is identical.
- [Grok headless reference](https://docs.x.ai/build/cli/headless-scripting):
  `-p`, `--output-format streaming-json`, and `--no-auto-update`. Its stream
  contract must be read from Grok's implementation, not a Claude parser alias.

Neither `copilot` nor `grok` was found on this host's PATH during this checkpoint.

### Grok binary-only probe (2026-09-07)

The official [installer source](https://x.ai/cli/install.sh) resolves the stable
artifact to `https://x.ai/cli/grok-1.0.13-linux-x86_64.gz`. The installer was
**read, not executed** (it changes shell configuration and reads local auth).
Downloaded/decompressed binary SHA-256:
`edf79521581bb5e6b95abef848491a6a742e860da3e237ebe86a280d30dce4c1`.

In a disposable Alpine container with no network, no credentials, read-only
rootfs, dropped capabilities and UID 1001, it reported
`grok 1.0.13 (5e9a58528b76)`. `--no-auto-update --no-memory --help` exited 0;
an invented flag before `--help` exited 2 (negative control). Its own help says
`streaming-json` is ACP-session-update NDJSON, distinct from the separate
`streaming-messages-json` format. The unauthenticated headless invocation emitted
a JSON `type: error` / `message` envelope and exited 1, not a successful run.

Local evidence: `/tmp/grok-1.0.13-help.log`, `/tmp/grok-1.0.13-flags.log`,
`/tmp/grok-1.0.13-control.log`, `/tmp/grok-1.0.13-offline-stream.log`.
This verifies binary/flag/error-shape compatibility only. Successful message,
tool and usage streams, MCP/tool isolation, authentication, billing and an
installed Crewship adapter remain unverified/unimplemented. No real provider
account or paid model call was used.

## Integration verification checkpoint

- Device login completion now creates a split/sealed `PROVIDER_LOGIN`, not a
  legacy JSON credential. Generic file delivery accepts both representations.
- Google OAuth import and refresh have isolated fake-endpoint tests. Google
  uses a short-token refresh schedule; no real Gemini subscription run has
  been demonstrated. Updating a file does not update a CLI's in-memory token.
- Read-only credential resolution no longer triggers refresh. Boot and
  delegation use a separate loader; access and companion parts are read from
  one SQL snapshot. Expired refreshable logins fail closed on refresh failure.
- OAuth error messages only retain known protocol codes, never provider free
  text that might reflect submitted secrets.
- Codex nested `turn.failed.error.message` and top-level `error.message` are
  parsed. File preflight failures are checked for every declared file adapter,
  including a failure in an earlier batch flushed by a read probe.
- OpenCode now renders its API-key `auth.json` from current grants. Routed
  providers receive only a dummy; handle-only and subscription secrets are
  excluded. The file is cleared on unassignment and removed on revoke across
  direct grants, bindings and crew links. Container-level live acceptance is
  still outstanding.
- Full Go run exposed missing backup classification, route-role inventory,
  OpenAPI required fields and poller lifecycle accounting. These gates were
  corrected and passed targeted checks. The first full run was interrupted
  with SIGQUIT for diagnosis after 624 seconds in internal/api; it is NOT a
  passing verification. The rerun log is
  `/tmp/crewship-provider-logins-go-test-final.log` (check completion).
- Credentials/UI tests: 28 files, 539 tests passed. `pnpm build` passed;
  `pnpm lint` passed with 33 warnings; `go vet ./...` passed before the latest
  preflight regression test. No deploy or successful live-provider execution
  is implied by these checks.
- The second complete Go invocation finished: internal/api passed (701.693s),
  but database FK-index policy, stale model snapshot and quoted OpenAPI
  figures failed. The missing index was added in an append-only migration;
  the models.dev snapshot was regenerated, and the API schema/documentation
  were corrected. Targeted reruns of all three failing gates passed.
  The latest complete orchestrator run passed (23.790s); another complete
  internal/api run is at `/tmp/crewship-provider-logins-api-final.log`.
- GPT-6 Astra pricing comes from the refreshed snapshot, including its
  >272K tier. Paymaster's existing context-free estimator uses the upper tier;
  this is conservative estimation, not exact per-context invoicing. The old
  DeepSeek snapshot sentinels were replaced with current V4 model identifiers
  returned by the same upstream snapshot.

### Latest verification (Paymaster increment)

- `go test ./... -count=1 -timeout 30m` in
  `/tmp/crewship-provider-logins-complete-go.log` finished with API passing
  (763.303s) and database passing (793.184s), but failed old CLI DeepSeek
  sentinels and the OpenCode environment golden (the new XDG_DATA_HOME pin).
  Both test assumptions were corrected; this invocation remains a failure.
- Complete CLI rerun passed (222.391s):
  `/tmp/crewship-provider-logins-cli-complete.log`.
- Complete orchestrator rerun passed (29.674s):
  `/tmp/crewship-provider-logins-orchestrator-verified.log`.
- Provider-login regression tests including stale refresh persistence passed
  (3.229s): `/tmp/crewship-provider-logins-refresh-cas.log`.
- Credentials + Paymaster UI selection: 19 files, 407 tests passed;
  `/tmp/crewship-provider-logins-ui-complete.log`.
- Latest frontend build passed; lint passed with 33 warnings and no errors.
  `go vet ./...` passed at `/tmp/crewship-provider-logins-final-vet.log`.
  Migration lint and agent invariants passed.
- The intermediate `/tmp/crewship-provider-logins-verified-go.log` invocation
  was stopped as obsolete after later fixture fixes; it is not a pass.
- Final `go test ./... -count=1 -timeout 30m` passed with exit 0:
  `/tmp/crewship-provider-logins-final-go.log`. API 700.207s, database
  707.616s, orchestrator 39.984s. This includes the final runtime code and
  corrected fixtures in 22f44a4b. Documentation-only checkpoint follows.

The Groq CLI source inspected at
`build-with-groq/groq-code-cli/src/core/cli.ts` exposes an interactive Ink
application; `-p` means proxy, not a headless prompt. No native noninteractive
execution/JSON contract was found. P-F must not advertise a working native
adapter based on guessed flags. OpenCode's Groq provider is a possible
alternative, not evidence of native Groq CLI support.
