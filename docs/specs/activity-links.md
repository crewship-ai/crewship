# Activity link contract

Status: accepted for P0 (#2979). Applies to every edge `internal/chain` emits
and every relation Activity draws.

Activity answers two different questions, and they are not derived from each
other:

- **What did this cause?** The causal chain around a run, an assignment or a
  rule firing: `GET /api/v1/chains/{anchor}`.
- **What happened to this issue?** The issue's own timeline: its events,
  comments and runs, read directly by issue with their own paging. It never
  comes from a page of chains.

## Roles

Every edge carries exactly one role. The role decides whether the edge is
**causal**, which means walking it from a cause reaches consequences.

| Role | Edge kind | Meaning | Causal |
|---|---|---|---|
| **Cause** | `triggers` | `from` made `to` start or exist. | yes |
| **Output** | `produces` | `from` emitted `to` as its result: an inbox ask, a created issue, a written Page value, an escalation. | yes |
| **Definition** | `runs` | `from` is the routine; `to` is one run of it. | no |
| **Executor** | `executes` | `from` is the agent carrying `to` out. | no |
| **Related** | `relates` | An author-declared link with no direction, such as issue ↔ issue. | no |

The rules for walking these roles:

- A walk from an anchor follows only causal edges forward.
- When a walk reaches a **definition** node, it continues only into runs with the same `chain_origin`. The routine's other runs are not consequences of this one.
- An **executor** or **related** node is shown as context ("linked to"). It is never shown as a result ("caused" or "changed").
- An issue may be the **input** of a run (the run carries `triggered_via='issue'`) and also its **output**. These are two edges with two roles, never one.

## What an edge must be backed by

- **A stored column, written when the relation happened.** An edge exists only when a row names the other row in a stored column (or a JSON field written with the row). There are no joins by time window, by shared agent, by crew, by title or by "the newest live row".
- **Workspace fencing on every lookup.** `chain_origin`, `triggered_by_id` and `source_id` are untyped ids with no foreign key behind them.
- **Access checked per node.** A node the caller may not read is not returned, and is not hinted at by label. The rules: inbox items follow `inboxVisibilityClause`, chats and escalations follow `chataudience`, and restricted members keep the route allowlist. A walk that dropped nodes for access says so in `gaps`, without naming what was dropped.
- **A declared gap when the link is not stored.** If a link is not stored, the walker reports a gap (`KnownGaps`). It never guesses.

## Edges in P0

| From → To | Role | Backed by | State |
|---|---|---|---|
| rule → run | cause | `pipeline_runs.triggered_via='automation'` + `triggered_by_id` | walked |
| issue → run | cause | `pipeline_runs.triggered_via='issue'` + `triggered_by_id` (identifier) | walked |
| issue → run | cause | `issue_executions.routine_run_id` | walked (#2986) |
| run → run | cause | `pipeline_runs.chain_origin` / call-pipeline parent | walked |
| routine → run | definition | `pipeline_runs.pipeline_id` | walked |
| run → assignment | cause | `assignments.parent_run_id` | walked |
| assignment → assignment | cause | `assignments.parent_assignment_id` | walked; see below |
| issue → assignment | cause | `mission_tasks.assignment_id` | walked |
| issue → assignment | cause | `assignments.mission_id` | walked (#2986) |
| agent → assignment | executor | `assignments.to_agent_id` | walked |
| run → inbox (waitpoint) | output | `pipeline_waitpoints.pipeline_run_id` | walked |
| run → inbox (failed_run) | output | `inbox_items.payload.run_id` | walked |
| run → inbox (run_needs_human, routine) | output | `inbox_items.source_id` = pipeline run id | walked (#2986) |
| assignment → inbox (run_needs_human, issue) | output | `inbox_items.source_id` = assignment id, `payload.context.mission_id` | walked (#2986) |
| run → issue | output | `missions.author_run_id` | walked (#2986) |
| run → issue change | output | journal `trace_id` = run id on issue events (`issue_events.go`) | **to add** — needs a run-scoped issue-event read |

### Assignment and its attempts

An **assignment** is one unit of work asked of one agent. An **attempt** is one execution of it. Each attempt has its own run id, which becomes the journal `trace_id` and appears in `run.started` with `payload.assignment_id`.

- Attempts are reached from the assignment by `run.started.payload.assignment_id`, ordered by `ts`.
- A re-run of the same assignment is a second attempt, not a second assignment.
- No column on `assignments` holds a run id. Until one exists, `payload.assignment_id` is the stored link and is read as such. It is written with the event, so it is not a time join.

### Known weak link: `parent_assignment_id`

`resolveDelegationScope` picks the acting agent's newest live assignment as the parent. That is selection by shared agent, which this contract does not allow for new edges. The existing edge stays, but it is flagged as inferred at write time. P0 does not build new behaviour on it. The fix is to record the parent from the acting run's token (`actingRunIdentity`), which already carries the run.

## Not in P0, declared as gaps

- **escalation → run / issue.** `escalations` has only `chat_id`. For issue work, `chat_id` equals the mission id, but the column is untyped, so no edge is drawn until a typed column exists.
- **chat turn → run.** There is no per-turn table for agent chats. `chats.pipeline_run_id` (run → chat) is the only stored link.
- **Page value → agent run.** `page_panel_data.producer_run_id` is a foreign key to `pipeline_runs`. Agent pushes carry a journal run id, which that key cannot hold, and the sidecar does not forward it.
- **Issue change / comment → assignment.** `mission_activity` and `mission_comments` store only the actor.

## Inbox audience (fixed in #2986)

Inbox nodes used to ignore the inbox's role and user targeting. Now every inbox node in a walk passes `Options.CanSeeInbox`.

- The API builds the check as `inboxAudience`, the same rule as `inboxVisibilityClause`. A test runs both over every role to keep them in step.
- A hidden item is never named.
- A hidden anchor is a 404.
- The walk adds an `inbox → viewer` gap when it withheld anything.
- A caller that passes no viewer sees only untargeted items.
