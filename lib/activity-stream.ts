// Pure helpers behind /activity-new — the merged activity stream.
//
// The journal already IS the merged feed: `/api/v1/journal` carries runs,
// issues, approvals, keeper decisions, cost and memory as one typed,
// cursor-paginated, full-text-searchable table, and every row already holds
// crew_id / agent_id / mission_id / trace_id. Nothing here fetches; this
// module only classifies and shapes what comes back, so it stays testable
// without a DOM or a server.
//
// Colour NEVER appears as a literal. Each source names a token from
// globals.css and the component resolves it — the palette is owned there,
// and a hex here would fork it silently.

import type { JournalEntry } from "@/lib/types/journal"

/* ------------------------------------------------------------------ *
 *  Sources — the facet a person actually thinks in
 * ------------------------------------------------------------------ */

export type ActivitySource =
  | "run"
  | "routine"
  | "issue"
  | "human"
  | "security"
  | "cost"
  | "memory"
  | "comms"
  | "system"

export interface ActivitySourceMeta {
  key: ActivitySource
  label: string
  /** Short line for the filter tooltip — what lands in this bucket. */
  hint: string
  /** globals.css custom property. Never a literal colour. */
  token: string
  /** Entry types this source owns. Sent verbatim as `entry_type=` filter. */
  types: string[]
}

// `human` is deliberately split out of `security`: an approval REQUEST is
// something waiting on a person, a decision is a record of what happened.
// Folding them together is what makes an inbox feel like a log.
export const ACTIVITY_SOURCES: ActivitySourceMeta[] = [
  {
    key: "run",
    label: "Runs",
    hint: "Agent executions and the commands they ran",
    token: "--info",
    types: [
      "run.started",
      "run.completed",
      "run.failed",
      "run.cancelled",
      "run.timeout",
      "exec.command",
      "exec.output_chunk",
      "run.agent_span",
      "agent.error",
    ],
  },
  {
    key: "routine",
    label: "Routines",
    hint: "Scheduled and triggered routine runs, step by step",
    token: "--notice",
    types: [
      "pipeline.run.started",
      "pipeline.run.completed",
      "pipeline.run.failed",
      "pipeline.dry_run",
      "pipeline.runs_swept",
      "pipeline.step.started",
      "pipeline.step.completed",
      "pipeline.step.failed",
      "pipeline.step.skipped",
      "pipeline.step.retrying",
      "pipeline.step.container_ready",
      "pipeline.step.validation_failed",
      "pipeline.schedule.circuit_breaker_tripped",
      "pipeline.schedule.missed_occurrences",
      // An automation exists to fire a routine, so a person asking "why
      // did my routine not run" looks here — not under System.
      "automation.throttled",
      "automation.depth_exceeded",
    ],
  },
  {
    key: "issue",
    label: "Issues",
    hint: "Board activity — status, comments, mentions, assignments",
    token: "--purple",
    types: [
      "mission.created",
      "mission.assigned",
      "mission.status_change",
      "mission.comment",
      "agent.mentioned",
      "assignment.created",
      "assignment.running",
      "assignment.completed",
      "assignment.failed",
      "crew.action",
      "task.delegated",
    ],
  },
  {
    key: "human",
    label: "Waiting on you",
    hint: "Approvals, escalations and keeper requests that block work",
    token: "--warn",
    types: ["approval.requested", "peer.escalation", "keeper.request"],
  },
  {
    key: "security",
    label: "Security",
    hint: "Decisions and guardrail blocks — the record, not the ask",
    token: "--destructive",
    types: [
      "keeper.decision",
      "guardrail.input_blocked",
      "guardrail.output_blocked",
      "approval.granted",
      "approval.denied",
      "approval.timeout",
      "approval.cancelled",
      "credential.revealed",
      "credential.lease_issued",
      "credential.reveal_policy_changed",
      "credential.sensitivity_lowered",
      "credential.auto_assign_empty",
      "credential.auto_assign_failed",
      "audit.entity_created",
      "audit.entity_updated",
      "audit.entity_deleted",
      "audit.entity_restored",
    ],
  },
  {
    key: "cost",
    label: "Cost",
    hint: "Model calls, spend and budget thresholds",
    token: "--gold",
    types: ["llm.call", "llm.cache_hit", "cost.incurred", "budget.exceeded", "budget.warning"],
  },
  {
    key: "memory",
    label: "Memory & skills",
    hint: "Writes, consolidation, summaries and skill lifecycle",
    token: "--success",
    types: [
      "memory.updated",
      "memory.consolidated",
      "summary.generated",
      "memory.searched",
      "memory.config_updated",
      "memory.consolidation_proposed",
      "memory.versions_swept",
      "memory.write_rejected",
      "memory.write_verifier_blocked",
      "memory.skill_proposed",
      "memory.skill_approved",
      "memory.skill_rejected",
      "skill.invoked",
      "skill.assigned",
      "skill.unassigned",
      "skill.imported",
      "skill.deleted",
    ],
  },
  {
    key: "comms",
    label: "Messages",
    hint: "Peer conversation, broadcasts and outbound notifications",
    token: "--notice",
    types: [
      "peer.conversation",
      "message.broadcast",
      "notification.delivered",
      "notification.failed",
      "notification.dropped",
      "chat.user_message",
      "chat.agent_response",
      "conversation.compacted",
    ],
  },
  {
    key: "system",
    label: "System",
    hint: "Container, network, checkpoints, hooks, eval and migrations",
    token: "--muted-foreground",
    types: [
      "network.port_opened",
      "network.port_closed",
      "network.egress",
      "file.written",
      "container.metrics",
      "container.snapshot",
      "agent.status_change",
      "checkpoint.created",
      "checkpoint.restored",
      "fork.created",
      "hook.fired",
      "hook.blocked",
      "eval.run_started",
      "eval.metric",
      "eval.regression_detected",
      "system.compaction",
      "system.migration",
      "system.hook_toggled",
      "system.consolidation_triggered",
      "system.consolidation_completed",
      "provisioning.queued",
      "provisioning.building",
      "provisioning.step",
      "provisioning.complete",
      "provisioning.failed",
      "provisioning.build_failed",
      "sidecar.stale",
      "image.stale",
    ],
  },
]

const SOURCE_BY_TYPE: ReadonlyMap<string, ActivitySource> = new Map(
  ACTIVITY_SOURCES.flatMap((s) => s.types.map((t) => [t, s.key] as const)),
)

/** Which facet an entry type belongs to. Unknown types stay visible under System. */
export function activitySource(entryType: string): ActivitySource {
  return SOURCE_BY_TYPE.get(entryType) ?? "system"
}

export function sourceEntryTypes(key: ActivitySource): string[] {
  return ACTIVITY_SOURCES.find((s) => s.key === key)?.types ?? []
}

export function sourceMeta(key: ActivitySource): ActivitySourceMeta {
  // The map is exhaustive over the union, so the fallback is unreachable —
  // it exists so callers never have to handle undefined.
  return ACTIVITY_SOURCES.find((s) => s.key === key) ?? ACTIVITY_SOURCES[ACTIVITY_SOURCES.length - 1]
}

/* ------------------------------------------------------------------ *
 *  Time buckets
 * ------------------------------------------------------------------ */

export type TimeBucket = "now" | "hour" | "today" | "yesterday" | "earlier"

export const TIME_BUCKET_ORDER: TimeBucket[] = ["now", "hour", "today", "yesterday", "earlier"]

export const TIME_BUCKET_LABEL: Record<TimeBucket, string> = {
  now: "Right now",
  hour: "Past hour",
  today: "Earlier today",
  yesterday: "Yesterday",
  earlier: "Earlier",
}

const MINUTE = 60_000
const HOUR = 60 * MINUTE

function localDayIndex(d: Date): number {
  // Days since epoch in the VIEWER's timezone. Calendar arithmetic, not
  // millisecond arithmetic: "yesterday" is a date boundary, and a UTC diff
  // would call 23:50 and 00:10 a day apart only sometimes.
  return Math.floor((d.getTime() - d.getTimezoneOffset() * MINUTE) / 86_400_000)
}

export function timeBucket(ts: string, now: Date = new Date()): TimeBucket {
  const t = new Date(ts)
  if (Number.isNaN(t.getTime())) return "earlier"

  const age = now.getTime() - t.getTime()
  // Negative age is clock skew between an agent container and the host, not
  // a future event — it belongs at the top of the feed, not silently at the
  // bottom.
  if (age <= MINUTE) return "now"
  if (age <= HOUR) return "hour"

  const dayDelta = localDayIndex(now) - localDayIndex(t)
  if (dayDelta <= 0) return "today"
  if (dayDelta === 1) return "yesterday"
  return "earlier"
}

export interface ActivityGroup {
  bucket: TimeBucket
  label: string
  entries: JournalEntry[]
}

/** Groups a newest-first feed into time buckets, dropping the empty ones. */
export function groupIntoBuckets(entries: JournalEntry[], now: Date = new Date()): ActivityGroup[] {
  const byBucket = new Map<TimeBucket, JournalEntry[]>()
  for (const e of entries) {
    const b = timeBucket(e.ts, now)
    const list = byBucket.get(b)
    if (list) list.push(e)
    else byBucket.set(b, [e])
  }
  return TIME_BUCKET_ORDER.filter((b) => byBucket.has(b)).map((bucket) => ({
    bucket,
    label: TIME_BUCKET_LABEL[bucket],
    entries: byBucket.get(bucket) ?? [],
  }))
}

/* ------------------------------------------------------------------ *
 *  Correlation spine
 * ------------------------------------------------------------------ */

export type SpineKind = "issue" | "routine" | "run" | "step"

export interface SpineLink {
  kind: SpineKind
  /** What the row shows. */
  label: string
  /** What a click filters on. */
  id: string
}

/** Labels the caller has already resolved, keyed by id. */
export interface SpineLabels {
  issues?: Record<string, string>
  routines?: Record<string, string>
}

function readString(entry: JournalEntry, key: string): string | undefined {
  // payload first, refs second — a handler that sets both means the payload
  // is the specific one. Anything non-string is dropped rather than coerced;
  // String({}) renders "[object Object]" into the UI.
  const fromPayload = entry.payload?.[key]
  if (typeof fromPayload === "string" && fromPayload !== "") return fromPayload
  const fromRefs = entry.refs?.[key]
  if (typeof fromRefs === "string" && fromRefs !== "") return fromRefs
  return undefined
}

/** Compact stand-in for an id we have no name for yet. */
export function shortId(id: string): string {
  return `#${id.slice(-5)}`
}

/**
 * The chain an entry sits in: issue › routine › run › step.
 *
 * This is the whole point of the surface — every row is a place to go up or
 * down the chain, not just a sentence about something that happened. Links
 * are emitted only for ids the entry actually carries, so a row never shows
 * a dead breadcrumb.
 */
export function buildSpine(entry: JournalEntry, labels: SpineLabels = {}): SpineLink[] {
  const out: SpineLink[] = []

  if (entry.mission_id) {
    out.push({
      kind: "issue",
      label: labels.issues?.[entry.mission_id] ?? shortId(entry.mission_id),
      id: entry.mission_id,
    })
  }

  const routine = readString(entry, "pipeline_slug") ?? readString(entry, "routine_slug")
  if (routine) {
    out.push({ kind: "routine", label: labels.routines?.[routine] ?? routine, id: routine })
  }

  const runID = readString(entry, "run_id")
  if (runID) out.push({ kind: "run", label: shortId(runID), id: runID })

  const step = readString(entry, "step_id") ?? readString(entry, "step")
  if (step) out.push({ kind: "step", label: step, id: step })

  return out
}

/* ------------------------------------------------------------------ *
 *  Presentation primitives
 * ------------------------------------------------------------------ */

export type SeverityTone = "default" | "blue" | "warn" | "destructive"

/** Backend severity → the shared DetailTone vocabulary in components/ui/detail. */
export function severityTone(severity: string): SeverityTone {
  switch (severity) {
    case "error":
      return "destructive"
    case "warn":
      return "warn"
    case "notice":
      return "blue"
    default:
      return "default"
  }
}

/**
 * Duration for a right-aligned tabular column: always short, never wraps,
 * and an em dash when there is nothing to show — a blank cell reads as a
 * rendering bug.
 */
export function formatDurationMs(ms: number | undefined | null): string {
  if (ms == null || !Number.isFinite(ms) || ms < 0) return "—"
  if (ms < 1_000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1_000).toFixed(1)}s`
  if (ms < 3_600_000) {
    const m = Math.floor(ms / 60_000)
    const s = Math.floor((ms % 60_000) / 1_000)
    return `${m}m ${String(s).padStart(2, "0")}s`
  }
  const h = Math.floor(ms / 3_600_000)
  const m = Math.floor((ms % 3_600_000) / 60_000)
  return `${h}h ${String(m).padStart(2, "0")}m`
}

/** Duration carried on an entry, wherever the emitting handler put it. */
export function entryDurationMs(entry: JournalEntry): number | undefined {
  for (const key of ["duration_ms", "elapsed_ms", "took_ms"]) {
    const v = entry.payload?.[key] ?? entry.refs?.[key]
    if (typeof v === "number" && Number.isFinite(v)) return v
  }
  return undefined
}

/** Cost carried on an entry — cost rows are why the spend column exists. */
export function entryCostUSD(entry: JournalEntry): number | undefined {
  const v = entry.payload?.["cost_usd"] ?? entry.refs?.["cost_usd"]
  return typeof v === "number" && Number.isFinite(v) ? v : undefined
}

/* ------------------------------------------------------------------ *
 *  Overview shaping
 *
 *  The Activity page answers "what is happening and where do I look",
 *  which is a different question from the journal's "what exactly was
 *  written". These helpers shape the same rows into the handful of
 *  numbers and series the overview cards read.
 * ------------------------------------------------------------------ */

export type ActivityScope = "active" | "waiting" | "failed" | "done"

export interface ActivityScopeMeta {
  key: ActivityScope
  label: string
  token: string
}

export const ACTIVITY_SCOPES: ActivityScopeMeta[] = [
  { key: "active", label: "Running now", token: "--info" },
  { key: "waiting", label: "Waiting on you", token: "--warn" },
  { key: "failed", label: "Failed", token: "--destructive" },
  { key: "done", label: "Completed", token: "--success" },
]

/** Entry types that mean an agent is mid-flight. */
export const ACTIVE_ENTRY_TYPES = ["run.started", "assignment.running"]

const ACTIVE_SET = new Set(ACTIVE_ENTRY_TYPES)

/**
 * Which of the four operational buckets a row belongs to.
 *
 * Severity wins over type: a `run.failed` must never also read as active,
 * and a guardrail block is something that broke even though it is not a
 * run. The buckets are mutually exclusive so the sidebar counts add up to
 * the total — counts that overlap are counts nobody trusts twice.
 */
export function scopeOf(entry: JournalEntry): ActivityScope {
  if (entry.severity === "error") return "failed"
  if (ACTIVE_SET.has(entry.entry_type)) return "active"
  if (activitySource(entry.entry_type) === "human") return "waiting"
  return "done"
}

export interface SourceMixDatum {
  key: ActivitySource
  label: string
  count: number
  token: string
}

/** Per-source totals in a fixed order, with empty sources omitted. */
export function sourceMix(entries: JournalEntry[]): SourceMixDatum[] {
  const counts = new Map<ActivitySource, number>()
  for (const e of entries) {
    const s = activitySource(e.entry_type)
    counts.set(s, (counts.get(s) ?? 0) + 1)
  }
  return ACTIVITY_SOURCES.filter((s) => (counts.get(s.key) ?? 0) > 0).map((s) => ({
    key: s.key,
    label: s.label,
    count: counts.get(s.key) ?? 0,
    token: s.token,
  }))
}

export interface DayBucket {
  /** Local ISO date, yyyy-mm-dd. */
  date: string
  /** Short weekday for the axis. */
  label: string
  total: number
  errors: number
}

/**
 * One bucket per local day for the last `days` days, oldest first.
 *
 * Days with nothing in them are kept: a bar chart that silently skips
 * quiet days compresses time and makes a gap look like activity.
 */
export function dailyCounts(entries: JournalEntry[], days = 7, now: Date = new Date()): DayBucket[] {
  const todayIdx = localDayIndex(now)
  const firstIdx = todayIdx - (days - 1)

  const out: DayBucket[] = []
  for (let i = 0; i < days; i++) {
    const d = new Date(now.getTime() - (days - 1 - i) * 86_400_000)
    out.push({
      date: `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`,
      label: d.toLocaleDateString(undefined, { weekday: "short" }),
      total: 0,
      errors: 0,
    })
  }

  for (const e of entries) {
    const t = new Date(e.ts)
    if (Number.isNaN(t.getTime())) continue
    const slot = localDayIndex(t) - firstIdx
    // Outside the window is dropped, never clamped onto the edge bucket —
    // a clamp turns "older than this chart" into a spike on day one.
    if (slot < 0 || slot >= days) continue
    out[slot].total += 1
    if (e.severity === "error") out[slot].errors += 1
  }

  return out
}

/**
 * Telemetry that is emitted continuously and drowns everything else.
 *
 * A seeded dev instance writes container.metrics per crew per minute, so
 * the eight most recent journal rows are eight CPU/RAM samples — a feed
 * that is technically live and tells you nothing. The backend already
 * anticipates this: the journal CLI documents `--exclude-type` as
 * "useful for hiding container.metrics noise".
 *
 * Excluded by DEFAULT and put back by a toggle, never dropped silently
 * from a filtered query the user built themselves. Nothing a person is
 * waiting on and no failure is in here — tests pin both.
 */
export const NOISE_ENTRY_TYPES: string[] = [
  // Measured on a seeded dev instance, one hour, 500 rows sampled:
  //   208 container.metrics · 134 file.written · 51 network.egress
  //    24 network.port_opened · 15 agent.status_change
  // 86% of the feed. Every one of these is a by-product of an agent doing
  // something, not the something itself.
  "container.metrics",
  "container.snapshot",
  "file.written",
  "network.egress",
  "network.port_opened",
  "network.port_closed",
  "exec.output_chunk",
  "agent.status_change",
  "llm.cache_hit",
  // Step-level routine chatter: valuable inside one run's graph, ruinous
  // in a workspace-wide feed where five routines fire at once.
  "pipeline.step.started",
  "pipeline.step.container_ready",
  "provisioning.step",
  "memory.searched",
]

/**
 * The run a journal entry belongs to, or null.
 *
 * `trace_id` is NOT simply "the run id" — it is overloaded across three
 * namespaces (missions.trace_id is a per-issue "issue-<cuid>", journal
 * trace_id is an agent run id, and message_feedback/eval_runs hold OTel
 * 32-hex ids). Routine runs put their id in `payload.run_id` instead, which
 * the backend exposes as the generated column `journal_entries.run_id`.
 *
 * internal/api/pipeline_runs.go:452 says it plainly and matches BOTH:
 *   "Pipeline runs tag their journal entries with the run id in the payload
 *    (payload.run_id) — NOT the trace_id column … Agent-driven runs use
 *    trace_id instead. Match either so the console works for both."
 *
 * Reading only trace_id is why the execution graph came up empty for
 * routine runs — the exact case the graph exists for.
 */
export function runIdOf(entry: JournalEntry): string | null {
  if (typeof entry.trace_id === "string" && entry.trace_id !== "") return entry.trace_id
  const fromPayload = entry.payload?.["run_id"]
  if (typeof fromPayload === "string" && fromPayload !== "") return fromPayload
  const fromRefs = entry.refs?.["run_id"]
  if (typeof fromRefs === "string" && fromRefs !== "") return fromRefs
  return null
}

/**
 * The subset of a focus this narrowing needs. Deliberately structural rather
 * than an import of the sidebar's EntityFocus: this module is pure and must
 * not depend on a component, and the label is presentation.
 */
export interface FocusRef {
  kind: "issue" | "routine" | "crew"
  id: string
}

/**
 * Narrows a loaded window to the focused entity, for the ROUTINE case only.
 *
 * Issue and crew focus are expressible server-side and are already applied by
 * the query; a routine's slug lives inside the journal payload, which is not
 * indexed, so it can only be narrowed here — over whatever window was loaded.
 *
 * Extracted because two places need the SAME answer and were computing
 * different ones. The overview cards were built from the focused set while the
 * rail's status counts were built from the whole window, so a screen focused on
 * one routine read "FAILED 0 — nothing broke" beside a rail reading "Failed 9".
 * One screen, two answers to "did anything break", and the reassuring one was
 * the wrong one.
 *
 * The rail's counts are a filter CONTROL — each answers "how many would I get
 * if I also clicked this" — which is only true when counted over the same focus
 * the cards use.
 *
 * Both spellings are matched. Producers disagree (`pipeline_slug` from the
 * executor, `routine_slug` from the newer surfaces) and both reach the journal,
 * so matching one silently drops half a routine's events from its own count.
 */
export function narrowToFocus(entries: JournalEntry[], focus: FocusRef | null): JournalEntry[] {
  if (!focus || focus.kind !== "routine") return entries
  return entries.filter((e) => {
    const bag = { ...(e.payload ?? {}), ...(e.refs ?? {}) }
    return bag["pipeline_slug"] === focus.id || bag["routine_slug"] === focus.id
  })
}

/**
 * Wall-clock span of a chain: first entry to last, in milliseconds.
 *
 * Not the sum of per-entry durations, which is what "Chain duration" used to
 * show. That sum reads 0 for an agentless routine whose steps report no
 * duration — rendering as a dash beside "4 events", which tells a reader
 * nothing — and it double-counts a step's time inside the run that contains
 * it. Neither is what the word duration promises.
 *
 * Null, not zero, for fewer than two datable entries. One event has no span,
 * and 0ms would assert "it was instant" where the truth is "there is nothing
 * to measure between".
 *
 * Unparseable timestamps are skipped rather than poisoning the result with
 * NaN: a chain with one bad row should still report the span of the good ones.
 */
export function chainElapsedMs(chain: JournalEntry[]): number | null {
  const times: number[] = []
  for (const e of chain) {
    const t = Date.parse(e.ts ?? "")
    if (Number.isFinite(t)) times.push(t)
  }
  if (times.length < 2) return null
  return Math.max(...times) - Math.min(...times)
}
