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
