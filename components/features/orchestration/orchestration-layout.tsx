"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { CircleDot, FolderKanban, Kanban, List, Plus, X } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import { toast } from "@/lib/toast"
import { useAppStore } from "@/lib/store"
import { buildReturnTo } from "@/lib/return-to"
import { Button } from "@/components/ui/button"
import { SubBar, SubBarIconButton, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { SecondaryPanel, useCloseSecondaryPanelSheet } from "@/components/layout/secondary-panel"
import { OfflineBanner, PageEmpty, PageError, PageLoading, PageNoResults, PageNotFound, StaleData } from "@/components/layout/page-state"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceToggleRow,
} from "@/components/layout/create-surface"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import type { IssueLabel, Mission, Project, SavedView } from "@/lib/types/mission"
import type { CrewSummary } from "@/lib/types/orchestration"
import { useIsMobile } from "@/hooks/use-mobile"
import { useUserPreference } from "@/hooks/use-user-preference"
import { useFilteredIssues } from "@/hooks/use-filtered-issues"
import { useIssueDetail } from "@/hooks/use-issue-detail"
import { useIssuesList } from "@/hooks/use-issues-list"
import { useIssueBoardRealtime } from "@/hooks/use-issue-board-realtime"
import { useProjectDetail } from "@/hooks/use-project-detail"
import { useIssueFilters, activeFilterCount, filtersFromSavedView, savedViewPayload, NO_FILTERS } from "@/hooks/use-issue-filters"
import { useRealtimeStatusSafe } from "@/hooks/use-realtime"
import { useSessionSafe } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { isAdminTier } from "@/lib/permissions/tiers"
import { usePipelines } from "@/hooks/use-pipelines"
import { parseSavedViews, issueViews } from "@/lib/saved-views"
import { IssuesListView } from "@/components/features/issues/issues-list-view"
import { IssuesBoardView } from "@/components/features/issues/issues-board-view"
import { IssuePeek } from "@/components/features/issues/issue-peek"
import { IssuesDisplayOptions } from "@/components/features/issues/issues-display-options"
import { Segmented } from "@/components/ui/segmented"
import {
  DEFAULT_ROW_FIELDS,
  PRIORITY_TEXT,
  QUICK_VIEWS,
  STATUS_TEXT,
  applyQuickView,
  groupIssues,
  orderIssues,
  type GroupBy,
  type OrderBy,
  type QuickView,
  type RowFields,
} from "@/components/features/issues/issues-view-model"
import { ProjectDetailSkeleton, ProjectDetailSurface } from "@/components/features/issues/project-detail-surface"
import { CreateIssueModal } from "@/components/features/orchestration/create-issue-modal"
import { CreateProjectModal } from "@/components/features/orchestration/create-project-modal"
import { IssuesPanel, panelKeyOrder, selectionLabel, type IssuesPanelSelection } from "@/components/features/orchestration/issues-panel"
import { IssuesToolbarStrip } from "@/components/features/orchestration/issues-toolbar-strip"
import { arrivalFromParams } from "@/components/features/orchestration/issues-page-links"


export interface OrchestrationLayoutProps {
  crews: CrewSummary[]
  workspaceId: string
  /** Re-fetch the shell's own data (crews). */
  onRefresh: () => void
  /** The shell's fetch failed; said above the list with a retry, never as "no issues". */
  loadError?: string | null
}

// Stable: the realtime hook cancels its pending refetch when this changes.
const NOOP = () => {}

const isTyping = (t: EventTarget | null) => {
  const el = t as HTMLElement | null
  return !!el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT" || el.isContentEditable)
}

/**
 * /issues (Harbor 2): the secondary panel with views, projects, saved views
 * and the filter; the list or the board; a peek for the issue in focus; a
 * project's page when one is open. The bottom drawer the old orchestration
 * container carried is gone — its contents live in Activity and on the
 * issue's own page.
 */
export function OrchestrationLayout({ crews, workspaceId, onRefresh, loadError = null }: OrchestrationLayoutProps) {
  const router = useRouter()
  const isMobile = useIsMobile()
  const closePanelSheet = useCloseSecondaryPanelSheet()
  const openPanelSheet = useAppStore((s) => s.setSecondaryPanelOpen)
  const params = useSearchParams()
  const missionType = params.get("mission_type") ?? "issue"
  const me = useSessionSafe().data?.user.id ?? null
  // DELETE /projects/{id} is ADMIN+ (roleManage); others never see the button.
  const canDeleteProject = isAdminTier(useWorkspace().role)

  /* ------------------------------------------------------------ display */
  const [viewMode, setViewMode] = useUserPreference<"board" | "list">("orchestration.issues.viewMode", "list")
  const [groupBy, setGroupBy] = useUserPreference<GroupBy>("issues.list.groupBy", "status")
  const [orderBy, setOrderBy] = useUserPreference<OrderBy>("issues.list.order", "manual")
  const [storedFields, setFields] = useUserPreference<RowFields>("issues.list.fields", DEFAULT_ROW_FIELDS)
  const fields = useMemo(() => ({ ...DEFAULT_ROW_FIELDS, ...storedFields }), [storedFields])
  const [displayOpen, setDisplayOpen] = useState(false)

  /* ------------------------------------------------------------ filters */
  const { filters, setFilters, patch: patchFilters, clear: clearFacets } = useIssueFilters()
  const [search, setSearch] = useState("")
  const [committedSearch, setCommittedSearch] = useState("")
  useEffect(() => {
    const t = setTimeout(() => setCommittedSearch(search.trim()), 300)
    return () => clearTimeout(t)
  }, [search])
  const [quick, setQuick] = useState<QuickView>("all")
  const [savedViewId, setSavedViewId] = useState<string | null>(null)
  const [filterProjectId, setFilterProjectId] = useState<string | null>(null)

  const {
    issues,
    loading: issuesLoading,
    error: issuesError,
    total: issuesTotal,
    hasMore,
    loadingMore,
    refetch: fetchIssues,
    loadMore,
  } = useIssuesList(workspaceId, { search: committedSearch, crewId: filters.crewId, assigneeId: filters.agentId, missionType })

  // Agents create and advance issues live; every issue.* event (and a
  // reconnect) refetches the list, debounced.
  useIssueBoardRealtime({ filterCrewId: filters.crewId, fetchIssues, onRefresh: NOOP })

  /* ------------------------------------------------- workspace lookups */
  const [labels, setLabels] = useState<IssueLabel[]>([])
  const [projects, setProjects] = useState<Project[]>([])
  /** Whether the project list has answered: a project link is only "not found" once it has. */
  const [projectsLoad, setProjectsLoad] = useState<"loading" | "ready" | "error">("loading")
  const [savedViews, setSavedViews] = useState<SavedView[]>([])
  const { pipelines } = usePipelines(workspaceId)

  const fetchLabels = useCallback(async () => {
    try {
      const res = await apiFetch(`/api/v1/labels?workspace_id=${encodeURIComponent(workspaceId)}`)
      if (res.ok) setLabels(await res.json())
    } catch { /* the pickers show none */ }
  }, [workspaceId])
  const fetchProjects = useCallback(async () => {
    try {
      const res = await apiFetch(`/api/v1/projects?workspace_id=${encodeURIComponent(workspaceId)}`)
      if (res.ok) {
        setProjects(await res.json())
        setProjectsLoad("ready")
      } else setProjectsLoad((l) => (l === "ready" ? l : "error"))
    } catch {
      // The Projects section stays as it was.
      setProjectsLoad((l) => (l === "ready" ? l : "error"))
    }
  }, [workspaceId])
  const fetchSavedViews = useCallback(async () => {
    try {
      const res = await apiFetch(`/api/v1/saved-views?workspace_id=${encodeURIComponent(workspaceId)}`)
      // Journal views share the endpoint; applied here they would set nothing.
      if (res.ok) setSavedViews(issueViews(parseSavedViews(await res.json())))
    } catch { /* the Saved views section stays as it was */ }
  }, [workspaceId])
  useEffect(() => {
    void fetchLabels()
    void fetchProjects()
    void fetchSavedViews()
  }, [fetchLabels, fetchProjects, fetchSavedViews])

  /* ----------------------------------------------------------- selection */
  const issueDetail = useIssueDetail({ issues })
  const { selectedIdentifier, selectedIssue, handleIssueSelect, handleIssueClose } = issueDetail
  const { selectedProjectId, setSelectedProjectId, selectedProject, projectNotFound, projectTab, setProjectTab, handleProjectClose } =
    useProjectDetail({ projects, loaded: projectsLoad === "ready" })

  const selection: IssuesPanelSelection = selectedProjectId
    ? { kind: "project", id: selectedProjectId }
    : savedViewId
      ? { kind: "saved", id: savedViewId }
      : { kind: "quick", id: quick }

  const select = useCallback(
    (s: IssuesPanelSelection) => {
      closePanelSheet()
      if (s.kind === "project") {
        setSelectedProjectId(s.id)
        return
      }
      if (selectedProjectId) handleProjectClose()
      if (s.kind === "quick") {
        setSavedViewId(null)
        setQuick(s.id)
        if (savedViewId) {
          setFilters(NO_FILTERS)
          setFilterProjectId(null)
          setSearch("")
        }
        return
      }
      const view = savedViews.find((v) => v.id === s.id)
      if (!view) return
      const applied = filtersFromSavedView(view)
      setSavedViewId(view.id)
      setQuick("all")
      setFilters(applied.filters)
      setFilterProjectId(applied.projectId)
      setSearch(applied.search)
      if (view.view_type === "board" || view.view_type === "list") setViewMode(view.view_type)
    },
    [closePanelSheet, setSelectedProjectId, selectedProjectId, handleProjectClose, savedViewId, savedViews, setFilters, setViewMode],
  )

  /* -------------------------------------------------------------- data */
  const { visible: facetVisible } = useFilteredIssues({
    issues,
    search,
    selectedProjectId: null,
    filterProjectId,
    filterCrewId: filters.crewId,
    filterAgentId: filters.agentId,
    filterStatuses: filters.statuses,
    filterPriority: filters.priority,
    filterOwnerId: filters.ownerId,
    filterHolder: filters.holder,
    filterLabel: filters.label,
  })
  const visible = useMemo(() => orderIssues(applyQuickView(facetVisible, quick, me), orderBy), [facetVisible, quick, me, orderBy])
  const quickCounts = useMemo(
    () => Object.fromEntries(QUICK_VIEWS.map((v) => [v.id, applyQuickView(issues, v.id, me).length])) as Record<QuickView, number>,
    [issues, me],
  )
  // J / K follow what is on screen, group by group.
  const sequence = useMemo(
    () => groupIssues(visible, viewMode === "board" ? "status" : groupBy).flatMap((g) => g.issues),
    [visible, viewMode, groupBy],
  )
  const peekIndex = selectedIdentifier ? sequence.findIndex((i) => (i.identifier ?? i.id) === selectedIdentifier) : -1

  /* ------------------------------------------------------------ actions */
  const [showCreateIssue, setShowCreateIssue] = useState(false)
  /** The status a group's / column's + opened New issue with; cleared on close. */
  const [createStatus, setCreateStatus] = useState<string | null>(null)
  const openCreate = useCallback((status?: string) => {
    setCreateStatus(status ?? null)
    setShowCreateIssue(true)
  }, [])
  const openCreatePlain = useCallback(() => openCreate(), [openCreate])
  const [showCreateProject, setShowCreateProject] = useState(false)
  const [saveViewOpen, setSaveViewOpen] = useState(false)

  // The dashboard's "New issue" lands here with ?create=1. Consumed once:
  // without the strip-back a reload reopens the composer forever (#2187).
  // The Issues panel on /issues/<id> links here with one-time keys
  // (issues-page-links.ts): ?view=, ?saved=, ?q= and ?create=project. Read
  // once and stripped, like ?create=1.
  const [arrivalSaved, setArrivalSaved] = useState<string | null>(null)
  useEffect(() => {
    if (typeof window === "undefined") return
    const search = new URLSearchParams(window.location.search)
    const arrival = arrivalFromParams(search)
    const rest = new URLSearchParams(arrival.rest)
    if (search.get("create") === "1") {
      setShowCreateIssue(true)
      rest.delete("create")
    }
    if (arrival.quick) setQuick(arrival.quick)
    if (arrival.search) setSearch(arrival.search)
    if (arrival.savedId) setArrivalSaved(arrival.savedId)
    if (arrival.createProject) setShowCreateProject(true)
    const query = rest.toString()
    if (query !== search.toString()) {
      window.history.replaceState(window.history.state, "", `${window.location.pathname}${query ? `?${query}` : ""}${window.location.hash}`)
    }
  }, [])
  // A saved view applies once the list of them has answered.
  useEffect(() => {
    if (!arrivalSaved || !savedViews.some((v) => v.id === arrivalSaved)) return
    setArrivalSaved(null)
    select({ kind: "saved", id: arrivalSaved })
  }, [arrivalSaved, savedViews, select])

  const fullHref = useCallback((issue: Pick<Mission, "identifier" | "id">) => {
    const here = new URLSearchParams(typeof window === "undefined" ? "" : window.location.search)
    here.delete("issue")
    const from = `/issues${here.toString() ? `?${here}` : ""}`
    return `/issues/${encodeURIComponent(issue.identifier ?? issue.id)}?${buildReturnTo(from, "Issues")}`
  }, [])

  // A row opens the peek; on a phone the peek would be a sheet over a list
  // the reader cannot see, so it is the full page instead.
  const openIssue = useCallback(
    (issue: Mission) => {
      if (isMobile) {
        router.push(fullHref(issue))
        return
      }
      if ((issue.identifier ?? issue.id) === selectedIdentifier) return
      handleIssueSelect(issue)
    },
    [isMobile, router, fullHref, selectedIdentifier, handleIssueSelect],
  )
  const stepPeek = (delta: number) => {
    if (peekIndex < 0 || sequence.length === 0) return
    handleIssueSelect(sequence[(peekIndex + delta + sequence.length) % sequence.length])
  }

  const clearEverything = useCallback(() => {
    clearFacets()
    setSearch("")
    setQuick("all")
    setSavedViewId(null)
    setFilterProjectId(null)
  }, [clearFacets])

  /* ----------------------------------------------------------- keyboard */
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || isTyping(e.target) || e.metaKey || e.ctrlKey || e.altKey) return
      // A dialog, menu or the peek owns its keys.
      if (document.querySelector('[role="dialog"][data-state="open"], [role="alertdialog"], [role="menu"]')) return
      if (e.key === "/") {
        const el = document.querySelector<HTMLInputElement>("[data-issues-search] input")
        if (el) {
          e.preventDefault()
          el.focus()
          el.select()
        }
        return
      }
      if (e.key === "Escape") {
        if (activeFilterCount(filters) > 0 || search) {
          e.preventDefault()
          clearFacets()
          setSearch("")
        }
        return
      }
      if (e.key === "c" || e.key === "C") {
        if (e.shiftKey) return
        e.preventDefault()
        setShowCreateIssue(true)
        return
      }
      if (e.key === "V" && e.shiftKey) {
        e.preventDefault()
        setDisplayOpen((o) => !o)
        return
      }
      if (/^[1-9]$/.test(e.key)) {
        const target = panelKeyOrder(projects, savedViews)[Number(e.key) - 1]
        if (target) {
          e.preventDefault()
          select(target)
        }
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [filters, search, clearFacets, projects, savedViews, select])

  /* ------------------------------------------------------------ offline */
  const ws = useRealtimeStatusSafe()
  const [offline, setOffline] = useState(false)
  useEffect(() => {
    if (ws !== "disconnected" && ws !== "error") {
      setOffline(false)
      return
    }
    const t = setTimeout(() => setOffline(true), 3000)
    return () => clearTimeout(t)
  }, [ws])

  /* ------------------------------------------------------------- render */
  const sectionTitle = selectedProject
    ? selectedProject.name
    : projectNotFound
      ? "Project not found"
      : missionType === "mission"
      ? "Missions"
      : selectionLabel(selection, projects, savedViews)
  const nFilters = activeFilterCount(filters)
  const filteredAway = issues.length > 0 && visible.length === 0
  const nothingYet =
    !issuesLoading && !issuesError && issues.length === 0 && nFilters === 0 && !committedSearch && (issuesTotal ?? 0) === 0

  const filterWords = [
    quick === "mine" && "View: My work",
    filters.statuses.length > 0 && `Status: ${filters.statuses.map((s) => STATUS_TEXT[s] ?? s).join(", ")}`,
    filters.priority && `Priority: ${PRIORITY_TEXT[filters.priority]}`,
    filters.crewId && `Crew: ${crews.find((c) => c.id === filters.crewId)?.name ?? "one crew"}`,
    filters.agentId && "Delegate: one agent",
    filters.ownerId && "Owner: one person",
    filters.holder && `Held by ${filters.holder === "agent" ? "an agent" : "a person"}`,
    filters.label && `Label: ${filters.label}`,
    filterProjectId && `Project: ${projects.find((p) => p.id === filterProjectId)?.name ?? "one project"}`,
    search && `Search: “${search}”`,
  ].filter(Boolean) as string[]

  const actions = isMobile ? (
    <SubBarIconButton icon={Plus} aria-label="New issue" onClick={() => setShowCreateIssue(true)} />
  ) : selectedProjectId ? (
    <>
      <SubBarPrimary icon={Plus} title="C" aria-keyshortcuts="c" onClick={() => setShowCreateIssue(true)}>New issue</SubBarPrimary>
      <SubBarIconButton icon={X} aria-label="Close project" onClick={handleProjectClose} />
    </>
  ) : (
    <>
      <Segmented
        ariaLabel="View"
        size="xs"
        value={viewMode}
        onChange={setViewMode}
        options={[
          { value: "list", label: "List", icon: List },
          { value: "board", label: "Board", icon: Kanban },
        ]}
      />
      <IssuesDisplayOptions
        open={displayOpen}
        onOpenChange={setDisplayOpen}
        groupBy={groupBy}
        onGroupBy={setGroupBy}
        orderBy={orderBy}
        onOrderBy={setOrderBy}
        fields={fields}
        onFields={setFields}
        board={viewMode === "board"}
      />
      <SubBarSecondary icon={FolderKanban} onClick={() => setShowCreateProject(true)}>New project</SubBarSecondary>
      <SubBarPrimary icon={Plus} title="C" aria-keyshortcuts="c" onClick={() => setShowCreateIssue(true)}>New issue</SubBarPrimary>
    </>
  )

  let body: React.ReactNode
  if (selectedProject) {
    body = (
      <div className="h-full overflow-y-auto">
        <ProjectDetailSurface
          workspaceId={workspaceId}
          project={selectedProject}
          issues={issues}
          onChanged={fetchProjects}
          tab={projectTab}
          onTabChange={setProjectTab}
          onOpenIssue={openIssue}
          onNewIssue={openCreatePlain}
          canDelete={canDeleteProject}
          onDeleted={handleProjectClose}
        />
      </div>
    )
  } else if (selectedProjectId && projectNotFound) {
    body = (
      <PageNotFound
        what="project"
        description="It may have been deleted, or the link is wrong. Its issues, if it had any, are still in Issues."
        back={{ label: "Back to issues", onClick: handleProjectClose }}
      />
    )
  } else if (selectedProjectId && projectsLoad === "error") {
    body = (
      <PageError
        title="The project could not load"
        description="The project list did not answer. The link is kept."
        onRetry={() => void fetchProjects()}
        detail={<span className="font-mono">GET /api/v1/projects</span>}
      />
    )
  } else if (selectedProjectId) {
    body = <ProjectDetailSkeleton />
  } else if (issuesLoading && issues.length === 0) {
    body = <PageLoading variant="list" />
  } else if (issuesError && issues.length === 0) {
    body = (
      <PageError
        title="Issues could not load"
        description={`${issuesError.message} Your filters are kept.`}
        onRetry={() => void fetchIssues()}
        detail={issuesError.status ? <>HTTP {issuesError.status} from <span className="font-mono">GET /api/v1/issues</span></> : undefined}
      />
    )
  } else if (nothingYet) {
    body = (
      <PageEmpty
        icon={CircleDot}
        title="No issues yet"
        description="Write a brief, delegate it to an agent and review the result."
        primary={{ label: "New issue", onClick: () => setShowCreateIssue(true) }}
        cli="crewship issue create"
      />
    )
  } else {
    body = (
      <div className="flex h-full min-h-0 flex-col">
        <IssuesToolbarStrip
          statuses={filters.statuses}
          onToggleStatus={(s) =>
            patchFilters({ statuses: filters.statuses.includes(s) ? filters.statuses.filter((x) => x !== s) : [...filters.statuses, s] })
          }
          onClearStatuses={() => patchFilters({ statuses: [] })}
          shown={visible.length}
          groupBy={groupBy}
          board={viewMode === "board"}
          onOpenFilters={isMobile ? () => openPanelSheet(true) : undefined}
          activeFilters={nFilters}
          loaded={issues.length}
          total={issuesTotal}
          hasMore={hasMore}
          loadingMore={loadingMore}
          onLoadMore={() => void loadMore()}
        />
        {offline && <OfflineBanner className="mx-4 mt-3" onRetry={() => void fetchIssues()} />}
        {loadError && !issuesError && (
          <div role="alert" className="mx-4 mt-3 flex flex-wrap items-center gap-2 rounded-card border border-destructive/40 bg-destructive/10 px-3 py-2 text-label text-destructive">
            <span className="min-w-0 flex-1">{loadError}</span>
            <Button size="sm" variant="outline" onClick={() => { void fetchIssues(); onRefresh() }}>Try again</Button>
          </div>
        )}
        {/* Reachable only from a failed loadMore: what is loaded stays, and the
            retry re-requests that page rather than resetting to page 1. */}
        {issuesError && issues.length > 0 && (
          <div role="alert" className="mx-4 mt-3 flex items-center justify-between gap-3 rounded-card border border-destructive/30 bg-destructive/10 px-3 py-2 text-label text-destructive">
            <span>{issuesError.message}</span>
            <Button variant="ghost" size="xs" onClick={() => void loadMore()} disabled={loadingMore}>
              {loadingMore ? "Retrying…" : "Retry"}
            </Button>
          </div>
        )}
        <div className="relative min-h-0 flex-1 overflow-auto" data-slot="issues-scroll">
          <StaleData stale={offline}>
            {filteredAway || (visible.length === 0 && (nFilters > 0 || committedSearch || quick !== "all")) ? (
              <PageNoResults filters={filterWords.join(" · ") || undefined} onClear={clearEverything} />
            ) : viewMode === "board" ? (
              <IssuesBoardView
                issues={visible}
                onIssueClick={openIssue}
                selectedIssueId={selectedIssue?.id}
                onCreateClick={openCreate}
                workspaceId={workspaceId}
                onMoved={() => void fetchIssues()}
              />
            ) : (
              <IssuesListView
                issues={visible}
                onIssueClick={openIssue}
                selectedIssueId={selectedIssue?.id}
                workspaceId={workspaceId}
                groupBy={groupBy}
                fields={fields}
                projects={projects}
                onCreate={openCreate}
                onBulkApplied={() => void fetchIssues()}
              />
            )}
          </StaleData>
        </div>
      </div>
    )
  }

  return (
    <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col bg-background">
      <SubBar icon={CircleDot} title="Issues" section={sectionTitle} ariaLabel="Issues" actions={actions} />
      <div className="relative flex min-h-0 flex-1">
        <SecondaryPanel id="issues" label="Issues views">
          <IssuesPanel
            issues={issues}
            quickCounts={quickCounts}
            projects={projects}
            savedViews={savedViews}
            crews={crews}
            search={search}
            onSearchChange={setSearch}
            filters={filters}
            onFilters={patchFilters}
            onClearFilters={clearFacets}
            selection={selection}
            onSelect={select}
            onNewProject={() => setShowCreateProject(true)}
            onSaveView={() => setSaveViewOpen(true)}
          />
        </SecondaryPanel>
        <section className="relative min-w-0 flex-1 overflow-hidden" aria-label={sectionTitle}>
          {body}
        </section>
      </div>

      {!isMobile && (
        <IssuePeek
          open={selectedIdentifier !== null}
          onOpenChange={(o) => {
            if (!o) handleIssueClose()
          }}
          identifier={selectedIdentifier}
          row={selectedIssue}
          workspaceId={workspaceId}
          onPrev={sequence.length > 1 && peekIndex >= 0 ? () => stepPeek(-1) : undefined}
          onNext={sequence.length > 1 && peekIndex >= 0 ? () => stepPeek(1) : undefined}
          onOpenFull={() => selectedIdentifier && router.push(fullHref({ identifier: selectedIdentifier, id: selectedIdentifier }))}
        />
      )}

      <CreateIssueModal
        open={showCreateIssue}
        onOpenChange={(o) => {
          setShowCreateIssue(o)
          if (!o) setCreateStatus(null)
        }}
        crews={crews}
        labels={labels}
        projects={projects}
        routines={pipelines}
        workspaceId={workspaceId}
        onCreated={() => {
          void fetchIssues()
          void fetchProjects()
        }}
        onLabelsChanged={fetchLabels}
        initialStatus={createStatus}
        initialProjectId={selectedProject?.id ?? null}
      />
      <CreateProjectModal
        open={showCreateProject}
        onOpenChange={setShowCreateProject}
        crews={crews}
        labels={labels}
        workspaceId={workspaceId}
        onCreated={fetchProjects}
      />
      <SaveViewDialog
        open={saveViewOpen}
        onOpenChange={setSaveViewOpen}
        workspaceId={workspaceId}
        filters={savedViewPayload(filters, filterProjectId, search)}
        viewType={viewMode}
        onSaved={(view) => {
          void fetchSavedViews()
          setSavedViewId(view.id)
        }}
      />
    </div>
  )
}

/** Save the current filters as a named view. */
export function SaveViewDialog({
  open,
  onOpenChange,
  workspaceId,
  filters,
  viewType,
  onSaved,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
  filters: Record<string, unknown>
  viewType: "board" | "list"
  onSaved: (view: SavedView) => void
}) {
  const [name, setName] = useState("")
  const [shared, setShared] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    if (open) {
      setName("")
      setShared(false)
      setError(null)
    }
  }, [open])

  const save = async () => {
    if (!name.trim() || busy) return
    setBusy(true)
    setError(null)
    try {
      const res = await apiFetch(`/api/v1/saved-views?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: name.trim(), filters_json: JSON.stringify(filters), view_type: viewType, shared }),
      })
      if (!res.ok) {
        const b = (await res.json().catch(() => null)) as { error?: string; detail?: string } | null
        setError(b?.detail || b?.error || `The view was not saved (HTTP ${res.status}).`)
        return
      }
      const view = (await res.json()) as SavedView
      toast.success(shared ? "View saved · shared with the workspace" : "View saved")
      onOpenChange(false)
      onSaved(view)
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not reach the server")
    } finally {
      setBusy(false)
    }
  }

  return (
    <CreateSurface open={open} onOpenChange={(o) => !busy && onOpenChange(o)} size="sm" onSubmit={() => void save()} dirty={name.trim() !== ""}>
      <CreateSurfaceHeader concept="issues" title="Save view" description="Keeps the current filters and layout under a name in the panel." onClose={() => !busy && onOpenChange(false)} />
      <CreateSurfaceBody>
        <CreateSurfaceField label="Name" htmlFor="save-view-name" error={error ?? undefined}>
          <Input id="save-view-name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="Launch blockers" />
        </CreateSurfaceField>
        <CreateSurfaceToggleRow
          label="Share with the workspace"
          hint="Everyone in the workspace sees it under Saved views."
          control={<Switch checked={shared} onCheckedChange={setShared} aria-label="Share with the workspace" />}
        />
      </CreateSurfaceBody>
      <CreateSurfaceFooter onCancel={() => onOpenChange(false)} primaryLabel={busy ? "Saving…" : "Save view"} onPrimary={() => void save()} primaryDisabled={!name.trim()} busy={busy} />
    </CreateSurface>
  )
}
