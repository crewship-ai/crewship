"use client"

import { useCallback, useMemo, useRef } from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import type { CustomRange, TimeRange } from "@/components/features/logs/time-range-picker"
import type { SeverityFilter } from "@/components/features/logs/logs-toolbar"
import { GROUP_ORDER, type EntryGroup } from "@/lib/journal-style"
import { RUN_WINDOWS, type RunWindow } from "@/lib/runs-insights"

/**
 * The /journal URL contract.
 *
 * ## Why this is a hook and not a mirror effect
 *
 * The page used to read every URL value once, at mount — `useMemo(…, [])` and
 * lazy `useState` initialisers — and then push its own state back out through
 * an effect. That is fine for a cold load and wrong for everything else: a
 * `router.push` to the SAME pathname (a run row opening its trace, an in-app
 * link into `/journal?…`) re-renders the page without unmounting it, so the
 * frozen values never moved and the tab stayed put while the address bar
 * claimed otherwise.
 *
 * The obvious repair — re-read `searchParams` in an effect and copy it into
 * state — reintroduces the exact hazard the original author was avoiding when
 * they froze the values: state writes URL, URL writes state, and any
 * disagreement between the two encoders (a dropped default, a re-ordered
 * param) spins forever.
 *
 * So there is no second copy. `searchParams` IS the state: every field below
 * is a pure `useMemo` over it, and the only writes happen inside event
 * handlers. A render cannot schedule a navigation, so a render loop is not
 * possible by construction. The single exception — the RBAC demotion in
 * page.tsx — writes with `replace` under a condition that its own write makes
 * false, and is documented there.
 *
 * ## What belongs in the URL
 *
 * Anything that answers "what am I looking at": tab, time window, scope
 * (crew/agent/trace), severity, muted groups, the search box, and the Runs
 * tab's own window/status/trigger/page. Those travel — a link has to land the
 * recipient on the same view.
 *
 * Anything that answers "how do I like to read" stays a per-user preference
 * (`/api/v1/me/preferences`, via useUserPreference): wrap, sort direction,
 * dedup, refresh cadence, the stats-rail collapse, the metrics-visibility
 * chip. A shared link must not re-style the recipient's journal, and a URL
 * value would fight the stored preference on every load. Histogram bucket
 * selection stays component-local for a third reason: it is a transient
 * drill-down over the currently loaded buffer, keyed to a time range the
 * recipient may not even be looking at.
 */

export type JournalTab = "timeline" | "runs" | "spend"

/** Every key this page owns. A saved view may set these and nothing else. */
export const JOURNAL_URL_KEYS = [
  "tab",
  "time",
  "from",
  "to",
  "crew_id",
  "agent_id",
  "trace_id",
  "severity",
  "mute",
  "q",
  "run_window",
  "run_status",
  "run_trigger",
  "run_page",
] as const

export type JournalUrlKey = (typeof JOURNAL_URL_KEYS)[number]

const TIME_RANGES: readonly string[] = ["5m", "15m", "1h", "24h", "7d", "30d", "all", "custom"]
const SEVERITIES: readonly string[] = ["info", "notice", "warn", "error"]
const TABS: readonly string[] = ["timeline", "runs", "spend"]
export const RUN_STATUSES: readonly string[] = [
  "all",
  "RUNNING",
  "COMPLETED",
  "FAILED",
  "CANCELLED",
  "TIMEOUT",
]
export const RUN_TRIGGERS: readonly string[] = [
  "all",
  "USER",
  "WEBHOOK",
  "CRON",
  "AGENT",
  "SYSTEM",
]

export interface JournalUrlState {
  /** The tab named by the URL, before any RBAC clamp. */
  tab: JournalTab
  timeRange: TimeRange
  customRange: CustomRange | null
  crewId: string
  agentId: string
  traceId: string
  severity: SeverityFilter
  muted: Set<EntryGroup>
  /** The committed search query — what the backend is actually filtering on. */
  q: string
  runWindow: RunWindow
  runStatus: string
  runTrigger: string
  runPage: number
}

/** Minimal read surface shared by URLSearchParams and Next's readonly variant. */
export interface ParamReader {
  get(key: string): string | null
}

/**
 * Pure decoder. Unknown / malformed values fall back to the default rather
 * than propagating — a stale bookmark must never produce a 403 or bind a
 * filter the backend will reject.
 */
export function parseJournalUrl(sp: ParamReader): JournalUrlState {
  const rawTab = sp.get("tab")
  const tab = (TABS.includes(rawTab ?? "") ? rawTab : "timeline") as JournalTab

  const rawTime = sp.get("time")
  const timeRange = (TIME_RANGES.includes(rawTime ?? "") ? rawTime : "24h") as TimeRange

  let customRange: CustomRange | null = null
  const from = Number(sp.get("from"))
  const to = Number(sp.get("to"))
  if (sp.get("from") && sp.get("to") && Number.isFinite(from) && Number.isFinite(to) && to > from) {
    customRange = { fromMs: from, toMs: to }
  }

  const rawSeverity = sp.get("severity")
  const severity = (SEVERITIES.includes(rawSeverity ?? "") ? rawSeverity : "all") as SeverityFilter

  const muted = new Set<EntryGroup>()
  const rawMute = sp.get("mute")
  if (rawMute) {
    for (const g of rawMute.split(",")) {
      const trimmed = g.trim() as EntryGroup
      if ((GROUP_ORDER as readonly string[]).includes(trimmed)) muted.add(trimmed)
    }
  }

  const rawWindow = sp.get("run_window")
  const runWindow = (RUN_WINDOWS as readonly string[]).includes(rawWindow ?? "")
    ? (rawWindow as RunWindow)
    : "24h"

  const rawStatus = sp.get("run_status")
  const runStatus = RUN_STATUSES.includes(rawStatus ?? "") ? (rawStatus as string) : "all"

  const rawTrigger = sp.get("run_trigger")
  const runTrigger = RUN_TRIGGERS.includes(rawTrigger ?? "") ? (rawTrigger as string) : "all"

  const rawPage = Number(sp.get("run_page"))
  const runPage = Number.isInteger(rawPage) && rawPage > 0 ? rawPage : 1

  return {
    tab,
    timeRange,
    customRange,
    crewId: sp.get("crew_id") ?? "",
    agentId: sp.get("agent_id") ?? "",
    traceId: sp.get("trace_id") ?? "",
    severity,
    muted,
    q: sp.get("q") ?? "",
    runWindow,
    runStatus,
    runTrigger,
    runPage,
  }
}

/**
 * The journal-owned slice of a query string, as a plain record — the payload a
 * saved view stores in `filters_json`. Params the page does not own (anything
 * a future surface adds) are dropped rather than persisted, so applying an old
 * view can never resurrect a key that has since changed meaning.
 */
export function journalFiltersFromSearch(search: string): Record<string, string> {
  const sp = new URLSearchParams(search)
  const out: Record<string, string> = {}
  for (const key of JOURNAL_URL_KEYS) {
    const value = sp.get(key)
    if (value) out[key] = value
  }
  return out
}

/**
 * Decode a saved view's `filters_json` back into journal params.
 *
 * `filters_json` is a free-form payload shared with the issues saved-view
 * implementation, and it is written by anyone with the CLI
 * (`crewship saved-view create --filters '{…}'`), so it is treated as
 * untrusted: only known keys survive, values are coerced to strings, and a
 * malformed document yields null rather than throwing at the click handler.
 * Both the flat shape and the `{ "params": {…} }` envelope this UI writes are
 * accepted, so a hand-rolled `--filters '{"tab":"runs"}'` works as typed.
 */
export function journalFiltersFromJson(raw: string | null | undefined): Record<string, string> | null {
  if (!raw) return null
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return null
  }
  if (!parsed || typeof parsed !== "object") return null
  const doc = parsed as Record<string, unknown>
  const inner = doc.params
  const source = (inner && typeof inner === "object" ? inner : doc) as Record<string, unknown>
  const out: Record<string, string> = {}
  for (const key of JOURNAL_URL_KEYS) {
    const value = source[key]
    if (typeof value === "string" && value !== "") out[key] = value
    else if (typeof value === "number" && Number.isFinite(value)) out[key] = String(value)
  }
  return out
}

/** A patch: `null` (or "") deletes the key, anything else sets it. */
export type JournalUrlPatch = Partial<Record<JournalUrlKey, string | null>>

export interface UseJournalUrlStateResult {
  state: JournalUrlState
  /** The raw query string — the input to `journalFiltersFromSearch`. */
  search: string
  /**
   * Apply a patch to the current query string. Defaults to `push` so Back
   * steps back through the filter history instead of leaving the page;
   * `replace` is for writes the user did not perform (URL clean-ups) and for
   * continuous input (the debounced search box), where one history entry per
   * keystroke pause would bury the previous view.
   */
  setParams: (patch: JournalUrlPatch, opts?: { replace?: boolean }) => void
  /**
   * Replace the entire journal-owned key set — used when a saved view is
   * applied, so a view that does not name `severity` actually clears it
   * instead of inheriting whatever was on screen. Params this page does not
   * own are preserved untouched.
   */
  applyParams: (next: Partial<Record<JournalUrlKey, string>>) => void
}

export function useJournalUrlState(): UseJournalUrlStateResult {
  const searchParams = useSearchParams()
  const router = useRouter()
  const pathname = usePathname()

  const search = searchParams.toString()

  const state = useMemo(() => parseJournalUrl(new URLSearchParams(search)), [search])

  // `router.push` does not update `searchParams` synchronously, so two writes
  // inside one event handler would both build on the query string this render
  // was given and the second would silently drop the first. Remember the last
  // thing we wrote, keyed by the base it was written from: once the router
  // catches up the key stops matching and the note is ignored. (Prefer one
  // call with a combined patch anyway — two writes are two history entries.)
  const pendingRef = useRef<{ base: string; result: string } | null>(null)

  // `build` is handed the effective current query string — the last one we
  // asked for if the router has not caught up yet, otherwise the real one.
  // Resolving it inside the call, not at render, is the whole point: two
  // writes in one handler happen between renders.
  const navigate = useCallback(
    (build: (base: string) => URLSearchParams, replace: boolean) => {
      const pending = pendingRef.current
      const base = pending && pending.base === search ? pending.result : search
      const qs = build(base).toString()
      // No-op guard. Without it a re-render that re-fires an idempotent
      // handler (a select re-emitting its current value, the debounced search
      // committing an unchanged string) would stack duplicate history entries
      // that Back then has to walk through one by one.
      if (qs === base) return
      pendingRef.current = { base: search, result: qs }
      const url = qs ? `${pathname}?${qs}` : pathname
      if (replace) router.replace(url, { scroll: false })
      else router.push(url, { scroll: false })
    },
    [router, pathname, search],
  )

  const setParams = useCallback(
    (patch: JournalUrlPatch, opts?: { replace?: boolean }) => {
      navigate((base) => {
        const sp = new URLSearchParams(base)
        for (const [key, value] of Object.entries(patch)) {
          if (value === null || value === undefined || value === "") sp.delete(key)
          else sp.set(key, value)
        }
        return sp
      }, opts?.replace ?? false)
    },
    [navigate],
  )

  const applyParams = useCallback(
    (next: Partial<Record<JournalUrlKey, string>>) => {
      navigate((base) => {
        const sp = new URLSearchParams(base)
        for (const key of JOURNAL_URL_KEYS) sp.delete(key)
        for (const key of JOURNAL_URL_KEYS) {
          const value = next[key]
          if (value) sp.set(key, value)
        }
        return sp
      }, false)
    },
    [navigate],
  )

  return { state, search, setParams, applyParams }
}
