"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import {
  Activity,
  BookOpen,
  DollarSign,
  ListOrdered,
  Radio,
  RadioTower,
  Zap,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { SubBar } from "@/components/layout/sub-bar"
import { cn } from "@/lib/utils"
import { useAbilities } from "@/hooks/use-abilities"
import { useWorkspace } from "@/hooks/use-workspace"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalStream } from "@/hooks/use-journal-stream"
import { useUserPreference } from "@/hooks/use-user-preference"
import { RunsView } from "@/components/features/journal/runs-view"
import { JournalSpendView } from "@/components/features/journal/journal-spend-view"
import { JournalSavedViews } from "@/components/features/journal/journal-saved-views"
import { LogsPanel } from "@/components/features/logs/logs-panel"
import { ResourcesStrip } from "@/components/features/logs/resources-strip"
import { sinceFromTimeRange, type CustomRange, type TimeRange } from "@/components/features/logs/time-range-picker"
import { refreshRateMs, type RefreshRate } from "@/components/features/logs/refresh-rate-picker"
import type { ScopeOption, SeverityFilter } from "@/components/features/logs/logs-toolbar"
import {
  journalFiltersFromSearch,
  useJournalUrlState,
  type JournalTab,
  type JournalUrlKey,
} from "@/hooks/use-journal-url-state"
import { type EntryGroup } from "@/lib/journal-style"
import type { RunWindow } from "@/lib/runs-insights"
import { entryTypesForGroups } from "@/lib/journal-groups"
import { parseStructuredQuery } from "@/lib/log-search"
import { apiFetch } from "@/lib/api-fetch"

/**
 * Cap on entries kept in memory. Generous enough to hold a full
 * Grafana-style "show me everything in the time range" window for
 * busy workspaces; small enough to keep the filter chain + virtuoso
 * + histogram + stats rail all responsive on a laptop.
 */
const JOURNAL_MAX_ENTRIES = 5000

interface CrewSummary {
  id: string
  name: string
  icon?: string | null
  color?: string | null
  avatar_style?: string | null
}
interface AgentSummary {
  id: string
  name: string
  crew_id?: string | null
  avatar_seed?: string | null
  avatar_style?: string | null
  crew?: { avatar_style?: string | null } | null
}

interface TabDef {
  id: JournalTab
  label: string
  icon: typeof ListOrdered
  /** When true, only OWNER/ADMIN see the tab. */
  adminOnly?: boolean
  /**
   * Locked tabs render with a "Soon" badge and a lock icon, and the
   * click handler is a no-op so activeTab never lands on them.
   */
  locked?: boolean
}

const ALL_TABS: TabDef[] = [
  { id: "timeline", label: "Timeline", icon: ListOrdered },
  { id: "runs", label: "Runs", icon: Activity },
  { id: "spend", label: "Spend", icon: DollarSign, adminOnly: true },
]

/**
 * Crew Journal — workspace-wide records center.
 *
 *   - Timeline: runtime events from `journal_entries` (Grafana-style)
 *   - Runs:     agent run aggregates derived from journal `run.*` entries
 *   - Spend:    cost ledger surface — currently locked behind a "Soon"
 *               badge until LLM-cost attribution is prioritized.
 *
 * Audit log moved to `/settings?tab=audit` (admin compliance view).
 * Eval / quartermaster replay surface was removed; the backend emit
 * machinery in `internal/quartermaster/` is preserved as a nice-to-have
 * to revisit when there are >=2 production missions worth comparing.
 *
 * Per-tab RBAC hides admin-only tabs entirely from non-admins so they
 * never see a "click here for 403" affordance.
 */
export default function JournalPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { role, loading: rolesLoading } = useAbilities()
  const isAdmin = role === "OWNER" || role === "ADMIN"

  // ── The URL IS the filter state ─────────────────────────────────────────
  //
  // Not a copy of it. Every value below is a pure read of `searchParams`, so
  // a same-pathname `router.push` — a run row opening its trace, an in-app
  // link into /journal?… while the page is already mounted — moves the whole
  // page instead of only the address bar. It also means there is no mirror
  // effect writing the URL back, and therefore no way for a render to
  // schedule a navigation: the state→URL→state loop that made the original
  // author freeze these values at mount cannot form. Writes happen in event
  // handlers only. See hooks/use-journal-url-state.ts.
  const { state: url, search: urlSearch, setParams, applyParams } = useJournalUrlState()
  const { timeRange, customRange, crewId, agentId, traceId, severity, muted } = url

  // Visible tabs depends on role. Admin-only tabs are filtered out for
  // non-admins. The deeplink defaults to timeline if the user lacks
  // access to the requested tab.
  const visibleTabs = useMemo<TabDef[]>(
    () => ALL_TABS.filter((t) => !t.adminOnly || isAdmin),
    [isAdmin],
  )

  const setTimeRange = useCallback(
    (next: TimeRange) => {
      // Leaving "custom" drops the bounds with it — a stale from/to left in
      // the URL would come back the next time the picker reached "custom".
      setParams(
        next === "custom"
          ? { time: "custom" }
          : { time: next === "24h" ? null : next, from: null, to: null },
      )
    },
    [setParams],
  )

  const setCustomRange = useCallback(
    (next: CustomRange | null) => {
      if (!next) setParams({ from: null, to: null })
      else setParams({ time: "custom", from: String(next.fromMs), to: String(next.toMs) })
    },
    [setParams],
  )

  const setAgentId = useCallback(
    (id: string) => setParams({ agent_id: id || null }),
    [setParams],
  )
  const setTraceId = useCallback(
    (id: string) => setParams({ trace_id: id || null }),
    [setParams],
  )
  // Severity + muted-groups are LIFTED out of LogsPanel so we can mirror
  // them as server-side filters. The previous client-only filtering
  // silently dropped matches when the 5,000-entry buffer cap kicked in
  // — muting "container" might leave zero events visible because the
  // server already returned the most recent 5k container.metrics rows.
  const setSeverity = useCallback(
    (next: SeverityFilter) => setParams({ severity: next === "all" ? null : next }),
    [setParams],
  )
  const setMuted = useCallback(
    (next: Set<EntryGroup>) =>
      setParams({ mute: next.size > 0 ? Array.from(next).join(",") : null }),
    [setParams],
  )
  // Live-tail pause is a session control, not a view: it says "stop moving
  // while I read this", which is meaningless to whoever opens the link.
  const [live, setLive] = useState(true)
  // Auto-refresh cadence — defaults to "live" (SSE-driven, no polling)
  // so we don't load the backend with redundant requests when a
  // working stream is already pushing events. Pollers (5s/10s/…) are
  // additive on top of SSE for users who want a hard freshness floor.
  const [refreshRate, setRefreshRate] = useUserPreference<RefreshRate>(
    "journal.timeline.refreshRate",
    "live",
  )

  // Per-user list of entry types to hide from the Timeline. Defaults to
  // ["container.metrics"] — those are high-volume performance samples
  // already visualised in the resources strip; rendering them in the
  // event log buries everything else under stats noise. Users can flip
  // them back on via the toolbar chip; the choice is persisted server-
  // side via /api/v1/me/preferences so it survives reload + new device.
  const [excludedTypes, setExcludedTypes] = useUserPreference<string[]>(
    "journal.timeline.excludedTypes",
    ["container.metrics"],
  )

  // Tab from `?tab=`. Unknown / locked / unauthorized values render as
  // timeline so a stale bookmark can never surface a 403 — the clamp is a
  // derivation, so it holds on every render, not just the first.
  // `isAdmin` is false while the role is still loading, which is the safe
  // direction: an admin-only surface never paints before we know.
  const activeTab = useMemo<JournalTab>(() => {
    const tabDef = ALL_TABS.find((t) => t.id === url.tab)
    if (!tabDef || tabDef.locked || (tabDef.adminOnly && !isAdmin)) return "timeline"
    return url.tab
  }, [url.tab, isAdmin])

  const setActiveTab = useCallback(
    (next: JournalTab) => setParams({ tab: next === "timeline" ? null : next }),
    [setParams],
  )

  // Keep the URL honest once the role is known: a link that says `tab=spend`
  // while showing the timeline is a link nobody can share. `replace`, because
  // the user did not navigate here — and the write makes its own condition
  // false (url.tab becomes "timeline", which is what activeTab already is),
  // so it runs at most once per demotion rather than looping.
  useEffect(() => {
    if (rolesLoading) return
    if (url.tab === activeTab) return
    setParams({ tab: null }, { replace: true })
  }, [rolesLoading, url.tab, activeTab, setParams])

  // Crew + agent options for the toolbar selects.
  const [crews, setCrews] = useState<ScopeOption[]>([])
  const [agents, setAgents] = useState<ScopeOption[]>([])

  useEffect(() => {
    if (!workspaceId) {
      setCrews([])
      return
    }
    let cancelled = false
    ;(async () => {
      try {
        const res = await apiFetch(`/api/v1/crews?workspace_id=${workspaceId}`)
        if (!res.ok) return
        const json = (await res.json()) as CrewSummary[]
        if (!cancelled && Array.isArray(json)) {
          setCrews(
            json.map((c) => ({
              id: c.id,
              name: c.name,
              icon: c.icon ?? null,
              color: c.color ?? null,
            })),
          )
        }
      } catch {
        /* leave empty on failure */
      }
    })()
    return () => { cancelled = true }
  }, [workspaceId])

  useEffect(() => {
    if (!workspaceId) {
      setAgents([])
      return
    }
    let cancelled = false
    const url = crewId
      ? `/api/v1/agents?workspace_id=${workspaceId}&crew_id=${crewId}`
      : `/api/v1/agents?workspace_id=${workspaceId}`
    ;(async () => {
      try {
        const res = await apiFetch(url)
        if (!res.ok) return
        const json = (await res.json()) as AgentSummary[]
        if (!cancelled && Array.isArray(json)) {
          setAgents(
            json.map((a) => ({
              id: a.id,
              name: a.name,
              avatarSeed: a.avatar_seed ?? null,
              avatarStyle: a.avatar_style ?? a.crew?.avatar_style ?? null,
            })),
          )
        }
      } catch {
        /* leave empty on failure */
      }
    })()
    return () => { cancelled = true }
  }, [workspaceId, crewId])

  // Crew change clears any agent selection that's no longer in scope — one
  // navigation, so Back does not step through a crew/agent pair that was
  // never on screen.
  const onCrewChange = useCallback(
    (id: string) => setParams({ crew_id: id || null, agent_id: null }),
    [setParams],
  )

  // id → name lookup so the LogsPanel stats rail can render "viktor"
  // instead of a UUID.
  const agentLookup = useMemo<Record<string, string>>(() => {
    const out: Record<string, string> = {}
    for (const a of agents) out[a.id] = a.name
    return out
  }, [agents])

  // ── Search box ──────────────────────────────────────────────────────────
  //
  // The one filter that cannot be read straight off the URL: the input needs
  // the character the user just typed, and the URL should only carry the
  // query they stopped typing. So the draft is local, the committed value
  // lives in `?q=` (LogsPanel debounces the commit for us), and `committedRef`
  // records who wrote the URL last.
  //
  // That ref is what keeps the two-way sync from oscillating. A commit sets
  // it before writing, so the adopt-from-URL effect sees its own value and
  // stands down; an external change (Back, a saved view, a deeplink) does not
  // match, so the draft adopts it exactly once. Each direction terminates the
  // other.
  const [searchDraft, setSearchDraft] = useState(url.q)
  const committedRef = useRef(url.q)
  useEffect(() => {
    if (url.q === committedRef.current) return
    committedRef.current = url.q
    setSearchDraft(url.q)
  }, [url.q])
  const commitSearch = useCallback(
    (next: string) => {
      committedRef.current = next
      // `replace`, not `push`: typing is continuous input, and one history
      // entry per 300 ms pause would bury the view the user came from.
      setParams({ q: next || null }, { replace: true })
    },
    [setParams],
  )
  const serverQuery = url.q

  // Structured-query split: tokens like `agent:viktor severity:error
  // type:exec.command` get peeled off the search box and routed to
  // server-side query params instead of being narrowed client-side over
  // the 5,000-row buffer. Free text + payload keys (`payload.foo:bar`)
  // stay in clientSearchQuery and feed LogsPanel's local matcher.
  const structured = useMemo(() => parseStructuredQuery(serverQuery), [serverQuery])

  const queryParams = useMemo<Record<string, string | undefined>>(() => {
    const since = sinceFromTimeRange(timeRange, customRange)
    const until = timeRange === "custom" && customRange
      ? new Date(customRange.toMs).toISOString()
      : undefined
    // Server-side severity: skip when "all" so we don't bind a filter.
    const severityParam = severity === "all" ? undefined : severity
    // Server-side mute → exclude_entry_type. "other" can't be expanded
    // server-side (its membership is the complement of every known
    // type) so it remains client-only — entryTypesForGroups handles
    // that gracefully by returning [] for "other". User-pref
    // excludedTypes layers on top so the high-noise metric stream can
    // be hidden without muting the whole "container" group (which
    // would also drop snapshots + status changes).
    const groupExcludes = entryTypesForGroups(muted)
    const excludeTypes = Array.from(new Set([...groupExcludes, ...excludedTypes]))
    // structured.serverParams takes precedence over scope-level
    // filters when both are set so the user can use the search box to
    // pin one specific agent/crew/trace without first clearing the
    // toolbar selects. trace_id from URL still wins over an
    // unstructured token if both somehow coexist.
    return {
      crew_id: structured.serverParams.crew_id ?? (crewId || undefined),
      agent_id: structured.serverParams.agent_id ?? (agentId || undefined),
      trace_id: traceId || structured.serverParams.trace_id || undefined,
      entry_type: structured.serverParams.entry_type,
      severity: structured.serverParams.severity ?? severityParam,
      actor_type: structured.serverParams.actor_type,
      priority: structured.serverParams.priority,
      exclude_entry_type: excludeTypes.length > 0 ? excludeTypes.join(",") : undefined,
      q: structured.clientQuery.trim() || undefined,
      since,
      until,
    }
  }, [timeRange, customRange, crewId, agentId, traceId, severity, muted, excludedTypes, structured])

  // Only the Timeline tab consumes the journal list + SSE stream.
  const timelineEnabled = !wsLoading && activeTab === "timeline"
  const { entries, nextCursor, loading, loadingMore, error, refresh, loadMore, prependLive } =
    useJournalList({
      workspaceId,
      params: queryParams,
      enabled: timelineEnabled,
      maxEntries: JOURNAL_MAX_ENTRIES,
    })

  const liveRef = useRef(live)
  useEffect(() => { liveRef.current = live }, [live])

  // SSE prepend batching — chatty workspaces can fire 50+ events/sec.
  // Without batching, each event triggers a state update + full filter
  // chain re-render (toolbar counts, histogram 60 buckets, virtuoso
  // viewport, stats rail). Buffer up to 250 ms and flush as a group.
  const pendingRef = useRef<Parameters<typeof prependLive>[0][]>([])
  const flushTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const flushPending = useCallback(() => {
    flushTimerRef.current = null
    const batch = pendingRef.current
    if (batch.length === 0) return
    pendingRef.current = []
    for (const e of batch) prependLive(e)
  }, [prependLive])
  useEffect(() => {
    return () => {
      if (flushTimerRef.current) clearTimeout(flushTimerRef.current)
    }
  }, [])

  const handleLive = useCallback(
    (entry: Parameters<typeof prependLive>[0]) => {
      if (!liveRef.current) return
      pendingRef.current.push(entry)
      if (!flushTimerRef.current) {
        flushTimerRef.current = setTimeout(flushPending, 250)
      }
    },
    [flushPending],
  )
  const { status: streamStatus } = useJournalStream({
    workspaceId,
    params: queryParams,
    enabled: timelineEnabled,
    onEntry: handleLive,
  })

  const handleRefresh = useCallback(() => { void refresh() }, [refresh])

  // Periodic auto-refresh — only when the user has explicitly opted
  // in via the picker. "live" / "off" → no timer.
  useEffect(() => {
    if (!timelineEnabled) return
    const ms = refreshRateMs(refreshRate)
    if (ms === null) return
    const id = setInterval(() => {
      if (!liveRef.current) return // paused live tail → don't auto-refresh either
      void refresh()
    }, ms)
    return () => clearInterval(id)
  }, [timelineEnabled, refreshRate, refresh])

  // Eager pagination — once the initial fetch lands, keep walking the
  // cursor until the backend reports no more pages OR we hit the
  // in-memory cap. This is the Elastic Discover / Grafana Logs
  // behaviour: the time-range select determines what the user sees,
  // not a scroll position. Scroll-triggered loadMore is intentionally
  // not wired so the list stays "what's in the window" and nothing
  // sneaks in mid-scroll.
  useEffect(() => {
    if (!timelineEnabled) return
    if (loading || loadingMore) return
    if (!nextCursor) return
    if (entries.length >= JOURNAL_MAX_ENTRIES) return
    void loadMore()
  }, [timelineEnabled, loading, loadingMore, nextCursor, entries.length, loadMore])

  // Stats-rail Network card — admin-only and only meaningful when a single
  // crew is in scope (metrics are per-container).
  const showNetworkCard = isAdmin && Boolean(crewId)

  // ── Runs tab ────────────────────────────────────────────────────────────
  // Window / status / trigger / page were component state, so "the CRON runs
  // that failed this week" could only be handed over as instructions. Each
  // filter change drops the page number in the SAME navigation — a page 3
  // carried across a filter change lands on an empty table, and resetting it
  // in a second write would make Back step through a view nobody saw.
  const setRunWindow = useCallback(
    // Clear the page for the same reason status and trigger do: a narrower
    // window can leave the reader on a page that no longer exists, and an
    // empty page 3 reads as "no runs" rather than as "you are past the end".
    (next: RunWindow) =>
      setParams({ run_window: next === "24h" ? null : next, run_page: null }),
    [setParams],
  )
  const setRunStatus = useCallback(
    (next: string) => setParams({ run_status: next === "all" ? null : next, run_page: null }),
    [setParams],
  )
  const setRunTrigger = useCallback(
    (next: string) => setParams({ run_trigger: next === "all" ? null : next, run_page: null }),
    [setParams],
  )
  const setRunPage = useCallback(
    (next: number) => setParams({ run_page: next <= 1 ? null : String(next) }),
    [setParams],
  )

  // ── Saved views ─────────────────────────────────────────────────────────
  // A saved view is exactly this page's URL params, so it stores them
  // verbatim and applying one replaces the whole set — a view that does not
  // name `severity` has to CLEAR it, not inherit whatever is on screen.
  const savedViewFilters = useMemo(
    () => journalFiltersFromSearch(urlSearch),
    [urlSearch],
  )
  const applySavedView = useCallback(
    (params: Record<string, string>) => applyParams(params as Partial<Record<JournalUrlKey, string>>),
    [applyParams],
  )

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {/* ---- Sub-bar: identity + status + tabs ---- */}
      <SubBar<JournalTab>
        icon={BookOpen}
        title="Crew Journal"
        ariaLabel="Journal"
        description={activeTab === "timeline" ? `${entries.length} loaded` : undefined}
        meta={
          activeTab === "timeline" && (
            <>
              <StreamStatusBadge status={streamStatus} />
              <AnomalyBadge
                entries={entries}
                // One navigation, not two — a pair of writes would leave a
                // half-applied view sitting in the history for Back to land on.
                onClick={() => setParams({ severity: "error", time: "5m", from: null, to: null })}
              />
              <MetricsVisibilityChip
                excludedTypes={excludedTypes}
                onChange={setExcludedTypes}
              />
            </>
          )
        }
        tabs={visibleTabs.map((t) => ({
          id: t.id,
          label: t.label,
          icon: t.icon,
          locked: t.locked,
          badge: t.adminOnly && !t.locked ? "admin" : undefined,
        }))}
        activeTab={activeTab}
        // SubBar already guards locked tabs (never fires onTabChange for
        // them); the demote-to-timeline useEffect still owns stale-deeplink
        // and role-loss fallbacks.
        onTabChange={setActiveTab}
      />

      {/* ---- Saved views ---- */}
      {/* Above the tab content, not inside it: a saved view can name the tab,
          so the control that applies one cannot live in a single tab. */}
      <JournalSavedViews
        workspaceId={workspaceId}
        filters={savedViewFilters}
        onApply={applySavedView}
      />

      {/* ---- Tab content (animated swap) ---- */}
      <AnimatePresence mode="wait">
        {activeTab === "timeline" && (
          <motion.div
            key="timeline"
            initial={{ opacity: 0, y: 4 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -4 }}
            transition={{ duration: 0.18, ease: "easeOut" }}
            className="flex-1 min-h-0 flex flex-col"
          >
            <motion.div
              key="resources-strip"
              initial={{ opacity: 0, height: 0 }}
              animate={{ opacity: 1, height: "auto" }}
              transition={{ duration: 0.22, ease: "easeOut" }}
              className="overflow-hidden"
            >
              <ResourcesStrip
                workspaceId={workspaceId}
                crewId={crewId}
                mode={crewId ? "single" : "aggregate"}
              />
            </motion.div>
            <div className="flex-1 min-h-0">
              <LogsPanel
                entries={entries}
                timeRange={timeRange}
                onTimeRangeChange={setTimeRange}
                customRange={customRange}
                onCustomRangeChange={setCustomRange}
                crewScope={{ value: crewId, options: crews, onChange: onCrewChange }}
                agentScope={{ value: agentId, options: agents, onChange: setAgentId }}
                agentLookup={agentLookup}
                showNetworkCard={showNetworkCard}
                severity={severity}
                onSeverityChange={setSeverity}
                muted={muted}
                onMutedChange={setMuted}
                traceId={traceId}
                onClearTraceId={() => setTraceId("")}
                onSelectTrace={setTraceId}
                onSelectAgent={setAgentId}
                onSelectCrew={onCrewChange}
                query={searchDraft}
                onQueryChange={setSearchDraft}
                onServerSearch={commitSearch}
                onRefresh={handleRefresh}
                loading={loading}
                error={error}
                refreshRate={refreshRate}
                onRefreshRateChange={setRefreshRate}
                live={live}
                onLiveChange={setLive}
                hasMore={Boolean(nextCursor)}
                loadingMore={loadingMore}
                cappedAt={entries.length >= JOURNAL_MAX_ENTRIES ? JOURNAL_MAX_ENTRIES : undefined}
              />
            </div>
          </motion.div>
        )}

        {activeTab === "runs" && (
          <motion.div
            key="runs"
            initial={{ opacity: 0, y: 4 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -4 }}
            transition={{ duration: 0.18, ease: "easeOut" }}
            className="flex-1 min-h-0 overflow-hidden flex flex-col"
          >
            <RunsView
              workspaceId={workspaceId}
              workspaceLoading={wsLoading}
              window={url.runWindow}
              onWindowChange={setRunWindow}
              statusFilter={url.runStatus}
              onStatusFilterChange={setRunStatus}
              triggerFilter={url.runTrigger}
              onTriggerFilterChange={setRunTrigger}
              page={url.runPage}
              onPageChange={setRunPage}
            />
          </motion.div>
        )}

        {activeTab === "spend" && (
          <motion.div
            key="spend"
            initial={{ opacity: 0, y: 4 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -4 }}
            transition={{ duration: 0.18, ease: "easeOut" }}
            className="flex-1 min-h-0 overflow-hidden flex flex-col"
          >
            <JournalSpendView workspaceId={workspaceId} workspaceLoading={wsLoading} />
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}

/**
 * Live error/warn count over the last ANOMALY_WINDOW_MS milliseconds,
 * surfaced as a pulsing red pill in the header. Acts as a "you should
 * look at this" signal so a viewer scrolling through routine
 * exec.command + container.metrics traffic doesn't miss a fresh
 * cluster of failures. Clicking jumps the filter to severity=error +
 * time=5m so the pill always resolves to a useful narrowed view.
 *
 * Threshold (ANOMALY_THRESHOLD) deliberately starts low (>= 3) — false
 * positives here are cheap (a quick glance) and false negatives are
 * not (a missed cluster of run.failed events).
 */
const ANOMALY_WINDOW_MS = 5 * 60 * 1000
const ANOMALY_THRESHOLD = 3

function AnomalyBadge({
  entries,
  onClick,
}: {
  entries: Array<{ ts: string; severity?: string }>
  onClick: () => void
}) {
  // Wall-clock tick keeps the rolling window honest when no new entries
  // arrive: without it the cutoff would freeze at the timestamp of the
  // most recent render, so a quiet stream of errors three minutes ago
  // would keep firing the badge forever instead of aging out.
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 30_000)
    return () => clearInterval(id)
  }, [])
  const errCount = useMemo(() => {
    const cutoff = now - ANOMALY_WINDOW_MS
    let n = 0
    for (const e of entries) {
      if (e.severity !== "error" && e.severity !== "warn") continue
      const t = Date.parse(e.ts)
      if (Number.isFinite(t) && t >= cutoff) n++
    }
    return n
  }, [entries, now])
  if (errCount < ANOMALY_THRESHOLD) return null
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={`Focus ${errCount} error or warning events from the last 5 minutes`}
      className="inline-flex items-center gap-1.5 h-5 px-2 rounded-full border border-destructive/40 bg-destructive/10 text-[10px] font-mono text-destructive hover:bg-destructive/20 transition-colors"
      title={`${errCount} error/warn events in the last 5 minutes — click to focus`}
    >
      <span className="relative inline-flex">
        <span className="absolute inline-flex h-1.5 w-1.5 rounded-full bg-destructive opacity-75 animate-ping" />
        <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-destructive" />
      </span>
      <span className="tabular-nums">{errCount}</span>
      <span className="opacity-80">in 5m</span>
    </button>
  )
}

/**
 * Toggle for hiding/showing `container.metrics` in the timeline event
 * stream. Metrics are visualised in the resources strip above; rendering
 * them in the event log too produces a wall of stats noise that buries
 * other event types. The chip persists user choice via
 * useUserPreference (see "journal.timeline.excludedTypes"). Other entry
 * types in excludedTypes are not exposed here yet — this is the
 * highest-volume type by far and the only one users have flagged.
 */
function MetricsVisibilityChip({
  excludedTypes,
  onChange,
}: {
  excludedTypes: string[]
  onChange: (next: string[]) => void
}) {
  const hidden = excludedTypes.includes("container.metrics")
  const toggle = () => {
    if (hidden) {
      onChange(excludedTypes.filter((t) => t !== "container.metrics"))
    } else {
      onChange([...excludedTypes, "container.metrics"])
    }
  }
  return (
    <button
      type="button"
      onClick={toggle}
      aria-pressed={!hidden}
      title={
        hidden
          ? "Metrics are hidden from the timeline (still visible in the resources strip). Click to show them inline."
          : "Metrics are shown inline in the timeline. Click to hide them — they remain in the resources strip."
      }
      className={cn(
        "inline-flex items-center gap-1.5 h-5 px-2 rounded-full border text-[10px] font-mono transition-colors",
        hidden
          ? "border-border/60 bg-card/50 text-muted-foreground hover:bg-card hover:text-foreground/80"
          : "border-notice/40 bg-notice/10 text-notice hover:bg-notice/20",
      )}
    >
      <Activity className="h-3 w-3" />
      <span>{hidden ? "metrics: hidden" : "metrics: shown"}</span>
    </button>
  )
}

function StreamStatusBadge({ status }: { status: string }) {
  if (status === "connected") {
    return (
      <Badge variant="outline" className="gap-1 text-[10px] bg-success/10 text-success border-success/30">
        <Zap className="h-3 w-3" /> Live
      </Badge>
    )
  }
  if (status === "polling") {
    return (
      <Badge variant="outline" className="gap-1 text-[10px] bg-warn/10 text-warn border-warn/30">
        <RadioTower className="h-3 w-3" /> Polling
      </Badge>
    )
  }
  if (status === "connecting") {
    return (
      <Badge variant="outline" className="gap-1 text-[10px] bg-info/10 text-info border-info/30">
        <Radio className="h-3 w-3" /> Connecting
      </Badge>
    )
  }
  if (status === "error") {
    return (
      <Badge variant="outline" className="gap-1 text-[10px] bg-destructive/10 text-destructive border-destructive/30">
        Offline
      </Badge>
    )
  }
  return null
}
