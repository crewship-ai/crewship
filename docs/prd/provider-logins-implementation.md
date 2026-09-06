# Provider logins implementation — 2026-09-06

Execution checklist for `docs/prd/provider-logins.md` P-A through P-F, continuing
the session report and PR #2430. The session report remains untracked.

## Acceptance scope

- [ ] Integrate backend and device branches; reconcile split credentials with AuthDelivery.
- [ ] Codex: structured failures, API sidecar routing, auth file, refresh and expiry checks.
- [ ] Providers: real data, create/import, owner, assignment, refresh, device sign-in, Pays with.
- [ ] Claude, Cursor and Factory: declared env delivery and supported authentication modes.
- [ ] Gemini: split OAuth import, central refresh, rendered file and expiry handling.
- [ ] OpenCode: provider authentication file and revoke reconciliation.
- [ ] Pool selection, per-run cooldown, persisted quota state and explicit cross-owner opt-in.
- [ ] Paymaster attribution per credential; distinguish unknown historical attribution.
- [ ] Copilot: adapter, token delivery and device onboarding.
- [ ] Grok Build and Groq Code: adapters based on upstream command and output contracts.
- [ ] Model catalog and verified pricing.
- [ ] Full Go tests/vet, frontend lint/build/tests, documentation and migration gates.
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

The Groq CLI source inspected at
`build-with-groq/groq-code-cli/src/core/cli.ts` exposes an interactive Ink
application; `-p` means proxy, not a headless prompt. No native noninteractive
execution/JSON contract was found. P-F must not advertise a working native
adapter based on guessed flags. OpenCode's Groq provider is a possible
alternative, not evidence of native Groq CLI support.
