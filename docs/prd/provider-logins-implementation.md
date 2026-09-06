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
Full Go verification: `/tmp/provider-first-go.log`; vet: `/tmp/provider-first-vet.log`.
Deployment status is recorded after the dev3 restart, not inferred from hot reload.

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
