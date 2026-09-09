# Issue execution, Lead review and human acceptance — 2026-09-08

Follow-up to the live QUA-1 / QUA-9 workflow investigation. Feature branch
`feat/issues-work-clarity`, PR #2448, claim #2449. Only dev1 is a deployment target.

## Contract

Each Start snapshots the work and brief revisions into `issue_executions`.
Workers and their delegated assignments belong to that execution. Completion
queues exactly one persistent Lead review task, including unsuccessful worker
results as evidence. The Lead returns a structured HANDOFF `review` verdict:
`approve`, `request_changes` with a worker slug, or `needs_human`.

A corrective verdict creates another execution/task and preserves the old result.
After three work attempts, another rejection goes to a person. Missing outcome,
missing verdict or unsuccessful review cannot finish an issue. Review checks
revision and worker outcomes before approval. A handoff, stop or brief edit
supersedes pending review. Default client acceptance remains required; the detail
panel can opt into Lead completion before starting work.

Human takeover remains a separate work mode, stops agents and cancels an attached
routine. Human submission snapshots the brief; review cannot approve that result
after the brief changes. Human review is transactional with the decision comment
and activity record. The UI supplies expected work/brief revisions.

## Routine and conversation integration

The routine button and ordinary Start use one issue endpoint. The server runs the
bound routine with stored inputs and existing routine authorization/preflight
gates. The execution stores its exact routine run ID and waits durably for it,
including waiting runs, before Lead review. A routine failure is surfaced as
failure. If a routine review cannot target a worker from the issue execution,
the correction goes to a person; it does not silently launch a different routine.

The mission owns the resumable session stream across workers and review. Worker
text/tool events include agent/assignment/run identity; a worker's done/error
cannot terminate the whole mission stream. Normal issue comments and handoffs
remain the human-readable conversation. This does not turn an unrelated chat
message into an issue automatically.

Files explicitly declared in HANDOFF under `/crew/shared/` are copied to issue
attachments (up to 10 files, 6 MiB each) using the existing type validation,
content-addressed dedupe and audit. Confined reads reject escape symlinks and
host paths. Internal memory is not published. Other paths remain in the result
for inspection and can be attached explicitly through the existing agent API.

## UI

`issue-work-panel.tsx`: execution stage, workers, preparation vs execution,
reviewer, attempt, verdict and client acceptance setting.
`issue-detail-surface.tsx`: one Start path including routines and versioned review.
`lib/run-activity.ts`: collapse duplicate starts for the same trace and avoid
presenting opaque actor IDs as names.

## Verification

Regression coverage includes durable review after restart, duplicate scheduling,
correction history, stale briefs, missing outcomes/verdicts, unsuccessful workers,
human review races/version checks, acceptance policy, required routine inputs,
attachment confinement/dedupe, and a shared resumable stream.

Validation and live execution results will be recorded below after deployment.
The default migration lint compares to a newer origin/main and flags the pre-existing
absence of `20260907085608_provider_login_pools.sql` on this preview branch;
comparison to the actual preview base 46627c215 is the relevant append-only check.
No existing migration is edited by this change.

## Verified on dev1

Deployed through `systemctl reload crewship-ws@1`; final application commit
`6705549d6`. Main dev1 preview was fast-forwarded; pre-existing AGENTS/CODEX/design
WIP remains untouched. Fresh SQLite backup is in
`/tmp/crewship-dev1-before-issue-execution-20260908/crewship.db` (0600).

| Issue | Actual observed path | Final state |
| --- | --- | --- |
| [QUA-10](https://crewship-dev1.unifylab.cz/issues/QUA-10) | Jordan delegated to Casey, returned WORK_CREATED; Casey produced the deliberately incomplete test artifact; Jordan independently read it and requested changes; Casey corrected it; a second Jordan review read the exact bytes and approved | Done, automatically; two execution attempts, five completed assignments |
| [QUA-11](https://crewship-dev1.unifylab.cz/issues/QUA-11) | Human takeover, submit, brief edited, approval rejected with HTTP 409, resubmit against new brief, human approve | Done; no agent needed |
| [QUA-12](https://crewship-dev1.unifylab.cz/issues/QUA-12) | Missing routine input rejected with HTTP 422; stored JSON supplied; exact linked routine run produced `{"status":"ready","sum":4}` with SUCCEEDED; Jordan review approved | Review, intentionally left for client acceptance |
| [QUA-13](https://crewship-dev1.unifylab.cz/issues/QUA-13) | Routine parked on approval; survived a server restart; human took over | Todo, with Demo User; routine and waitpoint cancelled, Inbox resolved, downstream transform never executed |

The routine test uses a manual-only transform recipe; the parked test uses a
manual-only approval recipe. Neither has a schedule or external side effect.
QUA-1's existing substantive docs audit was not rerun or accepted as part of these
tests. QUA-10 has both declared artifact versions attached for review history.
The routine reviewer evaluated the platform-provided persisted output and checked
the routine registry; it explicitly noted that it could not independently re-fetch
the literal run payload through its tools. The operator also checked the stored
run output directly. This is not evidence of every possible routine/adapter working.

A live `chat stream` connection reported active, received 171 frames with Jordan
and Casey identities, and 163 sequence-bearing events were strictly increasing.
It continued across worker completion and the corrective handoff. Browser checks
verified the final detail and review controls after the deployment/restart.

Final verification:

- `TMPDIR=/dev/shm go test -p=3 ./... -count=1 -timeout=40m`: passed, 134 tested packages.
- After the final parked-cancellation patch, full `internal/api` suite passed again
  (123.655s), including the actual-schema detail projection and parked approval/Inbox tests.
- `go vet ./...`: passed on final code.
- Frontend Issues + run activity suites: 266 passing tests; the final work-panel
  wording change also passed its four tests.
- `pnpm lint`: zero errors, 32 pre-existing warnings. Production Next.js export
  and Go/sidecar build passed in the final dev1 reload.
- Migration append-only check against the actual preview base and agents invariants passed.
- Backup round-trip now explicitly verifies execution review and assignment linkage.

One live-only defect was caught and fixed during this pass: assignments do not
store a run_id column. Detail now resolves that ID through the same journal
projection used by issue runs, with a real-schema regression test.

The prior Quality crew memory permission failure was also repaired on dev1:
a named POSIX ACL grants only the service UID 1000 access to that crew's `.memory`
directory, preserving agent/sidecar ownership. A matching default ACL was added
where none existed. A write probe passed and the real QUA-11 completion lesson
appeared in lessons.md. Other instances and unrelated crew directories were not
changed; this is a deployment repair, not a claim about every host's permissions.
