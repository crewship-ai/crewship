# Polish-2 follow-up — status checkpoint

Running record so successive audit-loop sessions don't re-discover
state. Updated whenever a PR ships or a finding is consciously
deferred.

## PRs shipped from the loop

| # | Title | Source finding | Severity |
|---|---|---|---|
| #476 | `fix(api): ETHOS block forbids tool/dir/sibling enumeration in refusals` | wave5/a5-2 #1+#2 | LOW+MEDIUM |
| #477 | `fix(memory): scanner blocks invisible-format (Cf) codepoint evasion` | wave5/a5-1 #1 | **HIGH** |
| #478 | `fix(docker): broker prod docker.sock through filtering proxy` | iter#1 REPORT | **HIGH** |
| #479 | `fix(api): cap nextauth credential callback body at 16 KiB` | iter#1 REPORT | MEDIUM (DoS) |
| #480 | `fix(llm): mirror Anthropic retry/backoff on OpenAI provider` | wave6/a6-1 #4 | LOW |
| #481 | `fix(api): propagate request ctx into handler-spawned goroutines` | iter#1 REPORT | MEDIUM |

All six branch from `main`, lint-clean locally. Several have **Go
test failures upstream**: pre-existing SQL merge-marker bug in
`internal/api/agent_config.go` that PR #475 (hotfix) is open to fix.
The failing tests (`TestResolveAgent_*`, `TestResolveChat_*`) fail
identically on `main` — **not** introduced by these PRs.

## Blocked on upstream PRs

These were on the priority queue but collide with files modified in
the open umbrella PR #472 (`feat/pr-g-ui-surface-complete` — PR-G/PR-F
roll-up). Picking them up now would create cherry-pick / rebase
churn for both sides.

| Priority | Finding | Blocked on |
|---|---|---|
| P1-A | Symmetric write-path scanner (wave5/a5-1 MEDIUM #2) | #472 — `tools.go` collision |
| P1-B | Off-by-one cap check (wave6/a6-1 M01) | #472 — `tools.go:372` collision |
| P1-C | Lessons cap regression test (wave6/a6-1 M06) | #472 — `tools_test.go` collision |
| P1-D | `[CREW CONTEXT]` block gated by `tool_profile` (wave5/a5-2 #1) | #472 — `exec_stream.go` collision |
| P0-C | Land 666f54d7 + 78dfcfcd (lessons hard-reject + scanner NFKD) | #472 — those commits live on #472's branch |

Action when #472 merges: rebase main, pick up P1-A through P1-D in
order. Each is small (≤ 100 LoC) and follows the PR template the
shipped six use.

## Deferred — design notes written

| Finding | Note path | Why deferred |
|---|---|---|
| Sidecar zero-hardening (iter#1) | `notes/sidecar-zero-hardening.md` | ~350 LoC + per-image integration test matrix |
| Docker SDK v28→v29 CVE chain (iter#1) | `notes/docker-sdk-v29-bump.md` | Major version bump across ~25 callsites + mock updates |
| Tool-loop terminate (wave6/a6-1 #2) | `notes/tool-loop-terminate.md` | ~525 LoC, crosses three packages, needs migration + feature flag |

Each note documents the safe scope, the breaking-change risk, and
the exact next-session steps. Pick up any of these in a dedicated
session, not a loop tick.

## Not yet attempted

- GDPR admin DELETE `/api/v1/admin/users/{id}/data` (iter#1
  REPORT) — net-new feature, not a bug fix; lives outside the audit
  remediation scope.
- `.env.example` refresh to cover the 72 env vars the code actually
  reads vs. the ~20 documented (iter#1 follow-up note) — docs/UX
  change, low priority.
- Hybrid-search wire-up retest (iter#1 decisions for next iteration)
  — not a fix, an audit-rerun task.
- Race tests against the full `internal/api/...` (iter#1 decisions)
  — same.

## Loop hygiene

- Cron `*/20 * * * *` (job `386b7b67`) is the wake driver. Fires
  only when REPL is idle; queues otherwise.
- Each fire is a fresh session — bootstraps from `git status`,
  `gh pr list`, this STATUS.md, and the priority queue baked into
  the cron prompt.
- `PROGRESS.md` is intentionally NOT written yet. Per cron-prompt
  spec, writing it triggers STOP. We're not done — there's still
  upstream-blocked P1 work plus net-new tasks.
