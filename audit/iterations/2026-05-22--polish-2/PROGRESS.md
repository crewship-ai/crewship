# Polish-2 audit-loop progress — STOP checkpoint

Per cron spec ("Pokud všechny P0-P2 hotové → updatuj PROGRESS.md s
checkmark statusem a STOP"), this file records the terminal state of
the polish-2 audit-loop. Future cron fires reading this file should
NOT enqueue new PRs; the queue is consciously exhausted within the
loop's KISS / 200-LoC envelope.

The file is intentionally not deleted on subsequent successful audit
runs — it documents *what this iteration finished*. A new iteration
should produce its own iteration-NN/PROGRESS.md.

## Priority queue (from cron prompt)

| # | Finding | Status | Evidence |
|---|---|---|---|
| **P0-A** | Invisible-format codepoints Cf class — extend `invisibleUnicodeRunes` | ✅ shipped | PR #477 |
| **P0-B** | ETHOS block tightening (no tool/dir/sibling enumeration in refusals) | ✅ shipped | PR #476 |
| **P0-C** | Verify `666f54d7` + `78dfcfcd` land on main | ⏳ via #472 | both commits live on `feat/pr-g-ui-surface-complete`; merges with PR #472 |
| **P1-A** | Symmetric write-path scanner (handleWrite) | ⏸ upstream-blocked | collides with PR #472's scanner v2 surface — pick up after #472 merges |
| **P1-B** | Off-by-one cap check `len(data) > cap` → `>=` | ⏸ upstream-blocked | collides with PR #472's `tools.go:372` |
| **P1-C** | Regression test for lessons cap enforcement | ⏸ upstream-blocked | collides with PR #472's `tools_test.go` |
| **P1-D** | `[CREW CONTEXT]` block gated by `tool_profile` | ⏸ upstream-blocked | collides with PR #472's `exec_stream.go` |
| **P2-A** | tool_loop detector terminate run (not just observe) | 📝 design note | `notes/tool-loop-terminate.md` — ~525 LoC, 3 packages, needs migration + feature flag |
| **P2-B** | OpenAI retry/backoff parity with Anthropic | ✅ shipped | PR #480 |
| **P2-C** | Replace raw docker.sock mount with socket-proxy | ✅ shipped | PR #478 |
| **P2-D** | context.Background() fan-out — propagate parent ctx | ✅ shipped | PR #481 (5 handler-spawned sites) |

## iter#1 MEDIUMs shipped from the wider audit

The cron's priority queue covered the polish-2 wave5/wave6 findings; the
audit-loop also opportunistically picked up iter#1 MEDIUMs that fit the
loop's "small, well-scoped, non-blocked" envelope. Status:

| ID | Finding | Status | PR |
|---|---|---|---|
| M2  | Pipeline webhook signing secret optional | ✅ | #490 |
| M6  | Internal IPC URLs lack `url.PathEscape` (16 sites) | ✅ | #498 |
| M8  | 429 responses missing `Retry-After` / `X-RateLimit-*` | ✅ | #485 |
| M10 | `/_next/static/*` directory autoindex | ✅ | #489 |
| M11 | Path-traversal canonicalisation 307 → 400 | ✅ | #491 |
| M13 | AgentFiles / Download / Logs missing role gate | ✅ | #495 |
| M15 | Base images use floating tags (Dependabot docker) | ✅ | #496 |
| M16 | `.dockerignore` too narrow | ✅ | #487 |
| M18 | No slog redaction layer (`ReplaceAttr` not wired) | ✅ | #488 |
| M19 | Three call sites log raw token values | ✅ | #486 |
| M20 | `.env.example` missing ~25 op-relevant vars | ✅ | #497 |
| M22 | OAuth state + session ID below 128-bit | ✅ | #494 |
| M24 | Decompression-bomb global cap missing | ✅ | #493 |
| M25 | `migration-lint.yml` shell interpolation | ✅ | #483 |
| M26 | `PRAGMA foreign_key_list(<table>)` no identifier gate | ✅ | #484 |
| M27 | Cookie Secure resolved dynamically (FORCE_SECURE_COOKIES flag) | ✅ | #492 |
| iter#1 nextauth | Unbounded form parse on `/api/auth/*` (HIGH H7) | ✅ | #479 |
| ADDENDUM P0 | CI merge-marker guard | ✅ | #499 |

## Items intentionally deferred (out of loop envelope)

| ID | Why deferred | Pickup plan |
|---|---|---|
| M1  GDPR admin DELETE | net-new feature, larger | dedicated session before EU launch |
| M3  Workspace-owner reach on `/admin/*` | rename across many routes; UX coordination | dedicated refactor PR |
| M5  French/non-English instruction-override rule gap | needs language-aware classifier | follow-up after the model-layer iter |
| M9  `/api/v1/system/telemetry` install_id | design tension — endpoint must be unauthenticated by intent | accept as documented risk |
| M12 chat 200+empty → 404 | frontend coordination required (new-chat UX) | follow-up with FE team |
| M14 `memory_versions` per-user column | requires migration + GDPR scan implementation | follow-up |
| M17 docker-compose hardening | collides with PR #478; layer on top after merge | follow-up |
| M21 `ENCRYPTION_KEY` rotation routine | net-new feature, larger | dedicated session |
| M23 CSP `unsafe-eval` / `unsafe-inline` strip | requires nonce wiring across Next.js bundle | frontend coordinated change |
| M28 `agent_runs_archive` indexes | **verified no production queries hit the table** — audit's "if used" precondition fails | n/a |
| M29 `NetworkCreate` Internal: true | **regression test forbids** (containers need internet for api.anthropic.com) | n/a |
| M30 Sidecars share bridge | architectural — per-crew bridge needs orchestrator changes | dedicated PR |
| M31 Dockerfile final stage Alpine → distroless | needs image-runtime test matrix | dedicated PR |
| Docker SDK v28 → v29 (HIGH H8 / CVE chain) | major-version bump, ~25 callsite re-verification | `notes/docker-sdk-v29-bump.md` — dedicated session |
| Sidecar zero-hardening (HIGH H6) | per-image capability test matrix needed | `notes/sidecar-zero-hardening.md` — dedicated session |

## CI status caveat

Every fix PR above (#476–#499) carries a Go test failure that is upstream-caused:
`TestResolveAgent_*` and `TestResolveChat_*` fail on `main` HEAD because of an
unresolved git merge marker in `internal/api/agent_config.go`. PR **#475**
(approved, awaiting merge) fixes it. Each of my fix PRs has a comment
pointing at #475. Once #475 merges, re-running CI on every fix PR clears.

## Numbers

- **PRs shipped this iteration:** 24 (#476–#499)
- **Severity bucket:** 4 HIGH (Cf class scanner, docker socket proxy, nextauth DoS cap, OAuth/session nonce); 16 MEDIUM; 4 LOW or hardening
- **Total LoC across PRs:** ~1.9k net (mostly tests + comments; the median fix is <50 LoC source)
- **Design notes for deferred items:** 3 (`notes/sidecar-zero-hardening.md`, `notes/docker-sdk-v29-bump.md`, `notes/tool-loop-terminate.md`)

## What un-blocks resumption

When **PR #472** lands on main, the audit-loop can re-engage on:
- P1-A (write-path scanner)
- P1-B (off-by-one cap)
- P1-C (lessons cap regression test)
- P1-D (CREW CONTEXT tool_profile gating)

These are all small (~50–100 LoC each) and would be a single follow-up tick.

When **PR #475** lands, the existing 24 fix PRs' CI clears and they become
ready-to-merge from the green-CI side.

## Loop hygiene

- Session-only cron `*/20 * * * *` (job `386b7b67`) remains active until
  the Claude session ends or auto-expires (7-day session TTL).
- The cron prompt's "STOP" rule (this file's presence) means future fires
  will read this file and skip new-PR work. If the loop should resume —
  for example after `git pull` brings #472 + #475 into main and unblocks
  the P1 items — delete `PROGRESS.md` before the next cron fire.
