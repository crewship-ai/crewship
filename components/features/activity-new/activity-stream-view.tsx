"use client"

// /activity-new — the shell: SubBar, rail, and whichever main surface the
// current scope calls for.
//
// Two modes, one page:
//
//   scope = all   → the overview dashboard. What is running, what is stuck
//                   on a person, what broke, what it cost, is today normal.
//   scope = one   → that bucket as a list, still in cards, so "3 failed"
//                   is a place you can go rather than a number you read.
//
// Selecting any row replaces both with the activity's own page: the run's
// node graph, the chain under its trace id, and the raw record. Not a side
// panel — see activity-detail.tsx for why.

import * as React from "react"
import { Activity } from "lucide-react"

import { SubBar } from "@/components/layout/sub-bar"
import { SidebarActiveChip, SidebarActiveChips, SidebarCollapseButton } from "@/components/layout/sidebar-kit"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { Appear, EmptyState } from "@/components/ui/detail"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalLookup } from "@/hooks/use-journal-lookup"
import { useJournalStream } from "@/hooks/use-journal-stream"
import { useIsMobile } from "@/hooks/use-mobile"
import { usePipelines } from "@/hooks/use-pipelines"
import { apiFetch } from "@/lib/api-fetch"
import {
  ACTIVE_ENTRY_TYPES,
  ACTIVITY_SCOPES,
  NOISE_ENTRY_TYPES,
  narrowToFocus,
  scopeOf,
  sourceEntryTypes,
  sourceMeta,
  type ActivityScope,
  type ActivitySource,
  type SpineLabels,
  type SpineLink,
} from "@/lib/activity-stream"
import type { JournalEntry } from "@/lib/types/journal"
import { cn } from "@/lib/utils"

import {
  ActivitySidebar,
  EMPTY_FACETS,
  TIME_RANGES,
  type EntityFocus,
  type FacetState,
  type SidebarIssue,
  type SidebarRoutine,
} from "./activity-sidebar"
import { ActivityOverview, iconFor } from "./activity-overview"
import { ActivityDetail } from "./activity-detail"
import { FeedRow } from "./feed-row"

/** Connection state as a word, not a button. */
function LiveBadge({ status }: { status: string }) {
  const connected = status === "connected"
  const degraded = status === "polling"
  return (
    <span
      title={
        connected
          ? "Streaming over SSE"
          : degraded
            ? "The stream is unavailable; falling back to polling every 5s"
            : "Connecting to the activity stream"
      }
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md px-2 py-1 font-mono text-[10px] uppercase tracking-wider",
        connected && "text-success",
        degraded && "text-warn",
        !connected && !degraded && "text-muted-foreground-soft",
      )}
    >
      <span
        aria-hidden
        className={cn(
          "h-1.5 w-1.5 rounded-full",
          connected && "bg-success animate-pulse",
          degraded && "bg-warn animate-pulse",
          !connected && !degraded && "bg-muted-foreground/40",
        )}
      />
      {connected ? "live" : degraded ? "polling" : "connecting"}
    </span>
  )
}

const MAX_BUFFERED = 2_000
const PAGE_SIZE = 300

export function ActivityStreamView({ workspaceId }: { workspaceId: string }) {
  const isMobile = useIsMobile()
  const lookup = useJournalLookup()

  const [facets, setFacets] = React.useState<FacetState>(EMPTY_FACETS)
  const [search, setSearch] = React.useState("")
  const [debouncedSearch, setDebouncedSearch] = React.useState("")
  const [pinned, setPinned] = React.useState<SpineLink | null>(null)
  const [selected, setSelected] = React.useState<JournalEntry | null>(null)
  const [railCollapsed, setRailCollapsed] = React.useState(false)
  const [focus, setFocus] = React.useState<EntityFocus | null>(null)

  // Entities for the rail. The journal lookup already carries crews, agents
  // and missions; routines come from the pipelines list so the rows can show
  // the same icon + colour the Routines rail does.
  const { pipelines } = usePipelines(workspaceId)
  const [issues, setIssues] = React.useState<SidebarIssue[]>([])
  React.useEffect(() => {
    let cancelled = false
    apiFetch(`/api/v1/missions?workspace_id=${encodeURIComponent(workspaceId)}&limit=200`)
      .then(async (r) => (r.ok ? await r.json() : []))
      .then((d: unknown) => {
        if (cancelled) return
        const arr = Array.isArray(d) ? d : ((d as { missions?: unknown[] })?.missions ?? [])
        setIssues(arr as SidebarIssue[])
      })
      .catch(() => {
        if (!cancelled) setIssues([])
      })
    return () => {
      cancelled = true
    }
  }, [workspaceId])

  React.useEffect(() => {
    const t = setTimeout(() => setDebouncedSearch(search.trim()), 280)
    return () => clearTimeout(t)
  }, [search])

  const range = TIME_RANGES.find((r) => r.key === facets.range) ?? TIME_RANGES[1]

  // Server-side wherever the endpoint can express it: the journal already
  // filters on entry_type, severity, crew_ids, agent_ids, since, q and
  // mission_id, so the page filters the whole table, not the page it holds.
  const params = React.useMemo<Record<string, string | undefined>>(() => {
    const entryTypes =
      facets.scope === "active"
        ? ACTIVE_ENTRY_TYPES
        : facets.scope === "waiting"
          ? sourceEntryTypes("human")
          : facets.sources.length > 0
            ? facets.sources.flatMap((s) => sourceEntryTypes(s))
            : []

    const severities =
      facets.scope === "failed" ? ["error"] : facets.severities.length > 0 ? facets.severities : []

    return {
      since: new Date(Date.now() - range.ms).toISOString(),
      entry_type: entryTypes.length > 0 ? entryTypes.join(",") : undefined,
      severity: severities.length > 0 ? severities.join(",") : undefined,
      // Per-minute container/exec telemetry is excluded unless asked for.
      // Without this the eight most recent rows are eight CPU samples — a
      // feed that is live and says nothing.
      exclude_entry_type: facets.showTelemetry ? undefined : NOISE_ENTRY_TYPES.join(","),
      crew_ids: facets.crewIDs.length > 0 ? facets.crewIDs.join(",") : undefined,
      agent_ids: facets.agentIDs.length > 0 ? facets.agentIDs.join(",") : undefined,
      // An issue focus IS expressible server-side, so it narrows the whole
      // table rather than the loaded page.
      mission_id: focus?.kind === "issue" ? focus.id : pinned?.kind === "issue" ? pinned.id : undefined,
      crew_id: focus?.kind === "crew" ? focus.id : undefined,
      q: debouncedSearch || undefined,
    }
  }, [facets, pinned, focus, range.ms, debouncedSearch])

  const { entries, loading, loadingMore, error, nextCursor, refresh, loadMore, prependLive } =
    useJournalList({ workspaceId, params, limit: PAGE_SIZE, maxEntries: MAX_BUFFERED })

  const { status: streamStatus } = useJournalStream({
    workspaceId,
    params,
    enabled: true,
    onEntry: prependLive,
  })

  // Routine / run / step ids live inside the payload, which the API cannot
  // filter on — so a crumb of those kinds narrows here, over the loaded
  // window, and the chip says so rather than implying a full-table match.
  // Narrowed by the pinned crumb and the entity focus, but NOT by the status
  // facet — this is the set the rail's status counts are taken over, so each
  // count answers "how many would I get if I also clicked this".
  const focusScoped = React.useMemo(() => {
    let out = entries
    if (pinned && pinned.kind !== "issue") {
      out = out.filter((e) => {
        const bag = { ...(e.payload ?? {}), ...(e.refs ?? {}) }
        if (pinned.kind === "routine") {
          return bag["pipeline_slug"] === pinned.id || bag["routine_slug"] === pinned.id
        }
        if (pinned.kind === "run") return bag["run_id"] === pinned.id
        return bag["step_id"] === pinned.id || bag["step"] === pinned.id
      })
    }
    // A routine focus cannot go to the server: the slug lives inside the
    // payload, which the journal does not index. Narrowed here, over the
    // loaded window, and the chip says so.
    return narrowToFocus(out, focus)
  }, [entries, pinned, focus])

  // What the feed shows and the cards count: the focused set with the status
  // facet applied on top.
  //
  // The split matters because the two were computed from different windows
  // before. The cards were built from the focused set while the rail's counts
  // came from the whole loaded window, so a screen focused on one routine read
  // "FAILED 0 — nothing broke" beside a rail reading "Failed 9" — one screen,
  // two answers to "did anything break", and the reassuring one was wrong.
  const visible = React.useMemo(() => {
    // `done` has no server-side expression (it is "everything else"), so it
    // is the one scope narrowed client-side.
    if (facets.scope === "done") return focusScoped.filter((e) => scopeOf(e) === "done")
    return focusScoped
  }, [focusScoped, facets.scope])

  const scopeCounts = React.useMemo(() => {
    const c: Record<ActivityScope, number> = { active: 0, waiting: 0, failed: 0, done: 0 }
    // Counted over the FOCUSED window, so the rail and the cards answer the
    // same question. When a scope facet is active the server already narrowed
    // the fetch, so the other buckets are not knowable from what was loaded —
    // that is a property of the query, not a zero.
    for (const e of focusScoped) c[scopeOf(e)] += 1
    return c
  }, [focusScoped])

  const crewCounts = React.useMemo(() => {
    const c: Record<string, number> = {}
    for (const e of focusScoped) if (e.crew_id) c[e.crew_id] = (c[e.crew_id] ?? 0) + 1
    return c
  }, [focusScoped])

  const issueCounts = React.useMemo(() => {
    const c: Record<string, number> = {}
    for (const e of entries) if (e.mission_id) c[e.mission_id] = (c[e.mission_id] ?? 0) + 1
    return c
  }, [entries])

  const routineCounts = React.useMemo(() => {
    const c: Record<string, number> = {}
    for (const e of entries) {
      const bag = { ...(e.payload ?? {}), ...(e.refs ?? {}) }
      const slug = bag["pipeline_slug"] ?? bag["routine_slug"]
      if (typeof slug === "string" && slug) c[slug] = (c[slug] ?? 0) + 1
    }
    return c
  }, [entries])

  const routines = React.useMemo<SidebarRoutine[]>(
    () =>
      pipelines.map((p) => ({
        id: p.id,
        slug: p.slug,
        name: p.name,
        icon: (p as { icon?: string | null }).icon ?? null,
        color: (p as { color?: string | null }).color ?? null,
        invocation_count: p.invocation_count,
        last_invocation_status: p.last_invocation_status ?? null,
      })),
    [pipelines],
  )

  const labels = React.useMemo<SpineLabels>(() => {
    const issues: Record<string, string> = {}
    lookup.missions.forEach((m, id) => {
      issues[id] = m.title.length > 30 ? `${m.title.slice(0, 29)}…` : m.title
    })
    return { issues }
  }, [lookup.missions])

  const crews = React.useMemo(
    () => [...lookup.crews.values()].map((c) => ({ id: c.id, name: c.name, icon: c.icon, color: c.color })),
    [lookup.crews],
  )
  const agents = React.useMemo(
    () => [...lookup.agents.values()].map((a) => ({ id: a.id, name: a.name, crew_id: a.crew_id })),
    [lookup.agents],
  )

  const agentName = React.useCallback(
    (id?: string) => (id ? lookup.agents.get(id)?.name : undefined),
    [lookup.agents],
  )
  const crewName = React.useCallback(
    (id?: string) => (id ? lookup.crews.get(id)?.name : undefined),
    [lookup.crews],
  )
  const crewMeta = React.useCallback(
    (id?: string) => {
      const c = id ? lookup.crews.get(id) : undefined
      return c ? { icon: c.icon, color: c.color } : undefined
    },
    [lookup.crews],
  )

  // Keyboard: a surface you can only drive with a mouse is one you abandon
  // on the second screenful.
  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = document.activeElement
      if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) return
      if (e.key === "Escape") {
        setSelected(null)
        return
      }
      if (e.key !== "j" && e.key !== "k") return
      e.preventDefault()
      const i = selected ? visible.findIndex((x) => x.id === selected.id) : -1
      const next = e.key === "j" ? Math.min(i + 1, visible.length - 1) : Math.max(i - 1, 0)
      if (visible[next]) setSelected(visible[next])
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [visible, selected])

  const chips: { label: string; onClear: () => void }[] = []
  if (debouncedSearch) chips.push({ label: `“${debouncedSearch}”`, onClear: () => setSearch("") })
  if (pinned) {
    chips.push({
      label:
        pinned.kind === "issue" ? `issue: ${pinned.label}` : `${pinned.kind}: ${pinned.label} (loaded window)`,
      onClear: () => setPinned(null),
    })
  }
  if (focus) {
    chips.push({
      label:
        focus.kind === "routine"
          ? `routine: ${focus.label} (loaded window)`
          : `${focus.kind}: ${focus.label}`,
      onClear: () => setFocus(null),
    })
  }
  if (facets.range !== "24h") {
    chips.push({ label: range.label, onClear: () => setFacets({ ...facets, range: "24h" }) })
  }
  for (const s of facets.sources) {
    chips.push({
      label: sourceMeta(s).label,
      onClear: () => setFacets({ ...facets, sources: facets.sources.filter((x) => x !== s) }),
    })
  }
  for (const s of facets.severities) {
    chips.push({
      label: s,
      onClear: () => setFacets({ ...facets, severities: facets.severities.filter((x) => x !== s) }),
    })
  }
  for (const id of facets.crewIDs) {
    chips.push({
      label: lookup.crews.get(id)?.name ?? id,
      onClear: () => setFacets({ ...facets, crewIDs: facets.crewIDs.filter((x) => x !== id) }),
    })
  }
  for (const id of facets.agentIDs) {
    chips.push({
      label: lookup.agents.get(id)?.name ?? id,
      onClear: () => setFacets({ ...facets, agentIDs: facets.agentIDs.filter((x) => x !== id) }),
    })
  }

  const scopeMeta = ACTIVITY_SCOPES.find((s) => s.key === facets.scope)

  return (
    <div className="flex h-[calc(100vh-48px)] flex-col bg-background">
      <SubBar
        icon={Activity}
        title="Activity"
        section={scopeMeta?.label}
        ariaLabel="Activity"
        description={
          <>
            {visible.length.toLocaleString()} {visible.length === 1 ? "event" : "events"} ·{" "}
            {range.label.toLowerCase()}

          </>
        }
        actions={
          // No Pause, no Refresh. This surface is always live: the journal
          // pushes over SSE and falls back to polling on its own, so a
          // Refresh button would only ever advertise a staleness that is
          // not there, and a Pause is a way to be shown old numbers by
          // accident. The status word next to the count is the whole
          // control surface.
          <LiveBadge status={streamStatus} />
        }
      />

      <div className="relative flex min-h-0 flex-1 overflow-hidden">
        {isMobile && !railCollapsed && (
          <button
            type="button"
            aria-label="Close filters"
            onClick={() => setRailCollapsed(true)}
            className="absolute inset-0 z-20 bg-black/50"
          />
        )}

        <aside
          className={cn(
            "shrink-0 overflow-hidden border-r border-white/[0.06] bg-card transition-all",
            railCollapsed ? "w-9" : "w-[280px]",
            isMobile && !railCollapsed && "absolute inset-y-0 left-0 z-30 shadow-2xl",
          )}
        >
          {railCollapsed ? (
            <div className="flex h-full flex-col items-center pt-1.5">
              <SidebarCollapseButton collapsed onToggle={() => setRailCollapsed(false)} />
            </div>
          ) : (
            <ActivitySidebar
              search={search}
              onSearchChange={setSearch}
              facets={facets}
              onChange={setFacets}
              crews={crews}
              agents={agents}
              issues={issues}
              routines={routines}
              scopeCounts={scopeCounts}
              crewCounts={crewCounts}
              issueCounts={issueCounts}
              routineCounts={routineCounts}
              focus={focus}
              onFocus={setFocus}
              total={focusScoped.length}
              onToggleCollapse={() => setRailCollapsed(true)}
            />
          )}
        </aside>

        <div className="flex min-w-0 flex-1 flex-col">
          {/* An opened activity takes the whole content area. It was a 340px
              right panel first, and an execution graph does not fit in a
              column — the shape of a run IS the information. */}
          {selected ? (
            <div className="min-h-0 flex-1 overflow-y-auto">
              <ActivityDetail
                entry={selected}
                workspaceId={workspaceId}
                labels={labels}
                agentName={agentName}
                crewName={crewName}
                crewMeta={crewMeta}
                onBack={() => setSelected(null)}
                onSelectEntry={setSelected}
                onSpineClick={(l) => {
                  setSelected(null)
                  setPinned(l)
                }}
              />
            </div>
          ) : (
          <div className="flex min-h-0 min-w-0 flex-col">
            {chips.length > 0 && (
              <SidebarActiveChips className="shrink-0 border-b border-white/[0.06] px-4 py-1.5">
                {chips.map((c) => (
                  <SidebarActiveChip key={c.label} onRemove={c.onClear}>
                    {c.label}
                  </SidebarActiveChip>
                ))}
              </SidebarActiveChips>
            )}

            <div className="min-h-0 flex-1 overflow-y-auto">
              {loading && entries.length === 0 && (
                <div className="flex items-center justify-center gap-2 py-20 text-xs text-muted-foreground">
                  <Spinner className="h-3.5 w-3.5" /> Loading activity…
                </div>
              )}

              {error && !loading && (
                <div className="px-6 py-14">
                  <EmptyState
                    icon={Activity}
                    title="Could not load activity"
                    description={error}
                    action={
                      <Button size="sm" variant="outline" onClick={() => void refresh()}>
                        Try again
                      </Button>
                    }
                  />
                </div>
              )}

              {!loading && !error && facets.scope === "all" && (
                <ActivityOverview
                  entries={visible}
                  rangeLabel={range.label}
                  labels={labels}
                  agentName={agentName}
                  crewName={crewName}
                  crewMeta={crewMeta}
                  selectedID={undefined}
                  onSelect={setSelected}
                  onSpineClick={setPinned}
                  onScope={(s) => setFacets({ ...facets, scope: s })}
                  onSource={(s: ActivitySource) =>
                    setFacets({
                      ...facets,
                      sources: facets.sources.includes(s)
                        ? facets.sources.filter((x) => x !== s)
                        : [...facets.sources, s],
                    })
                  }
                />
              )}

              {!loading && !error && facets.scope !== "all" && (
                <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
                  <Appear order={0}>
                    <div>
                      <h1 className="text-lg font-semibold tracking-tight">{scopeMeta?.label}</h1>
                      <p className="text-xs text-muted-foreground">
                        {visible.length.toLocaleString()} in {range.label.toLowerCase()}
                      </p>
                    </div>
                  </Appear>
                  <Appear order={1}>
                    <DashboardCard title={scopeMeta?.label ?? "Activity"} icon={Activity} hint={`${visible.length}`}>
                      {visible.length === 0 ? (
                        <div className="py-8">
                          <EmptyState
                            icon={Activity}
                            title="Nothing here"
                            description="No event in this window matches. Widen the range or clear a filter."
                            action={
                              <Button size="sm" variant="outline" onClick={() => setFacets(EMPTY_FACETS)}>
                                Reset
                              </Button>
                            }
                          />
                        </div>
                      ) : (
                        <div className="flex flex-col">
                          {visible.map((e) => (
                            <FeedRow
                              key={e.id}
                              entry={e}
                              icon={iconFor(e)}
                              labels={labels}
                              actorName={agentName(e.agent_id)}
                              crewName={crewName(e.crew_id)}
                              agentId={e.agent_id}
                              crewIcon={crewMeta(e.crew_id)?.icon}
                              crewColor={crewMeta(e.crew_id)?.color}
                              selected={false}
                              onSelect={() => setSelected(e)}
                              onSpineClick={setPinned}
                            />
                          ))}
                        </div>
                      )}
                    </DashboardCard>
                  </Appear>
                  {nextCursor && (
                    <div className="flex justify-center">
                      <Button size="sm" variant="outline" disabled={loadingMore} onClick={() => void loadMore()}>
                        {loadingMore && <Spinner className="mr-1.5 h-3 w-3" />}
                        Load older
                      </Button>
                    </div>
                  )}
                </div>
              )}
            </div>
          </div>
          )}
        </div>
      </div>
    </div>
  )
}
