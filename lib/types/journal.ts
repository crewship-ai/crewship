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
  // Hooks
  "hook.fired",
  "hook.blocked",
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
    types: ["checkpoint.created", "checkpoint.restored", "fork.created", "hook.fired", "hook.blocked"],
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
