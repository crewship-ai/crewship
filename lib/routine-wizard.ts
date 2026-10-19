// The New routine wizard's model, and the one place it turns into a routine
// definition the server accepts.
//
// Pure and in lib for the same reason as routine-save-payload: a definition
// that is wrong is invisible until the server refuses it, and the server is
// strict in ways a form does not show — unknown keys are dropped without an
// error, `name` must be slug-shaped, an agent must belong to the routine's
// crew, a notify step needs a recipient. Every field below maps to a field
// of the routine DSL (internal/pipeline/types.go); nothing here is stored
// anywhere else.

import type { RoutineInputSpec } from "./routine-inputs"
import { slugify } from "./routine-save-payload"

/** The server's shape for a routine name (internal/pipeline/dsl.go). */
export const SLUG_RE = /^[a-z0-9][a-z0-9_-]{0,63}$/

/** Steps run as agents of one crew; the wizard reads these two fields. */
export interface WizardAgent {
  id: string
  name: string
  slug: string
  agent_role: string
  crew_id: string | null
}

export type SchedulePreset = "hour" | "day" | "weekdays" | "week" | "month" | "custom"
export type TriggerKind = "manual" | "schedule" | "once" | "app"

export interface WizardTrigger {
  kind: TriggerKind
  preset: SchedulePreset
  /** Minute past the hour, for "Every hour". */
  minute: number
  /** HH:MM, for every other preset. */
  time: string
  /** 0 = Sunday … 6 = Saturday, cron's own numbering. */
  weekday: number
  /** Only read for "Custom (cron)". */
  cron: string
  timezone: string
  catchup: "skip" | "once" | "all"
  maxFailures: number
  /** ISO timestamp for a one-time start. */
  fireAt: string
}

export type StepKind = "agent" | "ask" | "notify" | "wait" | "issue" | "panel" | "http" | "sub" | "other"
export type Effort = "trivial" | "fast" | "moderate" | "smart"

export interface WizardStep {
  /** The DSL step id. Stable once given: later steps refer to it. */
  id: string
  kind: StepKind
  name: string
  // agent_run
  agentSlug?: string
  prompt?: string
  effort?: Effort
  onFail?: "retry" | "abort" | "escalate"
  timeoutMin?: number
  // wait · approval
  question?: string
  shows?: string
  risk?: "normal" | "destructive"
  timeoutHours?: number
  // notify
  to?: string
  priority?: "urgent" | "high" | "medium" | "low"
  message?: string
  // wait · datetime / event
  waitKind?: "datetime" | "event"
  until?: string
  eventType?: string
  // crewship · issue.*
  issueAction?: "issue.create" | "issue.comment"
  title?: string
  identifier?: string
  body?: string
  // crewship · page.write (an artifact panel)
  pageSlug?: string
  panelId?: string
  /** The panel payload as typed: a JSON object. */
  panelData?: string
  // http
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE"
  url?: string
  credentialType?: string
  // call_pipeline
  pipelineSlug?: string
  /** "name = value" per line. */
  passInputs?: string
  /** A step the wizard cannot edit (code, transform, query, foreach,
   *  script…), kept byte-for-byte from the routine it was copied from. */
  raw?: Record<string, unknown>
}

export interface WizardState {
  name: string
  slug: string
  description: string
  crewId: string | null
  trigger: WizardTrigger
  slash: boolean
  inputs: RoutineInputSpec[]
  steps: WizardStep[]
  /** Dollars as typed; empty = no cap. */
  maxCost: string
  /** queue: wait in line; parallel: side by side; skip: drop the new start while one runs. */
  concurrency: "queue" | "parallel" | "skip"
  sideBySide: number
  /** Top-level definition fields a copied routine carries that the wizard
   *  does not edit (execution tier, outputs, credentials…). */
  extra: Record<string, unknown>
}

export const STEP_KINDS: { kind: Exclude<StepKind, "other">; label: string; dsl: string; hint: string }[] = [
  { kind: "agent", label: "Agent does a task", dsl: "agent_run", hint: "An agent from the crew follows your instructions" },
  { kind: "ask", label: "Ask a person", dsl: "wait · approval", hint: "The run pauses until a Manager approves" },
  { kind: "notify", label: "Notify people", dsl: "notify", hint: "An Inbox item for a role or the whole workspace" },
  { kind: "wait", label: "Wait", dsl: "wait · datetime / event", hint: "Pause until a time or an event" },
  { kind: "issue", label: "Create or comment on an issue", dsl: "crewship · issue.*", hint: "Open an issue or comment on one" },
  { kind: "panel", label: "Update an artifact panel", dsl: "crewship · page.write", hint: "Push fresh data into a panel of a page this routine produces" },
  { kind: "http", label: "Call a web API", dsl: "http", hint: "A request to an outside URL · needs a Manager review" },
  { kind: "sub", label: "Run another routine", dsl: "call_pipeline", hint: "Reuse a routine as one step" },
]

const KIND_PREFIX: Record<StepKind, string> = {
  agent: "task",
  ask: "ask",
  notify: "notify",
  wait: "wait",
  issue: "issue",
  panel: "panel",
  http: "call",
  sub: "sub",
  other: "step",
}

const DEFAULT_NAME: Record<StepKind, string> = {
  agent: "Agent task",
  ask: "Ask a person",
  notify: "Notify people",
  wait: "Wait",
  issue: "Create an issue",
  panel: "Update a panel",
  http: "Call a web API",
  sub: "Run another routine",
  other: "Step",
}

export function newStep(kind: StepKind, existing: WizardStep[]): WizardStep {
  const prefix = KIND_PREFIX[kind]
  let n = 1
  while (existing.some((s) => s.id === `${prefix}${n}`)) n++
  const base: WizardStep = { id: `${prefix}${n}`, kind, name: DEFAULT_NAME[kind] }
  switch (kind) {
    case "agent":
      return { ...base, agentSlug: "", prompt: "", effort: "moderate", onFail: "retry" }
    case "ask":
      return { ...base, question: "", shows: "", risk: "normal", timeoutHours: 24 }
    case "notify":
      return { ...base, to: "workspace", priority: "medium", message: "" }
    case "wait":
      return { ...base, waitKind: "datetime", until: "", eventType: "" }
    case "issue":
      return { ...base, issueAction: "issue.create", title: "", body: "", identifier: "" }
    case "panel":
      return { ...base, pageSlug: "", panelId: "", panelData: "{}" }
    case "http":
      return { ...base, method: "POST", url: "", credentialType: "" }
    case "sub":
      return { ...base, pipelineSlug: "", passInputs: "" }
    default:
      return base
  }
}

/** What a step still needs before it can be tested. Empty = complete. */
export function stepProblems(step: WizardStep): string[] {
  const out: string[] = []
  const blank = (v?: string) => !v || !v.trim()
  switch (step.kind) {
    case "agent":
      if (blank(step.agentSlug)) out.push("Pick an agent")
      if (blank(step.prompt)) out.push("Write the instructions")
      break
    case "ask":
      if (blank(step.question)) out.push("Write the question")
      if (blank(step.shows)) out.push("Say what they see")
      break
    case "notify":
      if (blank(step.to)) out.push("Pick who hears about it")
      if (blank(step.message)) out.push("Write the message")
      break
    case "wait":
      if (step.waitKind === "event" ? blank(step.eventType) : blank(step.until)) out.push(step.waitKind === "event" ? "Name the event" : "Pick the time")
      break
    case "issue":
      if (step.issueAction === "issue.comment") {
        if (blank(step.identifier)) out.push("Name the issue")
        if (blank(step.body)) out.push("Write the comment")
      } else if (blank(step.title)) out.push("Write the title")
      break
    case "panel":
      if (blank(step.pageSlug)) out.push("Name the page")
      if (blank(step.panelId)) out.push("Name the panel")
      if (!parseObject(step.panelData)) out.push("Write the panel data as a JSON object")
      break
    case "http":
      if (blank(step.url)) out.push("Enter the URL")
      break
    case "sub":
      if (blank(step.pipelineSlug)) out.push("Pick the routine to run")
      break
  }
  return out
}

/** A JSON object from text, or null (arrays, scalars and bad JSON). */
function parseObject(text: string | undefined): Record<string, unknown> | null {
  try {
    const v = JSON.parse(text ?? "")
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : null
  } catch {
    return null
  }
}

function parsePairs(text: string | undefined): Record<string, string> | undefined {
  if (!text?.trim()) return undefined
  const out: Record<string, string> = {}
  for (const line of text.split("\n")) {
    const i = line.indexOf("=")
    if (i < 1) continue
    const key = line.slice(0, i).trim()
    if (key) out[key] = line.slice(i + 1).trim()
  }
  return Object.keys(out).length ? out : undefined
}

function pairsText(inputs: unknown): string {
  if (!inputs || typeof inputs !== "object") return ""
  return Object.entries(inputs as Record<string, unknown>)
    .map(([k, v]) => `${k} = ${typeof v === "string" ? v : JSON.stringify(v)}`)
    .join("\n")
}

/** One wizard step → one DSL step. */
export function stepToDsl(step: WizardStep): Record<string, unknown> {
  if (step.kind === "other") return step.raw ?? { id: step.id, type: "transform" }
  const head = { id: step.id, ...(step.name.trim() ? { name: step.name.trim() } : {}) }
  switch (step.kind) {
    case "agent":
      return {
        ...head,
        type: "agent_run",
        agent_slug: step.agentSlug ?? "",
        prompt: step.prompt ?? "",
        complexity: step.effort ?? "moderate",
        ...(step.onFail === "retry" ? { retry: { max_attempts: 3 } } : {}),
        ...(step.onFail === "abort" ? { on_fail: "abort" } : {}),
        ...(step.onFail === "escalate" ? { on_fail: "escalate_tier" } : {}),
        ...(step.timeoutMin ? { timeout_seconds: Math.round(step.timeoutMin * 60) } : {}),
      }
    case "ask":
      return {
        ...head,
        type: "wait",
        wait: {
          kind: "approval",
          approval_title: step.question ?? "",
          approval_prompt: step.shows ?? "",
          risk_level: step.risk ?? "normal",
          ...(step.timeoutHours ? { timeout_sec: Math.round(step.timeoutHours * 3600) } : {}),
        },
      }
    case "notify":
      return {
        ...head,
        type: "notify",
        notify: {
          to: step.to ?? "workspace",
          title: step.name.trim() || DEFAULT_NAME.notify,
          body: step.message ?? "",
          ...(step.priority ? { priority: step.priority } : {}),
        },
      }
    case "wait":
      return {
        ...head,
        type: "wait",
        wait: step.waitKind === "event" ? { kind: "event", event_type: step.eventType ?? "" } : { kind: "datetime", until: step.until ?? "" },
      }
    case "issue":
      return {
        ...head,
        type: "crewship",
        action: step.issueAction ?? "issue.create",
        args:
          step.issueAction === "issue.comment"
            ? { identifier: step.identifier ?? "", body: step.body ?? "" }
            : { title: step.title ?? "", ...(step.body?.trim() ? { description: step.body } : {}) },
      }
    case "panel":
      return {
        ...head,
        type: "crewship",
        action: "page.write",
        args: { page: step.pageSlug ?? "", panel: step.panelId ?? "", data: parseObject(step.panelData) ?? {} },
      }
    case "http":
      return {
        ...head,
        type: "http",
        http: {
          method: step.method ?? "GET",
          url: step.url ?? "",
          ...(step.credentialType?.trim() ? { credential_ref: { type: step.credentialType.trim(), inject_as: "bearer" } } : {}),
        },
      }
    case "sub": {
      const inputs = parsePairs(step.passInputs)
      return { ...head, type: "call_pipeline", pipeline_slug: step.pipelineSlug ?? "", ...(inputs ? { inputs } : {}) }
    }
  }
}

const str = (v: unknown): string => (typeof v === "string" ? v : "")
const EFFORTS: Effort[] = ["trivial", "fast", "moderate", "smart"]

/** One DSL step → a wizard step. A step it cannot edit stays raw. */
export function stepFromDsl(raw: Record<string, unknown>, _key: string): WizardStep {
  const id = str(raw.id) || "step1"
  const name = str(raw.name)
  const base = { id, name }
  const wait = (raw.wait ?? {}) as Record<string, unknown>
  // Only the fields the wizard writes are read back; anything else on a step
  // means the wizard would drop it, so the step is kept as it was instead.
  const known = (keys: string[]) => Object.keys(raw).every((k) => keys.includes(k))
  switch (raw.type) {
    case "agent_run": {
      if (!known(["id", "name", "type", "agent_slug", "prompt", "complexity", "retry", "on_fail", "timeout_seconds"])) break
      // The wizard writes exactly one of: retry 3 times, stop, or a stronger
      // model. Any other retry policy is kept as it was.
      let onFail: WizardStep["onFail"]
      if (raw.retry !== undefined) {
        if (raw.on_fail !== undefined || JSON.stringify(raw.retry) !== '{"max_attempts":3}') break
        onFail = "retry"
      } else if (raw.on_fail === undefined || raw.on_fail === "abort") onFail = "abort"
      else if (raw.on_fail === "escalate_tier") onFail = "escalate"
      else break
      const effort: Effort | null =
        raw.complexity === undefined ? "moderate" : EFFORTS.includes(raw.complexity as Effort) ? (raw.complexity as Effort) : null
      if (effort == null) break
      return {
        ...base,
        kind: "agent",
        agentSlug: str(raw.agent_slug),
        prompt: str(raw.prompt),
        effort,
        onFail,
        ...(typeof raw.timeout_seconds === "number" ? { timeoutMin: raw.timeout_seconds / 60 } : {}),
      }
    }
    case "wait": {
      if (!known(["id", "name", "type", "wait"])) break
      if (wait.kind === "approval" && Object.keys(wait).every((k) => ["kind", "approval_title", "approval_prompt", "risk_level", "timeout_sec"].includes(k))) {
        return {
          ...base,
          kind: "ask",
          question: str(wait.approval_title),
          shows: str(wait.approval_prompt),
          risk: wait.risk_level === "destructive" ? "destructive" : "normal",
          ...(typeof wait.timeout_sec === "number" ? { timeoutHours: wait.timeout_sec / 3600 } : {}),
        }
      }
      if (wait.kind === "datetime" && known(["id", "name", "type", "wait"]) && Object.keys(wait).every((k) => ["kind", "until"].includes(k)))
        return { ...base, kind: "wait", waitKind: "datetime", until: str(wait.until), eventType: "" }
      if (wait.kind === "event" && Object.keys(wait).every((k) => ["kind", "event_type"].includes(k)))
        return { ...base, kind: "wait", waitKind: "event", eventType: str(wait.event_type), until: "" }
      break
    }
    case "notify": {
      const n = (raw.notify ?? {}) as Record<string, unknown>
      if (!known(["id", "name", "type", "notify"]) || !Object.keys(n).every((k) => ["to", "title", "body", "priority"].includes(k))) break
      if (str(n.title) !== (name.trim() || DEFAULT_NAME.notify)) break
      return { ...base, kind: "notify", to: str(n.to), message: str(n.body), priority: (str(n.priority) || undefined) as WizardStep["priority"] }
    }
    case "crewship": {
      const args = (raw.args ?? {}) as Record<string, unknown>
      if (!known(["id", "name", "type", "action", "args"])) break
      if (raw.action === "issue.create" && Object.keys(args).every((k) => ["title", "description"].includes(k)))
        return { ...base, kind: "issue", issueAction: "issue.create", title: str(args.title), body: str(args.description), identifier: "" }
      if (raw.action === "issue.comment" && Object.keys(args).every((k) => ["identifier", "body"].includes(k)))
        return { ...base, kind: "issue", issueAction: "issue.comment", identifier: str(args.identifier), body: str(args.body), title: "" }
      if (raw.action === "page.write" && Object.keys(args).every((k) => ["page", "panel", "data"].includes(k)) && args.data && typeof args.data === "object" && !Array.isArray(args.data))
        return { ...base, kind: "panel", pageSlug: str(args.page), panelId: str(args.panel), panelData: JSON.stringify(args.data, null, 2) }
      break
    }
    case "http": {
      const h = (raw.http ?? {}) as Record<string, unknown>
      if (!known(["id", "name", "type", "http"]) || !Object.keys(h).every((k) => ["method", "url", "credential_ref"].includes(k))) break
      const cred = h.credential_ref as { type?: string; inject_as?: string } | undefined
      if (cred && (cred.inject_as !== "bearer" || Object.keys(cred).length !== 2)) break
      return { ...base, kind: "http", method: (str(h.method).toUpperCase() || "GET") as WizardStep["method"], url: str(h.url), credentialType: cred?.type ?? "" }
    }
    case "call_pipeline": {
      if (!known(["id", "name", "type", "pipeline_slug", "inputs"])) break
      const inputs = raw.inputs as Record<string, unknown> | undefined
      if (inputs && Object.values(inputs).some((v) => typeof v !== "string")) break
      return { ...base, kind: "sub", pipelineSlug: str(raw.pipeline_slug), passInputs: pairsText(inputs) }
    }
  }
  return { ...base, kind: "other", raw }
}

/**
 * What a step may insert: the routine's inputs and the outputs of the steps
 * ABOVE it. A reference to a step that has not run yet is refused at save.
 */
export function insertableRefs(steps: WizardStep[], index: number, inputs: RoutineInputSpec[]): string[] {
  if (index <= 0 && inputs.length === 0) return []
  return [
    ...inputs.map((i) => `{{ inputs.${i.name} }}`),
    ...steps.slice(0, Math.max(0, index)).map((s) => `{{ steps.${s.id}.output }}`),
  ]
}

function hostOf(url: string | undefined): string | null {
  if (!url || url.includes("{{")) return null
  try {
    return new URL(url).hostname || null
  } catch {
    return null
  }
}

/** The wizard → the routine definition sent to test_run and save. */
export function buildDefinition(s: WizardState): Record<string, unknown> {
  // The copied routine's own identity never wins over the new one.
  const extra = Object.fromEntries(
    Object.entries(s.extra).filter(([k]) => !["dsl_version", "name", "display_name", "description", "inputs", "steps", "max_cost_usd", "concurrency_key", "max_concurrent", "concurrency_policy", "slash", "egress_targets"].includes(k)),
  )
  const cost = Number(s.maxCost)
  const hosts = s.steps.filter((st) => st.kind === "http").map((st) => hostOf(st.url)).filter((h): h is string => !!h)
  const declared = Array.isArray(s.extra.egress_targets) ? (s.extra.egress_targets as string[]) : []
  const egress = Array.from(new Set([...declared, ...hosts]))
  return {
    dsl_version: "1.0",
    name: s.slug,
    display_name: s.name.trim(),
    ...(s.description.trim() ? { description: s.description.trim() } : {}),
    ...extra,
    ...(s.inputs.length ? { inputs: s.inputs } : {}),
    steps: s.steps.map(stepToDsl),
    ...(s.maxCost.trim() && Number.isFinite(cost) && cost > 0 ? { max_cost_usd: cost } : {}),
    concurrency_key: s.slug,
    max_concurrent: s.concurrency === "parallel" ? Math.max(2, Math.round(s.sideBySide)) : 1,
    ...(s.concurrency === "skip" ? { concurrency_policy: "skip" } : {}),
    ...(s.slash ? { slash: { enabled: true, label: s.name.trim() } } : {}),
    ...(egress.length ? { egress_targets: egress } : {}),
  }
}

/** True when the server will land this as `proposed` for a Manager review
 *  (internal/pipeline/risk.go): a web call, code, outside hosts, credentials. */
export function requiresReview(definition: Record<string, unknown>): boolean {
  const steps = Array.isArray(definition.steps) ? (definition.steps as { type?: string }[]) : []
  if (steps.some((st) => st.type === "http" || st.type === "code")) return true
  const nonEmpty = (v: unknown) => Array.isArray(v) && v.length > 0
  return nonEmpty(definition.egress_targets) || nonEmpty(definition.credentials_required)
}

const DAY_NAMES = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"]

function hm(time: string): [number, number] {
  const [h, m] = time.split(":").map((x) => Number.parseInt(x, 10))
  return [Number.isFinite(h) ? h : 0, Number.isFinite(m) ? m : 0]
}

/** The schedule's cron, from a preset or as typed. */
export function cronFor(t: WizardTrigger): string {
  if (t.preset === "custom") return t.cron.trim()
  if (t.preset === "hour") return `${t.minute} * * * *`
  const [h, m] = hm(t.time)
  switch (t.preset) {
    case "day":
      return `${m} ${h} * * *`
    case "weekdays":
      return `${m} ${h} * * 1-5`
    case "week":
      return `${m} ${h} * * ${t.weekday}`
    case "month":
      return `${m} ${h} 1 * *`
  }
}

/** The trigger in words, for the summary column and step 3's header. */
export function whenText(t: WizardTrigger): string {
  switch (t.kind) {
    case "manual":
      return "When someone starts it"
    case "app":
      return "When an app calls (added after saving)"
    case "once":
      return t.fireAt ? `Once · ${new Date(t.fireAt).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}` : "Once · pick a time"
    case "schedule":
      switch (t.preset) {
        case "hour":
          return `Every hour at :${String(t.minute).padStart(2, "0")}`
        case "day":
          return `Every day at ${t.time}`
        case "weekdays":
          return `Weekdays at ${t.time}`
        case "week":
          return `Every ${DAY_NAMES[t.weekday]} at ${t.time}`
        case "month":
          return `1st of every month at ${t.time}`
        case "custom":
          return `Cron ${t.cron.trim() || "…"}`
      }
  }
}

/** Why a one-time start is not acceptable yet, or null. */
export function onceProblem(fireAt: string, now = new Date()): string | null {
  if (!fireAt) return "Pick a date and time"
  const at = new Date(fireAt).getTime()
  if (!Number.isFinite(at)) return "Pick a date and time"
  if (at <= now.getTime()) return "Pick a time in the future"
  if (at > now.getTime() + 2 * 365 * 86_400_000) return "Pick a time within the next two years"
  return null
}

/** Defaults are what a scheduled start runs with. */
function inputDefaults(inputs: RoutineInputSpec[]): Record<string, unknown> | undefined {
  const out: Record<string, unknown> = {}
  for (const i of inputs) if (i.default !== undefined && i.default !== "") out[i.name] = i.default
  return Object.keys(out).length ? out : undefined
}

/** The atomic trigger for POST /pipelines/save: manual | schedule | once. A
 *  webhook or an event cannot be created in the same save, so it is saved as
 *  manual and added on the routine page afterwards. */
export function buildTrigger(t: WizardTrigger, inputs: RoutineInputSpec[]): Record<string, unknown> {
  if (t.kind === "schedule") {
    const presets = inputDefaults(inputs)
    return {
      kind: "schedule",
      cron: cronFor(t),
      timezone: t.timezone,
      catchup_policy: t.catchup,
      max_consecutive_failures: t.maxFailures,
      ...(presets ? { inputs: presets } : {}),
    }
  }
  if (t.kind === "once") return { kind: "once", fire_at: t.fireAt }
  return { kind: "manual" }
}

/** The sample inputs a test run uses: each input's default. */
export function sampleInputs(inputs: RoutineInputSpec[]): Record<string, unknown> {
  return inputDefaults(inputs) ?? {}
}

export function buildSaveBody(
  s: WizardState,
  definition: Record<string, unknown>,
  saveToken: string,
  leadAgentId?: string | null,
): Record<string, unknown> {
  const comment = s.steps.some((st) => st.kind === "issue" && st.issueAction === "issue.comment")
  return {
    slug: s.slug,
    name: s.name.trim(),
    description: s.description.trim(),
    definition,
    ...(s.crewId ? { author_crew_id: s.crewId } : {}),
    // A comment is filed under an agent; the routine acts as its crew lead.
    ...(comment && leadAgentId ? { author_agent_id: leadAgentId } : {}),
    save_token: saveToken,
    trigger: buildTrigger(s.trigger, s.inputs),
    change_summary: "initial version",
  }
}

/** The document of a new draft (POST /pipelines/drafts). A draft has no
 *  start: triggers are created when it is published. */
export function buildDraftDocument(
  s: WizardState,
  definition: Record<string, unknown>,
  appearance?: { icon?: string | null; color?: string | null },
): Record<string, unknown> {
  return {
    slug: s.slug,
    name: s.name.trim(),
    description: s.description.trim(),
    definition,
    ...(s.crewId ? { author_crew_id: s.crewId } : {}),
    ...(appearance?.icon ? { icon: appearance.icon } : {}),
    ...(appearance?.color ? { color: appearance.color } : {}),
  }
}

export interface ValidationProblem {
  path: string
  message: string
  /** 0-based step index when the path points into `steps`. */
  stepIndex: number | null
}

/** The server flattens every validation error into one string
 *  (`N validation errors:\n  - /steps/0/agent_slug: …`). Split it back so
 *  each problem can point at the step it is about. */
export function parseValidationErrors(message: string): ValidationProblem[] {
  const lines = message.split("\n").map((l) => l.trim()).filter((l) => l.startsWith("- /"))
  if (lines.length === 0) return [{ path: "", message: message.trim(), stepIndex: null }]
  return lines.map((line) => {
    const body = line.slice(2)
    const i = body.indexOf(": ")
    const path = i > 0 ? body.slice(0, i) : body
    const m = /^\/steps\/(\d+)/.exec(path)
    return { path, message: i > 0 ? body.slice(i + 2) : "", stepIndex: m ? Number(m[1]) : null }
  })
}

/** Copy: steps, inputs and limits of an existing definition. Name, triggers,
 *  history and access are not copied. */
export function wizardFromDefinition(def: Record<string, unknown>): Pick<WizardState, "steps" | "inputs" | "maxCost" | "concurrency" | "sideBySide" | "extra" | "slash"> {
  const steps = Array.isArray(def.steps) ? (def.steps as Record<string, unknown>[]) : []
  const max = typeof def.max_concurrent === "number" ? def.max_concurrent : 1
  const extra = Object.fromEntries(
    Object.entries(def).filter(([k]) => !["dsl_version", "name", "display_name", "description", "inputs", "steps", "max_cost_usd", "concurrency_key", "max_concurrent", "concurrency_policy", "slash"].includes(k)),
  )
  return {
    steps: steps.map((st, i) => stepFromDsl(st, String(i))),
    inputs: Array.isArray(def.inputs) ? (def.inputs as RoutineInputSpec[]) : [],
    maxCost: typeof def.max_cost_usd === "number" ? String(def.max_cost_usd) : "",
    concurrency: max > 1 ? "parallel" : def.concurrency_policy === "skip" ? "skip" : "queue",
    sideBySide: max > 1 ? max : 3,
    extra,
    slash: !!(def.slash && typeof def.slash === "object" && (def.slash as { enabled?: boolean }).enabled),
  }
}

/** The lead of a crew: its LEAD agent, else any agent in it. */
export function leadOf<T extends WizardAgent>(agents: T[], crewId: string | null): T | null {
  if (!crewId) return null
  const inCrew = agents.filter((a) => a.crew_id === crewId)
  return inCrew.find((a) => a.agent_role === "LEAD") ?? inCrew[0] ?? null
}

/** What "Describe it" sends to the crew lead's chat, in the person's name —
 *  so it is the person's own words. How to author a routine is the lead's
 *  skill, not something to paste into a message someone else signs. */
export function describeMessage(name: string, goal: string): string {
  const title = name.trim() ? ` called "${name.trim()}"` : ""
  return `Please draft a routine${title}: ${goal.trim()}\n\nSave it as a draft so I can review it before it goes live.`
}

/** The real CLI path for a new routine (cmd/crewship: routine init, validate,
 *  save — save runs the same test gate as the web). */
export function describeCliCommands(slug: string, name: string, crew: string) {
  const file = `${slug || "my-routine"}.json`
  return [
    { step: "Start from a skeleton (or copy one with --from <slug>):", command: `crewship routine init -o ${file}` },
    { step: "Edit the steps, then check the file without running anything:", command: `crewship routine validate ${file}` },
    {
      step: "Test it and save it. Add --cron and --timezone to start it on a schedule:",
      command: `crewship routine save --name "${name.trim() || "My routine"}" --definition ${file} --author-crew ${crew || "<crew>"}`,
    },
  ]
}

/** A slug from the name, cut to the server's length. */
export function slugFromName(name: string): string {
  return slugify(name).slice(0, 64).replace(/-+$/, "")
}
