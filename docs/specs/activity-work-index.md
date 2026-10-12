# Activity work index: work outside routines (proposal)

Status: A implemented in #2993. B awaits the owner's decision.

## Problem

The rail and the home list chains from `pipeline_runs` only. Agent work started without a routine is not in the index at all:

- a delegation from a chat,
- `/assign`,
- an issue mention,
- lead planning,
- a workspace-channel job,
- a plain chat turn.

## What the data stores today

| Work | Stored identity | Chain grouping |
|---|---|---|
| Routine run (+ what it dispatched) | `pipeline_runs.id`, `chain_origin` | yes, today's index |
| Assignment tree started outside a routine | root `assignments.id`. The root has `chain_origin` NULL and `parent_run_id` NULL; its descendants carry `chain_origin` = root id | yes — stored on every row |
| Attempt of an assignment | journal `run.started` with `payload.assignment_id`, `trace_id` = run id | per assignment, via the journal |
| Direct chat turn (no assignment) | journal `run.started` with `payload.chat_id`, `trace_id` = run id | **none**. No row ties several turns into one piece of work |

## Proposal

### A. Assignment-rooted chains (implement now)

- A root assignment with `chain_origin IS NULL AND parent_run_id IS NULL` becomes an index row with `kind = "assignment"` and `origin = root id`. Its descendants are the rows whose `chain_origin` equals that id. Only stored columns are used; nothing is grouped by time.
- Status uses the same outcome counts as routine chains: assignment statuses map to running / waiting / completed / failed / cancelled.
- The row says what started it in short words: `from chat`, `issue OPS-1`, `mention`, `lead planning`, `channel`.
- The run page for such a row shows:
  - the assignment's task;
  - its attempts, from `run.started.payload.assignment_id`;
  - its delegations, through the tree;
  - its issue (`assignments.mission_id`);
  - its `run_needs_human` asks.
- **Access:** a row is shown only if the viewer may read the chat it came from (`chataudience`, `assignments.chat_id`), or, for issue work, if they may read the issue. Restricted members keep the route allowlist.

### B. Direct chat turns (needs your decision)

A chat turn that dispatches nothing has a run id but no stored "piece of work" around it. Options:

1. **One row per turn.** It is exact, but noisy: every message an agent answers becomes an activity.
2. **One row per chat session per day,** grouped by `chat_id` and calendar day. This is a grouping *convention*, not a causal link, and it would be labelled as such ("conversation with Casey, today").
3. **Leave chat turns to Chat and Sessions.** Activity lists work, meaning anything that was dispatched or ran a routine. A chat appears only when it caused work (row A).

**Recommendation: option 3 now.** It keeps "every row is a stored piece of work" true, and the chat itself is one click away from any row it caused. Option 2 can follow if people miss it.

## Contract additions

- New node and row kind `assignment` as a chain origin.
- An edge from chat → assignment would need `assignments.chat_id`, which is typed only as "the dispatching chat or the mission id". It stays context ("from chat"), not a causal edge, until a typed column exists.
