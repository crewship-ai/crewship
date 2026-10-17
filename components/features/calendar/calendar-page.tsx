"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { CalendarDays, CalendarClock, ChevronLeft, ChevronRight, Layers, Plus } from "lucide-react"
import { SubBar, SubBarIconButton } from "@/components/layout/sub-bar"
import { SecondaryPanel, useCloseSecondaryPanelSheet } from "@/components/layout/secondary-panel"
import { PageEmpty, PageError, PageLoading, PageNoResults } from "@/components/layout/page-state"
import { Button } from "@/components/ui/button"
import { ButtonGroup } from "@/components/ui/button-group"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { useUrlSelection } from "@/hooks/use-issue-detail"
import { useIsMobile } from "@/hooks/use-mobile"
import { useUserPreference } from "@/hooks/use-user-preference"
import { useAbilities } from "@/hooks/use-abilities"
import { usePipelines } from "@/hooks/use-pipelines"
import { useSessionSafe } from "@/hooks/use-auth"
import { useCalendarEvents } from "@/hooks/use-calendar-events"
import { useAppStore } from "@/lib/store"
import { apiFetch } from "@/lib/api-fetch"
import { routinePermissions } from "@/lib/routine-governance"
import { routineTimeZone } from "@/lib/routine-time"
import { addDays, dateKey, parseCalendarDate } from "@/lib/routine-calendar"
import {
  CALENDAR_PAGE_VIEWS,
  CALENDAR_SCOPE_LABELS,
  CALENDAR_VIEW_LABELS,
  STATUS_FACETS,
  dayLabel,
  filterItems,
  isCalendarPageView,
  movePage,
  needYouSoon,
  pageRange,
  rangeTitle,
  routineItems,
  type CalendarFilters,
  type CalendarPageView,
  type CalendarScope,
  type StatusFacet,
} from "@/lib/calendar/model"
import { cn } from "@/lib/utils"
import { CalendarPanel, type CalendarCrew } from "./calendar-panel"
import { CalendarAgenda, CalendarMonth, CalendarWeek, STATUS_ICON } from "./calendar-views"
import { CalendarPeek } from "./calendar-peek"
import { ScheduleStartDialog } from "./schedule-start-dialog"

/** The next full hour, for "Add a date" without a clicked slot. */
const nextHour = (now = new Date()) => new Date(now.getFullYear(), now.getMonth(), now.getDate(), now.getHours() + 1)

function useCrews(workspaceId: string): CalendarCrew[] {
  const [crews, setCrews] = useState<CalendarCrew[]>([])
  useEffect(() => {
    const controller = new AbortController()
    void (async () => {
      try {
        const res = await apiFetch(`/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}`, { signal: controller.signal })
        if (!res.ok) return
        const rows = ((await res.json()) ?? []) as { id: string; name?: string; slug?: string }[]
        if (!controller.signal.aborted && Array.isArray(rows)) setCrews(rows.map((c) => ({ id: c.id, name: c.name || c.slug || c.id })))
      } catch {
        // The Crews section is an aid, not the page: without it the calendar still works.
      }
    })()
    return () => controller.abort()
  }, [workspaceId])
  return crews
}

/**
 * /calendar (Harbor 2, F-01). A view, not a store: every date belongs to an
 * object that already exists and changes there. The URL holds what a reload
 * keeps — the view and the day; Mine / Workspace is remembered per person.
 */
export function CalendarPage({ workspaceId }: { workspaceId: string }) {
  const isMobile = useIsMobile()
  const closePanelSheet = useCloseSecondaryPanelSheet()
  const openPanelSheet = useAppStore((s) => s.setSecondaryPanelOpen)
  const [viewParam, setViewParam] = useUrlSelection("view")
  const [dateParam, setDateParam] = useUrlSelection("date")
  const view: CalendarPageView = isCalendarPageView(viewParam) ? viewParam : isMobile ? "agenda" : "week"
  const anchor = parseCalendarDate(dateParam) ?? addDays(new Date(), 0)
  const { from, to } = pageRange(anchor, view)

  const [pref, setPref] = useUserPreference<{ scope?: CalendarScope }>("calendar.view", { scope: "mine" })
  const scope: CalendarScope = pref?.scope === "workspace" ? "workspace" : "mine"
  const [local, setLocal] = useState<{ crewId: string | null; statuses: StatusFacet[]; query: string }>({ crewId: null, statuses: [], query: "" })
  const filters: CalendarFilters = useMemo(() => ({ ...local, scope }), [local, scope])
  const onChange = useCallback(
    (patch: Partial<CalendarFilters>) => {
      const { scope: nextScope, ...rest } = patch
      if (nextScope) setPref({ ...pref, scope: nextScope })
      if (Object.keys(rest).length) setLocal((l) => ({ ...l, ...rest }))
    },
    [pref, setPref],
  )
  const clearFilters = () => setLocal({ crewId: null, statuses: [], query: "" })

  const { pipelines, error: routinesError } = usePipelines(workspaceId)
  const calendar = useCalendarEvents(workspaceId, from, to)
  const crews = useCrews(workspaceId)
  const meId = useSessionSafe().data?.user.id ?? null
  const { role, capabilities } = useAbilities()
  const perms = routinePermissions(role, capabilities)

  const items = useMemo(() => routineItems(calendar.events, pipelines, meId), [calendar.events, pipelines, meId])
  const visible = useMemo(() => filterItems(items, filters), [items, filters])
  const soon = needYouSoon(visible)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const selected = visible.find((i) => i.id === selectedId) ?? null
  const [planAt, setPlanAt] = useState<Date | null>(null)

  const crewName = filters.crewId ? crews.find((c) => c.id === filters.crewId)?.name ?? "Crew" : null
  const scopeTitle = crewName ? `${crewName} crew` : CALENDAR_SCOPE_LABELS[scope]

  const setView = (v: CalendarPageView) => setViewParam(v, { replace: true })
  const goTo = (d: Date) => setDateParam(dateKey(d), { replace: true })
  const openDay = (key: string) => {
    setDateParam(key, { replace: true })
    setViewParam("agenda", { replace: true })
  }
  const plan = perms.run ? (at: Date) => setPlanAt(at.getTime() > Date.now() ? at : nextHour()) : undefined

  // 1 / 2 switch Mine / Workspace; / focuses the panel's search.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable)) return
      if (e.metaKey || e.ctrlKey || e.altKey || document.querySelector("[role=dialog]")) return
      if (e.key === "1") onChange({ scope: "mine", crewId: null })
      else if (e.key === "2") onChange({ scope: "workspace", crewId: null })
      else if (e.key === "/") {
        const el = document.querySelector<HTMLInputElement>("[data-calendar-search] input")
        if (el) {
          e.preventDefault()
          el.focus()
        }
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [onChange])

  const addDate = (
    <Popover>
      <PopoverTrigger asChild>
        {isMobile ? (
          <SubBarIconButton icon={Plus} aria-label="Add a date" />
        ) : (
          <Button size="sm" variant="outline">
            <Plus className="size-3.5" aria-hidden />
            Add a date
          </Button>
        )}
      </PopoverTrigger>
      <PopoverContent align="end" className="w-72 p-1.5">
        <p className="px-2 pb-1 pt-1.5 text-micro font-semibold text-muted-foreground-soft">Add a date to…</p>
        <button
          type="button"
          disabled={!perms.run}
          onClick={() => setPlanAt(nextHour())}
          className="kit-tap flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-left hover:bg-muted disabled:cursor-not-allowed disabled:opacity-60"
        >
          <span className="grid size-6 shrink-0 place-items-center rounded-md bg-chip-violet-bg text-chip-violet-fg">
            <CalendarClock className="size-3.5" aria-hidden />
          </span>
          <span className="min-w-0">
            <b className="block text-body font-medium">A routine start</b>
            <small className="block text-label text-muted-foreground">{perms.run ? "One time or repeating" : "You cannot start routines in this workspace"}</small>
          </span>
        </button>
        <p className="px-2 pb-1 pt-2 text-label text-muted-foreground">The calendar has no events of its own. Each date lives on its routine.</p>
      </PopoverContent>
    </Popover>
  )

  const loading = calendar.loading && !calendar.events.length
  const noAnything = !loading && !calendar.error && items.length === 0
  const filteredAway = !loading && !calendar.error && items.length > 0 && visible.length === 0
  const filterWords = [
    CALENDAR_SCOPE_LABELS[scope],
    crewName,
    ...STATUS_FACETS.filter((f) => filters.statuses.includes(f.id)).map((f) => f.label),
    filters.query && `“${filters.query}”`,
  ]
    .filter(Boolean)
    .join(" · ")

  let body: React.ReactNode
  if (calendar.error || routinesError) {
    body = (
      <PageError
        title="The calendar could not be loaded"
        description="Nothing was changed. The dates stay on their routines; try again in a moment."
        onRetry={calendar.reload}
      />
    )
  } else if (loading) {
    body = <PageLoading rows={6} onRetry={calendar.reload} />
  } else if (noAnything) {
    body = (
      <PageEmpty
        icon={CalendarDays}
        title="Nothing scheduled"
        description="Routine starts and runs in this range appear here."
        primary={perms.run ? { label: "Schedule a start", onClick: () => setPlanAt(nextHour()) } : undefined}
        cli="crewship routine calendar"
      />
    )
  } else if (filteredAway) {
    body = (
      <PageNoResults
        filters={filterWords}
        onClear={filters.crewId || filters.statuses.length || filters.query ? clearFilters : undefined}
        onWiden={scope === "mine" ? () => onChange({ scope: "workspace" }) : undefined}
        widenLabel="Show Workspace"
      />
    )
  } else if (view === "week") {
    body = <CalendarWeek items={visible} from={from} selectedId={selectedId} onPick={setSelectedId} onOpenDay={openDay} onPlan={plan} />
  } else if (view === "month") {
    body = <CalendarMonth items={visible} anchor={anchor} selectedId={selectedId} onPick={setSelectedId} onOpenDay={openDay} />
  } else {
    body = <CalendarAgenda items={visible} scope={scope} selectedId={selectedId} onPick={setSelectedId} />
  }

  return (
    <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col bg-background">
      <SubBar icon={CalendarDays} title="Calendar" section={scopeTitle} heading={false} ariaLabel="Calendar" actions={isMobile ? addDate : undefined} />
      <div className="relative flex flex-1 overflow-hidden">
        <SecondaryPanel id="calendar" label="Calendar views and filters">
          <CalendarPanel items={items} filters={filters} crews={crews} onChange={onChange} onPicked={closePanelSheet} />
        </SecondaryPanel>
        <main className="min-w-0 flex-1 overflow-y-auto">
          <div className="mx-auto grid max-w-[1400px] gap-3 px-4 py-4 md:px-6 md:py-5">
            {isMobile && (
              <button
                type="button"
                onClick={() => openPanelSheet(true)}
                className="kit-tap inline-flex items-center gap-1.5 justify-self-start rounded-full border border-border bg-card px-3 py-1 text-label"
              >
                <Layers className="size-3.5" aria-hidden />
                {scopeTitle} · filters
              </button>
            )}
            <header className="min-w-0">
              <h1 className="text-title font-semibold tracking-tight">Calendar</h1>
              <p className="text-body text-muted-foreground" data-testid="calendar-summary">
                {scopeTitle} · {visible.length} {visible.length === 1 ? "date" : "dates"} you can open · {soon.length} need you soon
              </p>
            </header>
            {soon.length > 0 && (
              <div className="grid gap-2 max-md:flex max-md:overflow-x-auto md:grid-cols-[repeat(auto-fit,minmax(230px,1fr))]" aria-label="Need you soon" role="list">
                {soon.map((i) => {
                  const s = STATUS_ICON[i.status]
                  return (
                    <button
                      key={i.id}
                      type="button"
                      role="listitem"
                      onClick={() => setSelectedId(i.id)}
                      className="kit-tap flex min-w-[230px] items-start gap-2.5 rounded-xl border border-border bg-card px-3 py-2.5 text-left hover:border-foreground/20"
                    >
                      <s.icon className={cn("mt-0.5 size-3.5 shrink-0", s.className)} aria-label={s.label} />
                      <span className="min-w-0">
                        <b className="block truncate text-body font-medium">{i.title}</b>
                        <small className="block truncate text-label text-muted-foreground">
                          {dayLabel(i.day)} {i.time} · {s.label}
                        </small>
                      </span>
                    </button>
                  )
                })}
              </div>
            )}
            <div className="flex flex-wrap items-center gap-2.5">
              <ButtonGroup aria-label="Calendar view">
                {CALENDAR_PAGE_VIEWS.map((v) => (
                  <Button key={v} size="sm" variant={v === view ? "secondary" : "outline"} aria-pressed={v === view} onClick={() => setView(v)}>
                    {CALENDAR_VIEW_LABELS[v]}
                  </Button>
                ))}
              </ButtonGroup>
              <span className="inline-flex items-center gap-0.5">
                <Button size="icon-sm" variant="ghost" aria-label="Previous" onClick={() => goTo(movePage(anchor, view, -1))}>
                  <ChevronLeft className="size-3.5" />
                </Button>
                <Button size="sm" variant="outline" onClick={() => goTo(new Date())}>
                  Today
                </Button>
                <Button size="icon-sm" variant="ghost" aria-label="Next" onClick={() => goTo(movePage(anchor, view, 1))}>
                  <ChevronRight className="size-3.5" />
                </Button>
              </span>
              <h2 className="text-default font-semibold" data-testid="calendar-range">
                {rangeTitle(anchor, view)}
              </h2>
              <span className="font-mono text-label text-muted-foreground">{routineTimeZone()}</span>
              <span className="flex-1" />
              {!isMobile && addDate}
            </div>
            {calendar.truncated && (
              <p role="status" className="rounded-md border border-warn/30 bg-chip-warn-bg px-3 py-2 text-label text-chip-warn-fg">
                Some dates in this range are left out because there are too many. Pick a shorter view or narrow the filters.
              </p>
            )}
            {body}
          </div>
        </main>
      </div>
      <CalendarPeek
        item={selected}
        items={visible}
        workspaceId={workspaceId}
        routines={pipelines}
        permissions={{ run: perms.run, author: perms.author }}
        onSelect={setSelectedId}
        onChanged={calendar.reload}
      />
      {planAt && (
        <ScheduleStartDialog
          workspaceId={workspaceId}
          routines={pipelines}
          date={planAt}
          canRepeat={perms.createSchedule}
          onClose={() => setPlanAt(null)}
          onScheduled={() => {
            setPlanAt(null)
            calendar.reload()
          }}
        />
      )}
    </div>
  )
}
