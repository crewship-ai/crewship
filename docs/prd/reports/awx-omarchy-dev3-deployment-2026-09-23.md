# AWX/Omarchy release-1.0 work: dev3 deployment and live verification — 2026-09-23

## Deployed build

- dev3 URL: `https://crewship-dev3.unifylab.cz/` (service `crewship-ws@3`, API `localhost:8083`).
- Source: `origin/main` at `ddaeb33aeb146ae1501a6932d9ace8f9154708dc` plus the pre-existing dev3-only unsigned-webhook profile ported from `feat/webhook-unsigned`; deploy branch `deploy/dev3-awx-unsigned-20260923`, build commit `3725eba255a160d5b1b3a88e00617ad4233b2e03`.
- The additional profile includes its already-applied migration `20260916140000_pipeline_webhooks_unsigned_profile.sql`. This deploy branch is **not** a merge candidate for main without a separate product decision about that profile.
- Backup before restart: `/srv/crewship/dev3-pages-release/backups/awx-20260923T175608Z/` contains the SQLite online backup (`PRAGMA quick_check = ok`) and previous server/sidecar binaries. The server also made its own pre-migration snapshot in `/srv/crewship/crewship_3/`.
- Installed the newly built server and sidecar binaries atomically, then restarted `crewship-ws@3`. Four pending migrations applied; active schema is `20260922182944`. `crewship version --remote` reports clean commit `3725eba25` built at `2026-09-23T17:54:20Z`.

## Verification

Before deployment, the deploy source passed relevant Go tests (unsigned profile, credential dependents, reviewed-recipe hash), `go vet ./...`, `pnpm lint` (no errors), `pnpm test:types`, 117 selected Vitest tests, OpenAPI regeneration without an unexpected diff, migration lint, and `make build` (Next.js export plus both Go binaries). The separately recorded integration run against the mainline feature set is in `awx-omarchy-integration-2026-09-23.md` (9,304 frontend tests and affected Go packages).

On dev3 after restart:

- Public URL responded 200; systemd service was active, and its error-priority journal had no new entries after deployment.
- A temporary transform-only routine was saved with version 1. Running it with an intentionally wrong expected hash returned 409; running with the correct hash completed and recorded the expected version, definition hash, output, and correlated journal events.
- A temporary Page received panel data and executed its `call_routine` action. The resulting run completed with Page/panel/action provenance, version/hash, output, and correlated journal events.
- The same routine also completed when invoked with explicit version 1.
- Browser smoke test: the command palette found the Page and opened it. “Ask about this Page” opened a chat draft carrying Page context; no chat message was posted automatically.
- Both temporary Page and routine were deleted afterward and are absent from their list responses. Historical execution/audit records remain by design.

## Limits of live verification

A real `hlidac` agent invocation reached the Claude Code container, but its captured output showed repeated Anthropic `401 authentication_failed` retries. We stopped it after about three minutes; the run is `CANCELLED` with correlated journal entries, and all 17 agents returned to idle. This verifies run creation/cancellation and diagnostic capture, **not** a successful model response or full agent-run detail experience. The current Anthropic credential must be fixed before that last end-to-end path can pass. The `--plan` CLI request still launched the adapter with coding tools exposed; no tool call occurred in this test, so that observation alone does not prove a permission bypass.

No second-account live RBAC/revoke test was performed; the relevant automated authorization tests remain the evidence for that path. Existing sidecars in already-running agent containers may be reused until their containers restart, although the installed sidecar binary is from the same build as the server.
