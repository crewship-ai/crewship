# Release-1.0 work: dev3 deployment and live verification — 2026-09-23

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

## Follow-up deployment and acceptance — 2026-09-24

- Built `201bb109e` from `main` at `f6b95e9d0` (including #2685), the release-status/CLI-help correction in #2688, and the existing dev3-only unsigned-webhook profile. This branch remains a deploy artifact, not a main merge candidate. `make build`, migration lint, and the unsigned-webhook API tests passed. On the corresponding mainline branch, `go test ./... -count=1 -p 4 -timeout 35m`, `go vet ./...`, and the documentation gates passed. Backup `/srv/crewship/dev3-pages-release/backups/awx-20260924T1112Z/` contains the previous binaries and a SQLite online backup (`PRAGMA quick_check = ok`). The restarted service reports clean commit `201bb109e`, schema `20260922182944`, and the public URL responds 200.
- **Current dev3 build:** after #2686 landed in `main`, its LLM proxy/run-identity fix was carried onto the same dev3 deploy branch as `fab10877d`. The new server and sidecar passed `make build`, `go vet ./...`, targeted auth/orchestrator/sidecar tests and migration lint. Backup `/srv/crewship/dev3-pages-release/backups/awx-20260924T112942Z/` contains the preceding binaries and a healthy SQLite snapshot. The restarted server reports clean commit `fab10877d`, schema `20260922182944`, and the public URL responds 200. The idle `coolify` crew container was recreated to pick up the new sidecar. An OpenCode/Z.AI run after this final restart answered `2 + 2` with `4` and completed as `msg_1790249492786045665_cdba7b2448914b3a`; the fleet returned to 17 idle agents. Its temporary chat was deleted.
- A newly invited MEMBER account on dev3 received no Page access before a grant (404); after `read` it received the Page but could not edit (403); after `write` it edited successfully; after revoke it received 404 again. The sequence passed both before and after the new binary was installed. No workspace-wide seed was run.
- An OpenCode/Z.AI agent completed run `msg_1790248511002388957_4461602e254f88ed`; its `GET /runs/{id}` detail and run-filtered journal showed the completed agent run, and `/activity?run=…` rendered it with a Copy evidence control without browser errors. OpenCode does not stamp Claude-specific model/session-init fields, so those remain absent in its run detail.
- A Page chat handoff to the same agent completed run `msg_1790248765223187865_4d1d61edaef6a18c`; the persisted user message contained server-derived `page_context` with Page ID, slug, name, workspace and snapshot time. After the agent's Page grant was revoked, another send showed a refusal; no message persisted and no agent run started.
- The first OpenCode attempt after restart failed because a reused crew container held an older sidecar. `crewship crew restart-agents coolify` recreated that idle crew's container; the next two runs passed. This is a release procedure requirement when the sidecar binary changes. For the only other dev3 team container, `restart-agents` reported no running agent container; its separate service container was left intact.
- The temporary Page, four QA chats, and test user's workspace membership were removed. The global test account row remains without workspace membership because the workspace-scoped GDPR command does not delete global accounts; its password, setup link and CLI token files were removed from `/tmp`. Historical run/audit records remain.

Remaining limits: the stored `ANTHROPIC_API_KEY` is `EXPIRED`, so the Claude Code path has not passed a successful live model invocation. `routine.run` does not yet authorize Page action/replay/schedule-run for a MEMBER; broadening it requires an explicit product/security decision. O3 and O5 remain outside the implemented ten-iteration package.
