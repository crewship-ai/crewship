/**
 * Copyable, deliberately small projection of an already-authorized run.
 * This is not an authorization boundary: callers must use their normal
 * workspace-scoped run/journal reads. Never hand this function a broad read
 * token or expose it as an agent tool without a separate server-side scope.
 */
import type { JournalEntry } from "@/lib/types/journal"

export type EvidenceKind = "routine" | "agent"
export interface RunEvidenceInput {
  kind: EvidenceKind
  runId: string
  status?: string
  outcome?: string
  startedAt?: string
  endedAt?: string
  stepId?: string
  failureKind?: string
  trigger?: string
  version?: number | null
  definitionHash?: string
  /** Only entries returned by the authorized run_id filter. Rechecked below. */
  entries?: readonly JournalEntry[]
  journalUnavailable?: boolean
  journalIncomplete?: boolean
  capturedAt: string
}

export interface RunEvidenceEvent {
  reference: string
  at: string
  type: string
  fact: string
}

export interface RunEvidenceView {
  schemaVersion: 1
  reference: { kind: EvidenceKind; id: string }
  sourceLinks: string[]
  status: string
  outcome?: string
  startedAt?: string
  endedAt?: string
  capturedAt: string
  lastRecordedAt?: string
  stepId?: string
  failureKind?: string
  trigger?: string
  version?: number
  definitionHash?: string
  evidence: RunEvidenceEvent[]
  unavailableReasons: string[]
  truncated: boolean
}

export const RUN_EVIDENCE_MAX_BYTES = 16 * 1024
export const RUN_EVIDENCE_MAX_EVENTS = 20
const utf8 = new TextEncoder()
const safeRef = (value: unknown): value is string =>
  typeof value === "string" && value.length <= 128 && /^[a-zA-Z0-9_:-]+$/.test(value)
const safeTime = (value: unknown): value is string =>
  typeof value === "string" && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(value) && Number.isFinite(Date.parse(value))
const oneOf = (value: unknown, options: readonly string[]): string | undefined =>
  typeof value === "string" && options.includes(value) ? value : undefined

const statusValues = ["queued", "running", "waiting", "paused", "completed", "failed", "cancelled", "interrupted", "PENDING", "QUEUED", "RUNNING", "COMPLETED", "FAILED", "CANCELLED"]
const outcomeValues = ["SUCCEEDED", "NO_CHANGE", "WORK_CREATED", "PARTIAL", "NEEDS_HUMAN", "FAILED", "CANCELLED"]
const triggerValues = ["manual", "schedule", "webhook", "event", "issue", "call_pipeline", "task", "mention", "delegation"]
const failureValues = ["checker_rejected", "validation_failed", "transform_input", "timeout", "cancelled", "missing_credential", "missing_integration", "http_status", "script_exit", "cost_cap", "unknown"]

// No journal summary, payload, detail, error_message, command, path, URL,
// prompt or tool I/O crosses this allowlist. A static fact is still useful:
// it tells the reader when/where to open the full authorized source.
const eventFacts: Record<string, string> = {
  "pipeline.run.started": "Routine execution started",
  "pipeline.run.completed": "Routine execution completed",
  "pipeline.run.failed": "Routine execution failed",
  "pipeline.step.started": "Routine step started",
  "pipeline.step.completed": "Routine step completed",
  "pipeline.step.failed": "Routine step failed",
  "pipeline.step.validation_failed": "Routine step validation failed",
  "pipeline.step.retrying": "Routine step retrying",
  "pipeline.step.skipped": "Routine step skipped",
  "run.started": "Agent execution started",
  "run.completed": "Agent execution completed",
  "run.failed": "Agent execution failed",
  "run.cancelled": "Agent execution cancelled",
  "run.timeout": "Agent execution timed out",
  "assignment.running": "Assignment running",
  "assignment.completed": "Assignment completed",
  "assignment.failed": "Assignment failed",
  "assignment.cancelled": "Assignment cancelled",
  "assignment.hard_stopped": "Hard stop signalled",
  "run.agent_span": "Agent action recorded",
}
const terminal = new Set(["pipeline.run.completed", "pipeline.run.failed", "run.completed", "run.failed", "run.cancelled", "run.timeout", "assignment.completed", "assignment.failed", "assignment.cancelled", "assignment.hard_stopped"])

function belongsToRun(entry: JournalEntry, runId: string): boolean {
  return entry.trace_id === runId || entry.actor_id === runId || entry.payload?.run_id === runId
}

function render(view: RunEvidenceView): string {
  return JSON.stringify(view, null, 2)
}

export function buildRunEvidence(input: RunEvidenceInput): RunEvidenceView {
  if (!safeRef(input.runId)) throw new Error("Invalid run reference")
  const url = `/activity?run=${encodeURIComponent(input.runId)}`
  const view: RunEvidenceView = {
    schemaVersion: 1,
    reference: { kind: input.kind, id: input.runId },
    sourceLinks: [url],
    status: oneOf(input.status, statusValues) ?? "unknown",
    capturedAt: safeTime(input.capturedAt) ? input.capturedAt : new Date(0).toISOString(),
    evidence: [],
    unavailableReasons: [],
    truncated: false,
  }
  const outcome = oneOf(input.outcome, outcomeValues)
  if (outcome) view.outcome = outcome
  if (safeTime(input.startedAt)) view.startedAt = input.startedAt
  if (safeTime(input.endedAt)) view.endedAt = input.endedAt
  if (safeRef(input.stepId)) view.stepId = input.stepId
  const failureKind = oneOf(input.failureKind, failureValues)
  if (failureKind) view.failureKind = failureKind
  const trigger = oneOf(input.trigger, triggerValues)
  if (trigger) view.trigger = trigger
  if (Number.isInteger(input.version) && input.version! > 0) view.version = input.version!
  if (typeof input.definitionHash === "string" && /^[a-fA-F0-9]{32,128}$/.test(input.definitionHash)) view.definitionHash = input.definitionHash
  if (input.journalUnavailable) view.unavailableReasons.push("Journal could not be read")
  if (input.journalIncomplete) view.unavailableReasons.push("Older journal events may be missing")

  const candidates = (input.entries ?? [])
    .filter((e) => safeRef(e.id) && safeTime(e.ts) && belongsToRun(e, input.runId) && Object.hasOwn(eventFacts, e.entry_type))
    .map((e): RunEvidenceEvent => ({ reference: e.id, at: e.ts, type: e.entry_type, fact: eventFacts[e.entry_type] }))
    .sort((a, b) => a.at.localeCompare(b.at) || a.reference.localeCompare(b.reference))
  if (!input.entries?.length && !input.journalUnavailable) view.unavailableReasons.push("No correlated journal events were recorded")
  if (candidates.length > RUN_EVIDENCE_MAX_EVENTS) {
    // Preserve the latest terminal event and the most recent other evidence.
    const lastTerminal = [...candidates].reverse().find((e) => terminal.has(e.type))
    const selected = candidates.slice(-(RUN_EVIDENCE_MAX_EVENTS - (lastTerminal ? 1 : 0)))
    if (lastTerminal && !selected.includes(lastTerminal)) selected.push(lastTerminal)
    view.evidence = selected.sort((a, b) => a.at.localeCompare(b.at) || a.reference.localeCompare(b.reference))
    view.truncated = true
  } else {
    view.evidence = candidates
  }
  if (candidates.length < (input.entries?.length ?? 0)) view.unavailableReasons.push("Some events were not correlated or approved for export")
  if (!input.journalUnavailable && candidates.length) {
    const hasTerminal = candidates.some((e) => terminal.has(e.type))
    const status = view.status.toLowerCase()
    if (["completed", "failed", "cancelled", "interrupted"].includes(status) && !hasTerminal)
      view.unavailableReasons.push("Run record is terminal, but no terminal journal event was recorded")
    if (["queued", "running", "waiting", "paused", "pending"].includes(status) && hasTerminal)
      view.unavailableReasons.push("Journal has a terminal event while the run record remains active")
  }
  if (view.evidence.length) view.lastRecordedAt = view.evidence.at(-1)?.at

  // Enforce the final serialized UTF-8 size, including labels and markers.
  // Dropping oldest nonterminal evidence first preserves the stopping fact.
  while (utf8.encode(render(view)).length > RUN_EVIDENCE_MAX_BYTES && view.evidence.length) {
    const nonterminal = view.evidence.findIndex((e) => !terminal.has(e.type))
    view.evidence.splice(nonterminal >= 0 ? nonterminal : 0, 1)
    view.truncated = true
  }
  if (utf8.encode(render(view)).length > RUN_EVIDENCE_MAX_BYTES) throw new Error("Evidence metadata exceeds export limit")
  return view
}

export function formatRunEvidence(view: RunEvidenceView): string {
  const result = render(view)
  if (utf8.encode(result).length > RUN_EVIDENCE_MAX_BYTES) throw new Error("Evidence exceeds export limit")
  return result
}
