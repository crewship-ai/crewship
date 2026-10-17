// The routine page (Harbor 2): tabs, the plain-language state in its header,
// the one list of what starts a routine, the run timeline and the Markdown
// trace "Copy for AI" puts on the clipboard. Pure, so every rule here is a
// table in a test rather than a guess in a component.

import { describeCron } from "@/lib/cron-describe"
import { scheduleHealth } from "@/lib/schedule-health"
import type { StatusTone } from "@/lib/format-status"
import { stepFromDsl, stepToDsl, type WizardStep } from "@/lib/routine-wizard"

/* ------------------------------------------------------------------ tabs */

export const ROUTINE_TABS = ["overview", "steps", "triggers", "versions", "settings"] as const
export type RoutineTab = (typeof ROUTINE_TABS)[number]
export type DetailDepth = "simple" | "full"

/** Preference key for Simple / Full detail (synced per user). */
export const DETAIL_DEPTH_PREF = "routines.detailDepth"

export const TAB_LABEL: Record<RoutineTab, string> = {
  overview: "Overview",
  steps: "Steps",
  triggers: "Triggers",
  versions: "Versions",
  settings: "Settings",
}

/**
 * Where an older `?view=` link lands. The routine page used to have Overview
 * (`definition`), History, Versions and Plan, with Edit and Publish opening
 * dialogs over it; the tabs are Overview · Steps · Triggers · Settings now,
 * with Versions in Full detail, and the runs live in Activity.
 */
export interface LegacyViewTarget {
  tab: RoutineTab
  /** Turn Full detail on for this visit (Versions is a Full-detail tab). */
  full?: boolean
  /** Open the publish review over the page. */
  publish?: boolean
  /** The runs of a routine are read in Activity. */
  activity?: boolean
}

export function legacyViewTarget(view: string | null | undefined): LegacyViewTarget | null {
  switch (view) {
    case "definition":
    case "overview":
      return { tab: "overview" }
    case "plan":
    case "triggers":
      return { tab: "triggers" }
    case "edit":
    case "settings":
      return { tab: "settings" }
    case "steps":
      return { tab: "steps" }
    case "versions":
      return { tab: "versions", full: true }
    case "publish":
      return { tab: "overview", publish: true }
    case "history":
      return { tab: "overview", activity: true }
    default:
      return null
  }
}

/** Overview · Steps · Triggers · (Versions) · Settings. Versions shows in Full
 *  detail, and stays visible while it is the open tab (a direct link). */
export function visibleTabs(depth: DetailDepth, current: RoutineTab): RoutineTab[] {
  return ROUTINE_TABS.filter((t) => t !== "versions" || depth === "full" || current === "versions")
}

export function parseTab(raw: string | null | undefined): RoutineTab {
  return (ROUTINE_TABS as readonly string[]).includes(raw ?? "") ? (raw as RoutineTab) : "overview"
}

/** The runs of one routine in Activity. */
export function routineActivityHref(slug: string): string {
  return `/activity?${new URLSearchParams({ pipeline: slug })}`
}

/** A routine page link: `/routines?slug=…&tab=…`. */
export function routineTabHref(slug: string, tab: RoutineTab = "overview", extra?: Record<string, string>): string {
  return `/routines?${new URLSearchParams({ slug, ...(tab === "overview" ? {} : { tab }), ...(extra ?? {}) })}`
}

/* ---------------------------------------------------------------- header */

export interface RoutineStateInput {
  headVersion?: number | null
  draft?: { revision: number } | null
  draftOnly?: boolean
}

export interface StateBadge {
  label: string
  tone: StatusTone
}

/**
 * "Live" / "Not live yet" and "Changes not live", in the reader's words.
 * Version and revision numbers are Full detail: a person asking "is it on?"
 * does not need to know it is v4 and draft r2.
 */
export function routineStateBadges(r: RoutineStateInput, depth: DetailDepth): { live: StateBadge; changes: StateBadge | null } {
  const published = !r.draftOnly && (r.headVersion ?? 0) > 0
  const live: StateBadge = published
    ? { label: depth === "full" ? `Live v${r.headVersion}` : "Live", tone: "success" }
    : { label: "Not live yet", tone: "muted" }
  const changes: StateBadge | null =
    published && r.draft
      ? { label: depth === "full" ? `Changes not live · draft r${r.draft.revision}` : "Changes not live", tone: "purple" }
      : null
  return { live, changes }
}

/* --------------------------------------------------------- last 20 runs */

export interface RunBarInput {
  id: string
  status: string
  outcome?: string
  started_at?: string
}

export type RunBarTone = "ok" | "bad" | "warn" | "mut" | "info"

/** The oldest of the last `n` runs on the left; tone by how each ended. */
export function lastRunBars(records: RunBarInput[], n = 20): { id: string; tone: RunBarTone; label: string }[] {
  return records
    .slice(0, n)
    .map((r) => ({ id: r.id, ...runBarTone(r) }))
    .reverse()
}

export function runBarTone(r: Pick<RunBarInput, "status" | "outcome">): { tone: RunBarTone; label: string } {
  if (r.outcome === "FAILED" || r.status === "failed" || r.status === "interrupted") return { tone: "bad", label: "Could not finish" }
  if (r.status === "waiting" || r.outcome === "NEEDS_HUMAN") return { tone: "warn", label: "Waiting for a person" }
  if (r.status === "running" || r.status === "queued") return { tone: "info", label: "Running" }
  if (r.status === "cancelled") return { tone: "mut", label: "Stopped" }
  if (r.outcome === "NO_CHANGE") return { tone: "mut", label: "Nothing changed" }
  return { tone: "ok", label: "Completed" }
}

/* -------------------------------------------------------------- triggers */

export type TriggerKind = "schedule" | "webhook" | "chat" | "event" | "manual"

export interface TriggerRow {
  key: string
  kind: TriggerKind
  kindLabel: string
  title: string
  detail: string
  /** Full detail only: the raw expression behind the words. */
  code?: string
  status: StateBadge
  /** The managing row further down the tab (`schedule-<id>`, `webhook-<id>`). */
  anchor?: string
}

export interface TriggerScheduleInput {
  id: string
  name: string
  cron_expr: string
  timezone: string
  enabled: boolean
  next_run_at?: string
  target_pipeline_version?: number | null
  version_pinned?: boolean
  catchup_policy?: string
  consecutive_failures: number
  max_consecutive_failures: number
  disabled_reason?: string
  activation?: "draft"
}

export interface TriggerWebhookInput {
  id: string
  name: string
  enabled: boolean
  ingress_profile?: string
  last_fired_at?: string
  last_status?: string
  rate_limit_per_min?: number
}

export interface TriggerAutomationInput {
  id: string
  name: string
  enabled: boolean
  event_type: string
}

const CATCHUP: Record<string, string> = {
  once: "if missed: run once",
  skip: "if missed: skip",
  all: "if missed: run each",
}

/**
 * Everything that can start this routine, as one list (v67 Triggers): the
 * schedules, the webhooks, the chat command, the automations that fire it,
 * and the Run button — which is always there.
 */
export function routineTriggerRows(input: {
  slug: string
  lifecycle: "active" | "proposed" | "disabled"
  schedules: TriggerScheduleInput[]
  webhooks: TriggerWebhookInput[]
  automations: TriggerAutomationInput[]
  slash?: { enabled?: boolean; label?: string } | null
  formatTime?: (iso: string, tz?: string) => string
}): TriggerRow[] {
  const fmt = input.formatTime ?? ((iso: string) => iso)
  const rows: TriggerRow[] = []
  for (const s of input.schedules) {
    const health = scheduleHealth(s)
    const pinned = s.version_pinned ?? s.target_pipeline_version != null
    const parts = [
      s.timezone || "UTC",
      s.enabled && s.next_run_at ? `next ${fmt(s.next_run_at, s.timezone)}` : null,
      CATCHUP[s.catchup_policy ?? ""] ?? null,
      pinned ? `pinned to v${s.target_pipeline_version}` : "follows the published version",
    ].filter(Boolean)
    const status: StateBadge =
      s.activation === "draft"
        ? { label: "Waiting for approval", tone: "purple" }
        : input.lifecycle === "proposed"
          ? { label: "Starts after approval", tone: "purple" }
          : s.disabled_reason === "circuit_breaker"
            ? { label: "Paused by circuit breaker", tone: "danger" }
            : !s.enabled
              ? { label: "Off", tone: "muted" }
              : health.tone === "warn"
                ? { label: health.label, tone: "warn" }
                : { label: "On", tone: "success" }
    rows.push({
      key: `schedule:${s.id}`,
      kind: "schedule",
      kindLabel: "Schedule",
      title: describeCron(s.cron_expr),
      detail: parts.join(" · "),
      code: `cron ${s.cron_expr}`,
      status,
      anchor: `schedule-${s.id}`,
    })
  }
  for (const w of input.webhooks) {
    rows.push({
      key: `webhook:${w.id}`,
      kind: "webhook",
      kindLabel: "Webhook",
      title: w.name,
      detail: [
        w.ingress_profile === "github" ? "GitHub signature" : "HMAC signature",
        w.rate_limit_per_min ? `${w.rate_limit_per_min}/min` : null,
        w.last_fired_at ? `last delivery ${fmt(w.last_fired_at)}${w.last_status ? ` · ${w.last_status}` : ""}` : "no delivery yet",
      ]
        .filter(Boolean)
        .join(" · "),
      status: w.enabled ? { label: "On", tone: "success" } : { label: "Off", tone: "muted" },
      anchor: `webhook-${w.id}`,
    })
  }
  if (input.slash?.enabled) {
    rows.push({
      key: "chat",
      kind: "chat",
      kindLabel: "Chat command",
      title: `/${input.slug}`,
      detail: `Anyone who can run this routine can type /${input.slug} in Chat${input.slash.label ? ` · shown as “${input.slash.label}”` : ""}`,
      status: { label: "On", tone: "success" },
    })
  }
  for (const a of input.automations) {
    rows.push({
      key: `event:${a.id}`,
      kind: "event",
      kindLabel: "Event",
      title: a.name,
      detail: `When ${a.event_type.replaceAll(".", " ").replaceAll("_", " ")} happens in the workspace`,
      status: a.enabled ? { label: "On", tone: "success" } : { label: "Off", tone: "muted" },
    })
  }
  rows.push({
    key: "manual",
    kind: "manual",
    kindLabel: "By hand",
    title: "Run button and CLI",
    detail: `crewship routine run ${input.slug}`,
    status:
      input.lifecycle === "proposed"
        ? { label: "Starts after approval", tone: "purple" }
        : input.lifecycle === "disabled"
          ? { label: "Off", tone: "muted" }
          : { label: "On", tone: "success" },
  })
  return rows
}

/* -------------------------------------------------------------- timeline */

export interface TimelineExecution {
  step_id: string
  status: string
  started_at: string
  ended_at?: string
  attempt?: number
}

export interface TimelineBar {
  stepId: string
  status: string
  /** Percent of the run's span. */
  left: number
  width: number
  durationMs: number | null
}

/**
 * One bar per step, placed on the run's own clock (first start = 0, last end
 * or `now` = 100 %). A step with several attempts spans from its first start
 * to its last end. Steps that never started are left out; the step list says
 * they did not run.
 */
export function timelineBars(rows: TimelineExecution[], now = Date.now()): TimelineBar[] {
  const spans = new Map<string, { start: number; end: number; open: boolean; status: string; attempt: number }>()
  for (const r of rows) {
    const start = Date.parse(r.started_at)
    if (!Number.isFinite(start)) continue
    const endParsed = r.ended_at ? Date.parse(r.ended_at) : NaN
    const open = !Number.isFinite(endParsed)
    const end = open ? now : endParsed
    const cur = spans.get(r.step_id)
    const attempt = r.attempt ?? 1
    if (!cur) spans.set(r.step_id, { start, end, open, status: r.status, attempt })
    else {
      cur.start = Math.min(cur.start, start)
      cur.end = Math.max(cur.end, end)
      cur.open = cur.open || open
      if (attempt >= cur.attempt) {
        cur.status = r.status
        cur.attempt = attempt
      }
    }
  }
  if (spans.size === 0) return []
  const all = [...spans.values()]
  const t0 = Math.min(...all.map((s) => s.start))
  const t1 = Math.max(...all.map((s) => s.end))
  const total = Math.max(1, t1 - t0)
  return [...spans.entries()]
    .sort((a, b) => a[1].start - b[1].start)
    .map(([stepId, s]) => ({
      stepId,
      status: s.status,
      left: ((s.start - t0) / total) * 100,
      width: Math.max(0.8, ((s.end - s.start) / total) * 100),
      durationMs: s.open ? null : s.end - s.start,
    }))
}

/* ---------------------------------------------------------- Copy for AI */

export interface TraceRun {
  id: string
  pipeline_slug: string
  pipeline_name?: string
  pipeline_version?: number | null
  status: string
  outcome?: string
  started_at?: string
  ended_at?: string
  duration_ms?: number | null
  cost_usd?: number | null
  failed_at_step?: string
  error_message?: string
  inputs?: Record<string, unknown> | null
  output?: string
  step_outputs?: Record<string, unknown> | null
}

const SECRET_KEY = /(secret|token|password|passwd|api[_-]?key|credential|auth|private)/i
const SECRET_VALUE = /\b(sk-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9]{16,}|xox[abprs]-[A-Za-z0-9-]{8,}|eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}|Bearer\s+[A-Za-z0-9._~+/-]{12,}=*)/g

/** Masks what looks like a credential; the trace goes to a clipboard and from
 *  there to whatever the reader pastes it into. */
export function redactSecrets(text: string): string {
  return text.replace(SECRET_VALUE, "[redacted]")
}

function clip(text: string, max = 1200): string {
  const t = redactSecrets(text.trim())
  return t.length > max ? `${t.slice(0, max)}… (${t.length - max} more characters)` : t
}

/**
 * A Markdown trace of one run for an assistant: what ran, in which order,
 * how each step ended, and the error. Inputs whose name says they carry a
 * secret are left out; values that look like tokens are masked.
 */
export function runTraceMarkdown(
  run: TraceRun,
  steps: { id: string; name?: string; type?: string }[],
  executions: { step_id: string; status: string; started_at?: string; ended_at?: string; attempt?: number; error?: string }[],
): string {
  const lines: string[] = []
  lines.push(`# Routine run ${run.id}`)
  lines.push("")
  lines.push(`- Routine: ${run.pipeline_name || run.pipeline_slug} (\`${run.pipeline_slug}\`)${run.pipeline_version != null ? ` · v${run.pipeline_version}` : ""}`)
  lines.push(`- Status: ${run.status}${run.outcome ? ` · ${run.outcome}` : ""}`)
  if (run.started_at) lines.push(`- Started: ${run.started_at}${run.ended_at ? ` · ended ${run.ended_at}` : ""}`)
  if (run.duration_ms != null) lines.push(`- Duration: ${Math.round(run.duration_ms / 100) / 10} s`)
  if (run.cost_usd != null) lines.push(`- Reported usage: $${run.cost_usd.toFixed(2)}`)
  const inputs = Object.entries(run.inputs ?? {})
  if (inputs.length) {
    lines.push("", "## Inputs")
    for (const [k, v] of inputs) {
      lines.push(SECRET_KEY.test(k) ? `- ${k}: [left out]` : `- ${k}: ${clip(typeof v === "string" ? v : JSON.stringify(v), 300)}`)
    }
  }
  lines.push("", "## Steps")
  const latest = new Map<string, (typeof executions)[number]>()
  for (const e of executions) {
    const cur = latest.get(e.step_id)
    if (!cur || (e.attempt ?? 1) >= (cur.attempt ?? 1)) latest.set(e.step_id, e)
  }
  steps.forEach((s, i) => {
    const e = latest.get(s.id)
    const status = e?.status ?? (run.failed_at_step === s.id ? "failed" : "not run")
    const attempts = e?.attempt && e.attempt > 1 ? ` · ${e.attempt} attempts` : ""
    lines.push(`${i + 1}. ${s.name || s.id} (\`${s.id}\`${s.type ? `, ${s.type}` : ""}) — ${status}${attempts}`)
    if (e?.error) lines.push(`   - Error: ${clip(e.error, 400)}`)
    const out = run.step_outputs?.[s.id]
    if (out != null && out !== "") lines.push(`   - Output: ${clip(typeof out === "string" ? out : JSON.stringify(out), 400)}`)
  })
  if (run.error_message) {
    lines.push("", "## Error", "", "```", clip(run.error_message, 2000), "```")
  }
  if (run.output) lines.push("", "## Result", "", clip(run.output, 2000))
  return lines.join("\n") + "\n"
}

/* ----------------------------------------------------------- import */

export interface ImportedRoutine {
  slug: string
  name: string
  description: string
  definition: Record<string, unknown>
}

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v)

/**
 * The definition inside an exported bundle (`{format, pipeline: {slug, name, definition}}`)
 * or a bare definition (`{name, steps, …}`). Import saves it as a DRAFT: the
 * import endpoint publishes, and an imported routine should be read, tested and
 * published like any other change.
 */
export function parseImportedRoutine(text: string): { ok: true; routine: ImportedRoutine } | { ok: false; error: string } {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    return { ok: false, error: "This is not valid JSON. Paste an exported bundle or a routine definition." }
  }
  if (!isObj(parsed)) return { ok: false, error: "Expected one routine: an object with a definition or steps." }
  // The export endpoint wraps the routine: {format, pipeline: {slug, name, definition}, metadata}.
  const src: Record<string, unknown> = isObj(parsed.pipeline) ? parsed.pipeline : parsed
  let definition: Record<string, unknown> | null = null
  if (isObj(src.definition)) definition = src.definition
  else if (Array.isArray(src.steps)) definition = src
  if (!definition) return { ok: false, error: "No routine definition found. A bundle carries one under “definition”." }
  if (!Array.isArray(definition.steps) || definition.steps.length === 0) return { ok: false, error: "The definition has no steps." }
  const str = (v: unknown) => (typeof v === "string" ? v.trim() : "")
  const slug = str(src.slug) || str(definition.name)
  // A bare definition's `name` is its slug; the title is `display_name`.
  const name = (src !== definition ? str(src.name) : "") || str(definition.display_name) || slug
  return {
    ok: true,
    routine: {
      slug,
      name,
      description: str(src.description) || str(definition.description),
      definition,
    },
  }
}

/* ---------------------------------------------------------- step editor */

/**
 * The routine page's step editor reuses the wizard's step model. Loading a
 * definition reads each top-level step back into it (a step it cannot edit
 * stays raw); saving writes a step back through the wizard ONLY when it was
 * changed, so an untouched step keeps every key it had — the server drops
 * keys it does not know, and a round trip must not rewrite a routine nobody
 * edited.
 */
export function editorSteps(definition: Record<string, unknown>): WizardStep[] {
  const steps = Array.isArray(definition.steps) ? definition.steps.filter(isObj) : []
  return steps.map((s, i) => stepFromDsl(s, String(i)))
}

export function definitionWithSteps(definition: Record<string, unknown>, steps: WizardStep[]): Record<string, unknown> {
  const originals = new Map<string, Record<string, unknown>>()
  for (const s of Array.isArray(definition.steps) ? definition.steps.filter(isObj) : []) {
    if (typeof s.id === "string") originals.set(s.id, s)
  }
  const out = steps.map((step) => {
    const orig = originals.get(step.id)
    if (orig && JSON.stringify(stepFromDsl(orig, step.id)) === JSON.stringify(step)) return orig
    return stepToDsl(step)
  })
  const next: Record<string, unknown> = { ...definition, steps: out }
  // A web call adds its host to the routine's declared egress, as the wizard does.
  const hosts = steps
    .filter((s) => s.kind === "http" && s.url && !s.url.includes("{{"))
    .map((s) => {
      try {
        return new URL(s.url as string).hostname
      } catch {
        return ""
      }
    })
    .filter(Boolean)
  if (hosts.length) {
    const declared = Array.isArray(definition.egress_targets) ? (definition.egress_targets as string[]) : []
    next.egress_targets = Array.from(new Set([...declared, ...hosts]))
  }
  return next
}

/** Steps that read another step's result (`{{ steps.<id>… }}` or `needs`). */
export function stepsReferencing(steps: WizardStep[], id: string): WizardStep[] {
  const needle = `steps.${id}`
  return steps.filter((s) => {
    if (s.id === id) return false
    const raw = JSON.stringify(s.kind === "other" ? s.raw ?? {} : stepToDsl(s))
    let at = raw.indexOf(needle)
    while (at >= 0) {
      const next = raw.charAt(at + needle.length)
      if (!/[A-Za-z0-9_-]/.test(next)) return true
      at = raw.indexOf(needle, at + 1)
    }
    const needs = s.raw && Array.isArray(s.raw.needs) ? (s.raw.needs as unknown[]) : []
    return needs.includes(id)
  })
}

export function moveStep<T>(list: T[], from: number, to: number): T[] {
  if (from === to || from < 0 || to < 0 || from >= list.length || to >= list.length) return list
  const next = list.slice()
  const [item] = next.splice(from, 1)
  next.splice(to, 0, item)
  return next
}
