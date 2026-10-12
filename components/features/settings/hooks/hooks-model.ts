/**
 * Settings › Lifecycle hooks — the event catalogue and the pure helpers the
 * page and the editor share.
 *
 * The events mirror internal/hooks/types.go `AllEvents` (the 14 a write may
 * name); which of them can cancel what they announce mirrors
 * `Event.SupportsBlocking()`. Read blocking off the EVENT, never off a row's
 * `blocking` column: nothing rejects `blocking: true` on a post-event at write
 * time, and rendering that as "can block" would promise a veto the dispatcher
 * never asks for.
 */

export type HookEventGroup = "delegation" | "agent" | "tools" | "llm" | "memory" | "peers" | "policy"

export interface HookEventInfo {
  id: string
  group: HookEventGroup
  /** What happens, in words, for the list and the editor. */
  label: string
  /** When exactly it fires — one line. */
  when: string
  /** The call site can still cancel the operation (pre_* with a veto). */
  canBlock: boolean
  /** The payload carries a severity a matcher can filter on. */
  hasSeverity?: boolean
  /** The payload carries a tool name a matcher can filter on. */
  hasTool?: boolean
}

export const HOOK_GROUPS: { id: HookEventGroup; label: string }[] = [
  { id: "delegation", label: "Task delegation" },
  { id: "agent", label: "Agent lifecycle" },
  { id: "tools", label: "Tool calls" },
  { id: "llm", label: "Model calls" },
  { id: "memory", label: "Memory writes" },
  { id: "peers", label: "Agent conversations" },
  { id: "policy", label: "Policy alerts" },
]

export const HOOK_EVENTS: HookEventInfo[] = [
  { id: "pre_task_delegation", group: "delegation", label: "Before a task is handed off", when: "Before a delegated task runs — nothing has been spent yet.", canBlock: true },
  { id: "post_task_delegation", group: "delegation", label: "After a task is handed off", when: "Once the hand-off is recorded.", canBlock: false },
  { id: "pre_agent_start", group: "agent", label: "Before an agent starts", when: "Before an agent run begins.", canBlock: true },
  { id: "post_agent_stop", group: "agent", label: "After an agent stops", when: "When an agent run ends, however it ended.", canBlock: false },
  { id: "post_tool_call", group: "tools", label: "After a tool runs", when: "After an agent's tool call — there is no 'before', the CLI has already run it.", canBlock: false, hasTool: true },
  { id: "pre_llm_call", group: "llm", label: "Before every model call", when: "Before a server-side model call; can refuse it before it costs anything.", canBlock: true },
  { id: "post_llm_call", group: "llm", label: "After every model call", when: "After a server-side model call, with its cost.", canBlock: false },
  { id: "pre_memory_write", group: "memory", label: "Before learned rules are written", when: "Before the consolidator appends rules to a crew's memory.", canBlock: true },
  { id: "post_memory_write", group: "memory", label: "After learned rules are written", when: "After the consolidator appends rules to a crew's memory.", canBlock: false },
  { id: "pre_peer_conversation", group: "peers", label: "Before agents talk", when: "Before one agent asks another a question.", canBlock: true },
  { id: "post_peer_conversation", group: "peers", label: "After agents talk", when: "When a question between agents is answered.", canBlock: false },
  { id: "on_approval_requested", group: "policy", label: "An agent waits for approval", when: "When a human is asked to approve — before it lands in the queue.", canBlock: false },
  { id: "on_budget_exceeded", group: "policy", label: "A budget is exceeded", when: "Once per breach, when a budget first goes over.", canBlock: false },
  { id: "on_guardrail_triggered", group: "policy", label: "A guardrail trips", when: "On every guardrail finding (e.g. a jailbreak attempt), with its severity.", canBlock: false, hasSeverity: true },
]

const EVENT_BY_ID = new Map(HOOK_EVENTS.map((e) => [e.id, e]))

/** Rows predating a decision to stop offering an event: they list and toggle,
 *  and can never fire. */
export const RETIRED_EVENTS = new Set(["pre_tool_call"])

export function eventInfo(id: string): HookEventInfo {
  return EVENT_BY_ID.get(id) ?? {
    id,
    group: "policy",
    label: RETIRED_EVENTS.has(id) ? "Retired event — never fires" : id,
    when: RETIRED_EVENTS.has(id) ? "This event is no longer dispatched; the hook cannot run." : "Unknown event.",
    canBlock: false,
  }
}

export const SEVERITIES = ["low", "medium", "high", "critical"] as const

export interface HookMatcher {
  tools?: string[]
  agent_ids?: string[]
  crew_ids?: string[]
  severities?: string[]
}

export interface HookRow {
  id: string
  workspace_id: string
  crew_id?: string
  event: string
  handler_kind: string
  handler_config?: Record<string, unknown>
  matcher?: HookMatcher | null
  enabled: boolean
  blocking: boolean
  created_by?: string
  created_at: string
  updated_at: string
}

export interface HookRun {
  hookId: string
  outcome: string
  ts: string
  latencyMs?: number
  message?: string
}

/** `handler_config.command` for shell, `.url` for http, `.agent_id` for
 *  subagent — the keys internal/hooks validates on write. */
export function targetOf(hook: Pick<HookRow, "handler_kind" | "handler_config">): string {
  const cfg = hook.handler_config ?? {}
  const key = hook.handler_kind === "http" ? "url" : hook.handler_kind === "subagent" ? "agent_id" : "command"
  const value = cfg[key]
  return typeof value === "string" && value !== "" ? value : "—"
}

/** A URL without its scheme and query, for a row's second line. */
export function shortTarget(hook: Pick<HookRow, "handler_kind" | "handler_config">): string {
  const t = targetOf(hook)
  if (hook.handler_kind !== "http") return t
  try {
    const u = new URL(t)
    return `${u.host}${u.pathname === "/" ? "" : u.pathname}`
  } catch {
    return t
  }
}

interface JournalEntry {
  id?: string
  ts: string
  entry_type: string
  actor_id?: string
  payload?: Record<string, unknown>
}

/** hook.fired / hook.blocked journal entries → runs per hook, newest first. */
export function runsByHook(entries: JournalEntry[]): Record<string, HookRun[]> {
  const out: Record<string, HookRun[]> = {}
  for (const e of entries) {
    const p = e.payload ?? {}
    const hookId = typeof p.hook_id === "string" ? p.hook_id : e.actor_id
    if (!hookId || Number.isNaN(Date.parse(e.ts))) continue
    const outcome = typeof p.outcome === "string" ? p.outcome : e.entry_type === "hook.blocked" ? "block" : "pass"
    const latency = typeof p.latency_ms === "number" ? p.latency_ms : undefined
    const message = typeof p.message === "string" && p.message ? p.message : undefined
    ;(out[hookId] ??= []).push({ hookId, outcome, ts: e.ts, latencyMs: latency, message })
  }
  for (const runs of Object.values(out)) runs.sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts))
  return out
}

/** pass is fine; block is a decision, not a failure; anything else failed. */
export function outcomeTone(outcome: string | undefined): "ok" | "warn" | "bad" | "none" {
  if (!outcome) return "none"
  if (outcome === "pass") return "ok"
  if (outcome === "block") return "warn"
  return "bad"
}

export interface HookFilters {
  q: string
  group: HookEventGroup | ""
  kind: "" | "http" | "shell" | "subagent"
  state: "" | "enabled" | "disabled" | "blocking" | "failing"
}
export const DEFAULT_HOOK_FILTERS: HookFilters = { q: "", group: "", kind: "", state: "" }

export function hookMatches(h: HookRow, f: HookFilters, runs: Record<string, HookRun[]>): boolean {
  const info = eventInfo(h.event)
  if (f.group && info.group !== f.group) return false
  if (f.kind && h.handler_kind !== f.kind) return false
  if (f.state === "enabled" && !h.enabled) return false
  if (f.state === "disabled" && h.enabled) return false
  if (f.state === "blocking" && !(info.canBlock && h.blocking)) return false
  if (f.state === "failing" && outcomeTone(runs[h.id]?.[0]?.outcome) !== "bad") return false
  const q = f.q.trim().toLowerCase()
  if (q && ![h.event, info.label, targetOf(h)].some((s) => s.toLowerCase().includes(q))) return false
  return true
}

/** "2m ago", "3h ago", "4d ago". */
export function ago(ts: string, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - Date.parse(ts)) / 1000))
  if (s < 60) return "just now"
  const m = Math.round(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.round(m / 60)
  if (h < 48) return `${h}h ago`
  return `${Math.round(h / 24)}d ago`
}

/** The write body for POST /hooks and PATCH /hooks/{id}. Blocking is only ever
 *  sent true for an event that can block. */
export interface HookDraft {
  event: string
  kind: "http" | "shell"
  url: string
  secret: string
  command: string
  crewId: string
  severities: string[]
  tools: string
  blocking: boolean
  enabled: boolean
}

export function draftFrom(h?: HookRow): HookDraft {
  const cfg = h?.handler_config ?? {}
  return {
    event: h?.event ?? "",
    kind: h?.handler_kind === "shell" ? "shell" : "http",
    url: typeof cfg.url === "string" ? cfg.url : "",
    secret: "",
    command: typeof cfg.command === "string" ? cfg.command : "",
    crewId: h?.crew_id ?? "",
    severities: h?.matcher?.severities ?? [],
    tools: (h?.matcher?.tools ?? []).join(", "),
    blocking: Boolean(h?.blocking),
    enabled: h?.enabled ?? true,
  }
}

export function draftProblems(d: HookDraft): string[] {
  const out: string[] = []
  if (!EVENT_BY_ID.has(d.event)) out.push("Pick when the hook runs.")
  if (d.kind === "http") {
    try {
      const u = new URL(d.url)
      if (u.protocol !== "https:" && u.protocol !== "http:") out.push("The URL must start with https:// or http://.")
    } catch {
      out.push("Enter the URL to send the event to.")
    }
  }
  if (d.kind === "shell" && !d.command.trim()) out.push("Enter the command to run.")
  return out
}

export function writeBody(d: HookDraft, editing?: HookRow): Record<string, unknown> {
  const info = eventInfo(d.event)
  const handlerConfig: Record<string, unknown> =
    d.kind === "http"
      ? { url: d.url.trim(), ...(d.secret.trim() ? { secret: d.secret.trim() } : editing?.handler_config?.secret ? { secret: editing.handler_config.secret } : {}) }
      : { command: d.command.trim() }
  const matcher: HookMatcher = {}
  if (info.hasSeverity && d.severities.length) matcher.severities = d.severities
  const tools = d.tools.split(",").map((t) => t.trim()).filter(Boolean)
  if (info.hasTool && tools.length) matcher.tools = tools
  return {
    event: d.event,
    handler_kind: d.kind,
    handler_config: handlerConfig,
    // "" is "whole workspace"; null would mean "unchanged" on a PATCH.
    crew_id: d.crewId,
    matcher,
    blocking: info.canBlock && d.blocking,
    enabled: d.enabled,
  }
}
