import { z } from "zod"

/**
 * Journal entry types — must match backend `internal/journal/types.go`.
 * When a new EntryType is added in Go, mirror it here so the UI can group
 * and colour-code it; unknown types still render via the fallback path.
 */
export const JOURNAL_ENTRY_TYPES = [
  // Communication
  "peer.conversation",
  "peer.escalation",
  "message.broadcast",
  "agent.mentioned",
  // Mission
  "mission.status_change",
  "mission.comment",
  "assignment.created",
  "assignment.running",
  "assignment.completed",
  "assignment.failed",
  "crew.action",
  "task.delegated",
  // Runs
  "run.started",
  "run.completed",
  "run.failed",
  "run.cancelled",
  "run.timeout",
  // Security
  "keeper.request",
  "keeper.decision",
  "guardrail.input_blocked",
  "guardrail.output_blocked",
  "approval.requested",
  "approval.granted",
  "approval.denied",
  "approval.timeout",
  "approval.cancelled",
  // Standing decisions: "approve this gate for me from now on", and the
  // withdrawal of that permission. Namespaced under approval.* on purpose —
  // one filter has to show the one-off decision and the standing grant that
  // explains why later runs stopped asking.
  "approval.trust_granted",
  "approval.trust_revoked",
  // Cost
  "llm.call",
  "llm.cache_hit",
  "cost.incurred",
  "budget.exceeded",
  "budget.warning",
  // Memory
  "memory.updated",
  "memory.consolidated",
  "summary.generated",
  // Observability
  "exec.command",
  "exec.output_chunk",
  "network.port_opened",
  "network.port_closed",
  "network.egress",
  "file.written",
  "container.metrics",
  "container.snapshot",
  // Presence
  "agent.status_change",
  // Checkpointing
  "checkpoint.created",
  "checkpoint.restored",
  "fork.created",
  // A forked restore (--as-workspace/--as-crew) re-signed this workspace's
  // journal hash chain and started it over at a new genesis (#2226). The row
  // is the audit record that the chain no longer links back to the source.
  "backup.chain_resigned",
  // Hooks
  "hook.fired",
  "hook.blocked",
  "hook.dispatch_error",
  // Eval
  "eval.run_started",
  "eval.metric",
  "eval.regression_detected",
  // System
  "system.compaction",
  "system.migration",
  "system.hook_toggled",
  "system.consolidation_triggered",
  "system.consolidation_completed",
  // Issues — creation and assignment used to be indistinguishable from a
  // status change, so the timeline showed one icon for all three.
  "mission.created",
  "mission.assigned",
  // Notifications — an outbound send is a side effect that LEAVES the
  // instance, and it deserves its own row and icon next to the event that
  // caused it rather than living only in an admin-only deliveries table.
  "notification.delivered",
  "notification.failed",
  "notification.dropped",

  // ── Added 2026-08-07 after measuring a 50-type drift against
  // internal/journal/types.go. lib/__tests__/activity-stream.test.ts now
  // reads that Go file directly, so this list cannot fall behind silently
  // again.
  // Routines
  "pipeline.run.started",
  "pipeline.run.completed",
  "pipeline.run.failed",
  "pipeline.step.started",
  "pipeline.step.completed",
  "pipeline.step.failed",
  "pipeline.step.validation_failed",
  "pipeline.dry_run",
  "pipeline.step.skipped",
  "pipeline.step.retrying",
  "pipeline.step.container_ready",
  "pipeline.schedule.circuit_breaker_tripped",
  "pipeline.schedule.missed_occurrences",
  "pipeline.runs_swept",
  // Chat
  "conversation.compacted",
  "chat.user_message",
  "chat.agent_response",
  // Provisioning
  "sidecar.stale",
  "image.stale",
  "provisioning.queued",
  "provisioning.building",
  "provisioning.complete",
  "provisioning.failed",
  "provisioning.build_failed",
  "provisioning.step",
  // Credentials
  "credential.revealed",
  "credential.reveal_policy_changed",
  "credential.sensitivity_lowered",
  "credential.auto_assign_failed",
  "credential.auto_assign_empty",
  "credential.lease_issued",
  // Skills
  "skill.imported",
  "skill.deleted",
  "skill.assigned",
  "skill.unassigned",
  "skill.invoked",
  // Audit
  "audit.entity_created",
  "audit.entity_updated",
  "audit.entity_deleted",
  "audit.entity_restored",
  // Memory (extended)
  "memory.write_rejected",
  "memory.consolidation_proposed",
  "memory.write_verifier_blocked",
  "memory.searched",
  "memory.versions_swept",
  "memory.config_updated",
  "memory.skill_proposed",
  "memory.skill_approved",
  "memory.skill_rejected",
  // Runs (extended)
  "run.agent_span",
  "agent.error",
  // The CLI session a run happened inside — model, binary version, and the
  // MCP servers it started WITHOUT. Severity carries the difference.
  "run.session_init",
  // Automations — the composition substrate. Both mean work did NOT
  // happen: throttled is a rule over its hourly cap, depth_exceeded is a
  // composed chain refused at the shared ceiling. Someone asking "why did
  // my routine not run" needs to find these, so they live under Routines
  // rather than in System where they would be filed and forgotten.
  "automation.throttled",
  "automation.depth_exceeded",
  // Pages — docs/prd/pages.md §5, §7.1b. Unknown types are forwarded to the
  // activity feed by design (internal/journal/feed_filter.go:33-35).
  "page.produce_denied",
  "page.panel.updated",
  "page.grant_added",
  "page.grant_removed",
  // §4 freshness verdicts and §5's wake gates.
  "page.panel.stale",
  "page.panel.recovered",
  "page.wake.fired",
  // §8b.2 — one entry per action click, written by the dispatch handler
  // (internal/api/pages_actions.go). The audit trail for "a button on a page
  // started a run" is the reason §8 rule 8 treats the platform's own agent as
  // an untrusted producer: the record has to exist regardless of who clicked.
  "page.action.dispatched",
  "page.spec.changed",

  // ── Added 2026-08-31 (#2207). Every type below is emitted by the
  // backend today but was declared OUTSIDE internal/journal/types.go — as
  // a file-local `journal.EntryType` constant, or as a bare string literal
  // at the emit site — so the backend-parity ratchet above, which reads
  // only types.go, could not see any of them.
  // lib/__tests__/journal-groups.test.ts now scans the whole Go tree.
  // §7.3.2 public links, §8b webhooks, and the owner handoff.
  "page.published",
  "page.link_revoked",
  "page.public_view",
  "page.webhook_issued",
  "page.webhook_revoked",
  "page.owner_transferred",
  // The setup agent's proposal being applied to a real workspace.
  "onboarding.proposal_applied",
  // Priority markers on existing entries. Emitted as a bare literal from
  // internal/api/journal_handler.go until #2207 promoted it to
  // journal.EntryMemoryPriorityChanged.
  "memory.priority_changed",
  // Keeper's rolling-reward auto-tuning: the rule that retuned itself, and
  // an operator wiping the window it scores against.
  "keeper.rule_auto_tuned",
  "approval.auto_tuning_reset",
  // Crew autonomy/behaviour policy edits, and the stuck-assignment sweeper.
  "policy.changed",
  "queue.sweeper_pumped",
] as const

export type JournalEntryType = (typeof JOURNAL_ENTRY_TYPES)[number]

/**
 * Category groupings used by the filter sidebar to fold 40+ entry types
 * into a handful of sections. Keep the ordering here stable — the filter
 * panel renders in this order.
 */
export const ENTRY_TYPE_GROUPS: { label: string; types: JournalEntryType[] }[] = [
  {
    label: "Communication",
    types: ["peer.conversation", "peer.escalation", "message.broadcast", "agent.mentioned"],
  },
  {
    label: "Mission",
    types: [
      "mission.status_change",
      "mission.comment",
      "assignment.created",
      "assignment.running",
      "assignment.completed",
      "assignment.failed",
      "crew.action",
      "task.delegated",
    ],
  },
  {
    label: "Security",
    types: [
      "keeper.request",
      "keeper.decision",
      "guardrail.input_blocked",
      "guardrail.output_blocked",
      "approval.requested",
      "approval.granted",
      "approval.denied",
      "approval.timeout",
    ],
  },
  {
    label: "Cost",
    types: ["llm.call", "llm.cache_hit", "cost.incurred", "budget.exceeded", "budget.warning"],
  },
  {
    label: "Memory",
    types: ["memory.updated", "memory.consolidated", "summary.generated"],
  },
  {
    label: "Observability",
    types: [
      "exec.command",
      "exec.output_chunk",
      "network.port_opened",
      "network.port_closed",
      "network.egress",
      "file.written",
      "container.metrics",
    ],
  },
  {
    label: "Presence",
    types: ["agent.status_change"],
  },
  {
    label: "Runs",
    types: ["run.started", "run.completed", "run.failed", "run.cancelled", "run.timeout"],
  },
  {
    label: "Checkpointing",
    types: [
      "checkpoint.created",
      "checkpoint.restored",
      "fork.created",
      "backup.chain_resigned",
      "hook.fired",
      "hook.blocked",
    ],
  },
]

export const JOURNAL_SEVERITIES = ["info", "notice", "warn", "error"] as const
export type JournalSeverity = (typeof JOURNAL_SEVERITIES)[number]

export const journalEntrySchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  ts: z.string(),
  entry_type: z.string(),
  severity: z.enum(["info", "notice", "warn", "error"]).or(z.string()),
  actor_type: z.string(),
  summary: z.string(),
  crew_id: z.string().optional(),
  agent_id: z.string().optional(),
  mission_id: z.string().optional(),
  actor_id: z.string().optional(),
  trace_id: z.string().optional(),
  // payload is free-form JSON — loosely typed on purpose (<any> per task spec).
  payload: z.record(z.string(), z.unknown()).optional(),
  refs: z.record(z.string(), z.unknown()).optional(),
})

export type JournalEntry = z.infer<typeof journalEntrySchema>

export const journalListResponseSchema = z.object({
  entries: z.array(journalEntrySchema),
  next_cursor: z.string().optional().nullable(),
  count: z.number().optional(),
})
