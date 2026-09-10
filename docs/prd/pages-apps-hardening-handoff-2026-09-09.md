# Pages Apps — current delivery handoff, updated 2026-09-10 11:15 UTC

## Delivery correction — 2026-09-10 12:40 UTC

This update supersedes earlier pending-CI and Safari-decision statements below.
Source run [34469301690](https://github.com/crewship-ai/crewship/actions/runs/34469301690)
at `4563f5db` and cumulative UI run
[34469677510](https://github.com/crewship-ai/crewship/actions/runs/34469677510)
at `08fcfc60` both completed successfully. Exact SHAs and scope are now pinned in
all five PR bodies. These are branch snapshots, not current-main merge checks.
The UI head contains the current source, server and CLI heads.

Safari panel-only support for v1 is now explicitly confirmed; the support matrix
is in `pages-apps-v1.md`. The independent review author's correction is preserved
in `pages-apps-independent-review-followup-2026-09-10.md`.

CONTRIBUTING.md explicitly permits manual review when CodeRabbit is throttled.
Quota alone is not a delivery blocker; actual layer review still needs evidence.
The previous interpretation that we must wait for CodeRabbit was too strict.

Read-only merge simulation against main `fa222ad2` found conflicts in
`docs/api-reference/openapi.mdx`, `internal/api/testdata/route-roles.txt`, and
`internal/sidecar/routine_mcp_test.go`. Resolve integration and run current CI
before merging; the successful historical runs do not cover that result.
Chat authoring and clean production installation acceptance remain open.


Issue #2472. Integration: `.claude/worktrees/pages-apps-project`, branch
`feat/pages-apps-project`. Root clone has another branch and user WIP; preserve it.
Claim before continuing; release when stopping. No PR has been merged.

Current detailed evidence: [verified follow-up response](pages-apps-followup-response-2026-09-10.md)
and `reports/pages-apps-followup-2026-09-10.json`.
Earlier acceptance: [September 10 validation](pages-apps-validation-2026-09-10.md)
and `reports/pages-apps-hardening-2026-09-09.json`. Historical findings are dated
reviews, not assertions that every listed defect remains present.

## Deployed and verified

Only dev3, using the existing `crewship-ws@3` wrapper. Live clean code commit
`078f3381`, SHA256 `e2e9ec33735e89002038918191a66922f54b2e1aa94e480e897f0385d85fcc7b`.
Running PID/hash and `/api/health` were verified after reload. Production static
export and Go build passed. The compiler profile did not change; configured image
remains `sha256:f2ba48d349d2d89b8d1e54f9d779c36bd10d37d2a9f70c39965c6d39145d244b`.

A real Chromium session against dev3 intercepted only its application response
to supply a mismatched version. Actual panels remained visible, with zero app
iframes and no loading spinner. Evidence `/tmp/pages-2472-live-loading/live-mismatch.json`
and `.png`; screenshot inspected. No server publication was modified for this test.
Live fsck passed: 3 sources, 3 checkpoints, 3 artifacts, 26 Git objects, no failures.

Previous binaries are retained as `crewship.pre-followup-20260910` and
`crewship-sidecar.pre-followup-20260910` in `/srv/crewship/dev3-pages-release`.
No migrations were added by this follow-up. Earlier consistent stopped-service
snapshot: `rollback-2472-20260909` in that directory, DB + Pages data + old release,
excluding crew volumes. Do not call it a complete workspace backup. The live
backup API encountered stopped crew containers; do not start unrelated crews.
CLI: `/srv/crewship/dev3-pages-release/crewship --server http://localhost:8083`.

## Latest fixes and tests

- N2: validate every proposed shallow boundary as an existing commit, in one
  bounded cat-file batch, before changing shallow/lock. Missing/tree inputs are
  rejected without changing existing boundaries or readability; fsck still passes.
- N3: an unopened application's explicit version mismatch immediately shows
  panels with an explanation. It opens only after the metadata agrees.
- N4 is not reproduced: save already validates the compiler's ASCII path profile.
  New real API test proves `src/čísla.tsx` gets 422 without advancing the draft.
- Earlier credential test flakes were fixed using controlled timer advancement
  and waiting for the actual passive-effect callback. Full frontend then passed
  701 files / 8,356 tests, followed by successful complete source/UI CI runs.
- New follow-up: pages, pagebuild and backup full package tests passed (backup
  145.498s); changed-core/backup vet passed. N4 API regression passed. UI component
  tests 7/7; broader selected Pages suite 46 files / 581 tests; changed-file lint
  and production static build passed. This UI selection differs from the older
  598-test selection; do not compare their totals as if the filter were identical.

Earlier live action acceptance proved UI confirmation → queue → completed routine
→ both panel writes from exact run `run_cmtua364y000ac5f80683`. Live VIEWER acceptance
proved grant revocation returns 404 and removes the open iframe; temporary member
was removed. See the earlier validation and JSON for receipts and timestamps.

## CI and review

N1's claim that stack code never ran CI is false: manual workflow_dispatch runs
exist and are recorded with exact commits. Complete green runs before N2/N3:
34378372215 (server), 34378375514 (UI stack), 34459514236 (source after test fixes),
34459629052 (UI stack after test fixes). These include Go/race/macOS checks.

New N2/N3 runs are still in progress, with no failed job observed at this update:
- source: https://github.com/crewship-ai/crewship/actions/runs/34469301690
- UI stack, manually dispatched: https://github.com/crewship-ai/crewship/actions/runs/34469677510

Actual review remains incomplete. Source's old approval covered `0b7c78df`, not
the latest changes. Four other PRs have no submitted reviews. At 11:04 the bot
said the next slot was 7 minutes away. A targeted `--retrigger 2475` at 11:12:28
was rejected at 11:12:34 with `Review rate limited` and a further 59-minute notice.
Do not merge on the green CodeRabbit status, or treat its command acknowledgment
as a review. No override or policy change was made.

| PR | Layer | Latest code head |
|---|---|---|
| #2475 | source/compiler | 4563f5db |
| #2477 | API/storage/backup/MCP | f48fd75b |
| #2479 | CLI/seed/examples | 4ab4b834 |
| #2480 | UI | 08fcfc60 |
| #2481 | docs/evidence | see branch (docs-only updates) |

All bases follow the previous branch in the stack. After actual review and CI,
merge from the bottom while preserving ancestry, explicitly check/retarget the
next base to main, verify its diff remains below 100 files and resolve changelog
conflicts. CI auto-triggers only for base main; use explicit dispatch when testing
an unretargeted stack. Do not infer test coverage from labels/surface checks.

## Remaining product acceptance

Live chat authoring still has not passed: Engineering guided policy returned 403
on page_create, with pending_review but no actionable approval-queue item. A separate
trusted test crew was proposed; explicit user approval remains unanswered. Do not
loosen Engineering or use another full-policy crew to evade the hold.

A separately registrable production runtime domain and DNS/TLS management remain
unspecified. Dev3 is an explicit same-origin internal demo, not production process
isolation evidence. Safari preference was asked separately; recommendation/default
remains desktop Chromium applications, other engines panels. Rendering in Safari
alone does not validate the loop-stop/process-isolation contract. No browser policy
was broadened during this follow-up.
