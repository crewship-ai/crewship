// The Journal lives in Activity now (F-02). Every `/journal…` link — bookmarks,
// Slack posts, the CLI's `crewship open journal`, the links other pages still
// build through entityHref — keeps landing, and lands on the same view.
//
// Who sees what does not change:
//
//   OWNER / ADMIN   the Journal's three tabs are three Activity views:
//                     Timeline → /activity?section=log   (the technical log)
//                     Runs     → /activity?section=runs  (fleet runs)
//                     Spend    → /activity?mode=spend
//                   Every Journal param travels verbatim, so the log and the
//                   runs ledger read them exactly as /journal did.
//   anyone else     never saw the Journal (a gate pointed them at Activity),
//                   so they land on the Activity list, narrowed by whatever
//                   the link said that the list can express. Spend is not
//                   one of those: a member's spend link opens plain Activity.
//
// The same member mapping clamps a member who opens `/activity?section=log`,
// `?section=runs` or `?mode=spend` directly — Activity is the one gate.
//
// Pure: the mapping is the contract worth testing (lib/__tests__/
// journal-redirect.test.ts), and it must not need a mounted page.

import { parseStructuredQuery } from "@/lib/log-search"

export type JournalView = "log" | "runs" | "spend"

/** The Activity section each admin-only Journal view is drawn in. */
export const JOURNAL_VIEW_SECTION = { log: "log", runs: "runs" } as const

/** Only OWNER / ADMIN — the Journal's gate (`roleAtLeast(role, "ADMIN")`), unchanged. */
export function canSeeJournalViews(role: string | null | undefined): boolean {
  return role === "OWNER" || role === "ADMIN"
}

/**
 * Which Journal view a URL names — a `/journal?tab=` link or an Activity URL
 * carrying `section=log|runs` / `mode=spend`. Null for an Activity URL that
 * names none of them.
 */
export function journalViewOf(params: URLSearchParams, pathname = "/activity"): JournalView | null {
  if (pathname.startsWith("/journal")) {
    const tab = params.get("tab")
    // An unknown tab rendered the timeline on /journal; it still does.
    return tab === "runs" ? "runs" : tab === "spend" ? "spend" : "log"
  }
  if (params.get("mode") === "spend") return "spend"
  const section = params.get("section")
  if (section === "log") return "log"
  if (section === "runs") return "runs"
  return null
}

/** Resolve a slug or identifier to the id Activity filters on. */
export interface JournalResolvers {
  crew?: (slugOrId: string) => string | undefined
  agent?: (slugOrId: string) => string | undefined
  issue?: (identifierOrId: string) => string | undefined
}

/** Keys that only mean something on the admin views; never copied to the list. */
const ROUTING_KEYS = new Set(["tab", "section", "mode"])

function url(params: URLSearchParams): string {
  const qs = params.toString()
  return qs ? `/activity?${qs}` : "/activity"
}

/** The admin target: the same view, every param as it was. */
export function adminActivityUrl(params: URLSearchParams, view: JournalView): string {
  if (view === "spend") return "/activity?mode=spend"
  const out = new URLSearchParams()
  out.set("section", JOURNAL_VIEW_SECTION[view])
  for (const [k, v] of params) if (!ROUTING_KEYS.has(k) && v !== "") out.append(k, v)
  return url(out)
}

/** Journal time windows → Activity ranges (Activity keeps 30 days). */
const TIME_TO_RANGE: Record<string, string> = {
  "5m": "1h",
  "15m": "1h",
  "1h": "1h",
  "24h": "24h",
  "7d": "7d",
  "30d": "30d",
  all: "30d",
}

/** Runs tab status → an Activity view (`view=`) or a status facet (`status=`). */
const RUN_STATUS_TO_LIST: Record<string, [key: "view" | "status", value: string]> = {
  RUNNING: ["view", "live"],
  FAILED: ["view", "failed"],
  TIMEOUT: ["view", "failed"],
  COMPLETED: ["status", "completed"],
  CANCELLED: ["status", "stopped"],
}

/** Runs tab trigger → "Work started by". SYSTEM has no counterpart. */
const RUN_TRIGGER_TO_SOURCE: Record<string, string> = {
  USER: "person",
  WEBHOOK: "webhook",
  CRON: "schedule",
  AGENT: "chat",
}

function get(params: URLSearchParams, ...keys: string[]): string | undefined {
  for (const k of keys) {
    const v = params.get(k)?.trim()
    if (v) return v
  }
  return undefined
}

/**
 * The member target: the Activity list, narrowed by what the link said.
 *
 * Carried: time window (`time`, `from`/`to`, `run_window`) → `range`;
 * crew / agent (id or slug, also from `crew:`/`agent:` search tokens) →
 * `crew` / `agent`; issue (`mission_id`) → `mission`; a run (`trace_id`) →
 * `run` (opens it in the Peek); the rest of the search → `q`; run status →
 * a view; run trigger → `source`; the Runs tab itself → `what=work`.
 *
 * Dropped, because the list has no such filter: `severity`, `mute`,
 * `run_page`, `entry_type`, the SYSTEM trigger — and everything on Spend.
 */
export function memberActivityUrl(params: URLSearchParams, view: JournalView, resolve: JournalResolvers = {}): string {
  const out = new URLSearchParams()
  if (view === "spend") return "/activity"

  if (view === "runs") {
    out.set("what", "work")
    const window = get(params, "run_window")
    if (window && window !== "24h" && TIME_TO_RANGE[window]) out.set("range", TIME_TO_RANGE[window])
    // The Runs tab defaulted to 24 hours; a link that said nothing still meant that.
    else out.set("range", "24h")
    const status = RUN_STATUS_TO_LIST[get(params, "run_status") ?? ""]
    if (status) out.set(status[0], status[1])
    const source = RUN_TRIGGER_TO_SOURCE[get(params, "run_trigger") ?? ""]
    if (source) out.set("source", source)
    return url(out)
  }

  const time = get(params, "time")
  const from = Number(params.get("from"))
  const to = Number(params.get("to"))
  if (time === "custom" && Number.isFinite(from) && Number.isFinite(to) && from > 0 && to > from) {
    out.set("range", "custom")
    out.set("from", new Date(from).toISOString())
    out.set("to", new Date(to).toISOString())
  } else if (time && TIME_TO_RANGE[time]) {
    out.set("range", TIME_TO_RANGE[time])
  }

  const structured = parseStructuredQuery(params.get("q") ?? "")
  const crew = get(params, "crew_id", "crew") ?? structured.serverParams.crew_id
  const agent = get(params, "agent_id", "agent") ?? structured.serverParams.agent_id
  const issue = get(params, "mission_id", "mission")
  const run = get(params, "trace_id") ?? structured.serverParams.trace_id
  if (crew) out.set("crew", resolve.crew?.(crew) ?? crew)
  if (agent) out.set("agent", resolve.agent?.(agent) ?? agent)
  if (issue) out.set("mission", resolve.issue?.(issue) ?? issue)
  const q = structured.clientQuery.trim()
  if (q) out.set("q", q)
  if (run) out.set("run", run)
  // `range=7d` is Activity's default; leave the URL short when it is that.
  if (out.get("range") === "7d") out.delete("range")
  return url(out)
}

/** Where a Journal link — or an admin-only Activity view — sends this reader. */
export function journalToActivity(
  params: URLSearchParams,
  opts: { admin: boolean; pathname?: string; resolve?: JournalResolvers },
): string {
  const view = journalViewOf(params, opts.pathname ?? "/journal") ?? "log"
  return opts.admin ? adminActivityUrl(params, view) : memberActivityUrl(params, view, opts.resolve)
}

interface LookupLike {
  crews: Map<string, { id: string; slug: string }>
  agents: Map<string, { id: string; slug: string }>
  missions: Map<string, { id: string; identifier?: string }>
}

/** Resolvers over the workspace lookup (hooks/use-journal-lookup): id, or slug / identifier → id. */
export function lookupResolvers(l: LookupLike): JournalResolvers {
  const find = <T extends { id: string }>(m: Map<string, T>, v: string, key: (x: T) => string | undefined) => {
    if (m.has(v)) return v
    for (const x of m.values()) if (key(x) === v) return x.id
    return undefined
  }
  return {
    crew: (v) => find(l.crews, v, (c) => c.slug),
    agent: (v) => find(l.agents, v, (a) => a.slug),
    issue: (v) => find(l.missions, v, (m) => m.identifier),
  }
}

/** Does the member mapping need the slug lookup to have loaded? */
export function needsLookup(params: URLSearchParams): boolean {
  if (get(params, "crew_id", "crew", "agent_id", "agent", "mission_id", "mission")) return true
  return /\b(agent|crew):/i.test(params.get("q") ?? "")
}
