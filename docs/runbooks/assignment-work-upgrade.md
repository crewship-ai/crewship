# Assignment work ownership upgrade

Ordinary assignments, mentions, channel requests, mission tasks and lead planning
commit their assignment and durable work owner together. The shared dispatcher
owns admission, leases, run identity, cancellation and recovery. Existing legacy
`RUNNING` assignments retain their executor and count against execution limits;
only unexecuted `PENDING` and `QUEUED` assignments transfer ownership on upgrade.

The migration refuses an unowned queued assignment that still names an exec or
container. A queued label cannot establish that a previous process is absent.
Before retrying a refused upgrade, inspect the affected records read-only:

```sql
SELECT a.id, a.workspace_id, a.status, a.exec_id, a.exec_container_id,
       a.cancel_requested_at
FROM assignments a
WHERE a.status IN ('PENDING', 'QUEUED')
  AND (COALESCE(a.exec_id, '') != '' OR COALESCE(a.exec_container_id, '') != '')
  AND NOT EXISTS (
    SELECT 1 FROM work_items w
    WHERE w.source = 'assignment' AND w.domain_kind = 'assignment'
      AND w.workspace_id = a.workspace_id AND w.domain_id = a.id
  );
```

Keep the previous release available while an operator reconciles each named
execution using its exact container and exec identity. A failed inspection or a
missing observation is insufficient evidence of a stopped process. Use the
existing stop and recovery procedures to persist the investigated domain result;
retain the evidence and operator decision. Retry the upgrade only after the
ambiguous queued records have been reconciled. Do not clear exec fields or
change a status solely to make the migration pass.

Mission task admission claims the pending task, its assignment, work owner and
link in one transaction. It requires the current mission to remain
`IN_PROGRESS`; stop or human handoff committed first prevents admission. A failed
post-commit wake preserves accepted work for shared polling and cannot undo its
owner.

A cancellation request remains intent until the shared owner confirms that
creation was prevented or execution stopped. Issue handoff and stop transfer
that intent into the work ledger in the same transaction. An uncertain runtime
holds its execution capacity and requires reconciliation; it cannot become a
successful assignment because an observer disconnected.

Terminal projection retries stored output and receipts without rerunning the
agent. Assignment results, session checkpoints and consumed deliveries survive
restart; mission completion effects and loop retries have a transactional
assignment receipt. Replaying an assignment through the generic work replay
endpoint is refused: a new assignment must have its own domain identity.

Lead planning uses `missions.planning_assignment_id` as its durable identity.
The migration adopts the earliest existing planning assignment for each mission;
restarts and failed wake-up hints reuse it. Admission reserves this identity only
while the mission is `IN_PROGRESS`, in a transaction serialized with stop and
human handoff. A failed or completed planner is not automatically replayed as a
new assignment; an operator must choose the next mission action explicitly.
