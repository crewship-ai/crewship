"use client"

import { useCallback, useEffect, useId, useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { Workflow, Plus, Upload, List } from "lucide-react"
import { SubBar, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { Button } from "@/components/ui/button"
import { useAppStore } from "@/lib/store"
import { apiFetch } from "@/lib/api-fetch"
import { useAbilities } from "@/hooks/use-abilities"
import { useIsMobile } from "@/hooks/use-mobile"
import { canKillRoutine, roleAtLeast } from "@/lib/routine-governance"
import { bulkRunNow, bulkSummary, bulkTurnOff } from "@/lib/routine-bulk"
import { toast } from "@/lib/toast"
import { useRoutinePurposes } from "@/hooks/use-routine-purposes"
import { usePipelines } from "@/hooks/use-pipelines"
import { useUrlSelection } from "@/hooks/use-issue-detail"
import { useActiveRoutineRuns, isAwaitingApproval } from "@/hooks/use-active-routine-runs"
import { SecondaryPanel, useCloseSecondaryPanelSheet } from "@/components/layout/secondary-panel"
import { listRoutineDrafts, type RoutineDraftListEntry } from "@/lib/routine-drafts"
import { RoutinesExplorer, ROUTINE_VIEWS, type PanelCrew, type RoutinesView } from "./routines-explorer"
import { isRoutineTestFixture, type RoutineFilterState as RoutineFilters } from "@/lib/routine-filters"
import { RoutineRunDetail } from "./routine-run-detail"
import { RoutinesWorkspace } from "./routines-workspace"
import { RoutinesDetailPanel } from "./routines-detail-panel"
import { RoutineNewWizard } from "./routine-new-wizard"
import { RoutinesViewPane, needsYou } from "./routines-views"
import { ImportRoutineDialog } from "./routine-import-dialog"
import { useUserPreference } from "@/hooks/use-user-preference"
import { useRoutinePanelIndex } from "@/hooks/use-routine-panel-index"

// Keep the shared panel mounted across the views, a routine and a run. The
// URL identifies the view (?view=needs|live|runs|drafts), the routine
// (?slug=) and the run (?run=); filters belong to the panel and survive
// navigation inside this area.

interface RoutinesLayoutProps {
  workspaceId: string
}

/** `?routine=` is the older spelling two callers still emit. */
const ROUTINE_SLUG_OPTIONS = { aliases: ["routine"] as const }

/** Creating or importing a routine is POST /pipelines/save|drafts|import,
 *  all roleCreate on the server: Manager and above. */
export const CREATE_ROUTINE_REASON = "Creating routines needs Manager or above. Ask a crew lead in Chat."

const EMPTY_FILTERS: RoutineFilters = {
  status: "all",
  invocations: "all",
  authorAgentId: null,
  showEphemeral: false,
  lifecycle: "all",
  crewId: null,
}

const LIST_VIEWS = new Set<RoutinesView>(["needs", "live", "runs", "drafts"])

function useWorkspaceCrews(workspaceId: string): ReadonlyMap<string, PanelCrew> {
  const [crews, setCrews] = useState<PanelCrew[]>([])
  useEffect(() => {
    const controller = new AbortController()
    apiFetch(`/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}`, { signal: controller.signal })
      .then((r) => (r.ok ? r.json() : []))
      // The crews route answers a bare array or, paginated, { data }.
      .then((body: PanelCrew[] | { data?: PanelCrew[] }) => {
        if (controller.signal.aborted) return
        setCrews(Array.isArray(body) ? body : Array.isArray(body?.data) ? body.data : [])
      })
      .catch(() => {})
    return () => controller.abort()
  }, [workspaceId])
  return useMemo(() => new Map(crews.map((c) => [c.id, { id: c.id, name: c.name }])), [crews])
}

export function RoutinesLayout({ workspaceId }: RoutinesLayoutProps) {
  const { pipelines: loadedPipelines, loading, error, refresh } = usePipelines(workspaceId)
  const pipelines = useRoutinePurposes(workspaceId, loadedPipelines)
  const { role } = useAbilities()
  const canCreate = roleAtLeast(role, "MANAGER")
  const isMobile = useIsMobile()
  const crews = useWorkspaceCrews(workspaceId)
  const [lastCrew] = useUserPreference<string | null>("routines.lastCrew", null)
  const { runs: activeRuns } = useActiveRoutineRuns()
  const reasonId = useId()
  // The panel is the shared SecondaryPanel: a column on desktop, a bottom
  // sheet on a phone. Picking something in it closes the sheet.
  const closePanelSheet = useCloseSecondaryPanelSheet()
  const openPanelSheet = useAppStore((s) => s.setSecondaryPanelOpen)
  const [search, setSearch] = useState("")
  const [filters, setFilters] = useState<RoutineFilters>(EMPTY_FILTERS)
  const visiblePipelines = useMemo(
    () => (filters.showTestRoutines ? pipelines : pipelines.filter((p) => !isRoutineTestFixture(p.slug))),
    [pipelines, filters.showTestRoutines],
  )
  const panelIndex = useRoutinePanelIndex(workspaceId, pipelines)
  // Select several in the panel (PNL-03): Turn off (Owner/Admin, one
  // disable per routine) and Run now (routines that need no answer).
  const nameOf = (slug: string) => pipelines.find((p) => p.slug === slug)?.name || slug
  const bulk = {
    canTurnOff: canKillRoutine(role),
    onTurnOff: async (slugs: string[]) => {
      const result = await bulkTurnOff(workspaceId, slugs, apiFetch)
      if (result.failed.length)
        toast.error(`Turned off ${result.done.length} of ${slugs.length}`, {
          description: result.failed.map((f) => `${nameOf(f.slug)}: ${f.reason}`).join(" · "),
        })
      else toast.success(`Turned off ${result.done.length} ${result.done.length === 1 ? "routine" : "routines"}`)
      changed()
    },
    onRunNow: async (slugs: string[]) => {
      const result = await bulkRunNow(workspaceId, slugs, apiFetch)
      const details = [
        ...result.skipped.map((s) => `${nameOf(s.slug)}: ${s.reason}`),
        ...result.failed.map((f) => `${nameOf(f.slug)}: ${f.reason}`),
      ].join(" · ")
      ;(result.failed.length || result.skipped.length ? toast.warning : toast.success)(bulkSummary(result), details ? { description: details } : undefined)
      changed()
    },
  }
  // The selected routine lives in the URL: /routines?slug=<slug> (every
  // routine link in the app points here; ?routine= is read as an alias).
  const [selectedRun, setSelectedRun] = useUrlSelection("run")
  const [selectedSlug, setSelectedSlug] = useUrlSelection("slug", ROUTINE_SLUG_OPTIONS)
  // `?view=` is the routine page's own view while a routine is open
  // (`?view=edit` opens the editor) and the list view otherwise. It never
  // carries over from one to the other: leaving or switching routines
  // clears it, or the next routine would open in the editor uninvited.
  const [viewParam, setViewParam] = useUrlSelection("view")
  // The routine page's tab (and a version opened in it) belong to the routine
  // that is open; leaving or switching routines clears them.
  const [, setTabParam] = useUrlSelection("tab")
  const [, setVersionParam] = useUrlSelection("version")
  const listView: RoutinesView = !selectedSlug && !selectedRun && LIST_VIEWS.has(viewParam as RoutinesView) ? (viewParam as RoutinesView) : "overview"
  const panelView: RoutinesView | null = selectedSlug || selectedRun ? null : listView
  const clearRoutineView = () => {
    setViewParam(null, { replace: true })
    setTabParam(null, { replace: true })
    setVersionParam(null, { replace: true })
  }
  const leaveRoutine = () => {
    clearRoutineView()
    setSelectedRun(null, { replace: true })
    setSelectedSlug(null)
  }
  // The routine and run pages put their actions in the top bar through this
  // slot (a node inside the SubBar's action area).
  const [pageActions, setPageActions] = useState<HTMLElement | null>(null)
  const [importDialogOpen, setImportDialogOpen] = useState(false)
  const [createDialogOpen, setCreateDialogOpen] = useState(false)
  const [drafts, setDrafts] = useState<RoutineDraftListEntry[] | null>(null)
  const loadDrafts = useCallback(() => {
    listRoutineDrafts(workspaceId).then(setDrafts).catch(() => setDrafts((d) => d ?? []))
  }, [workspaceId])
  useEffect(() => loadDrafts(), [loadDrafts])
  const changed = useCallback(() => {
    refresh()
    loadDrafts()
  }, [refresh, loadDrafts])

  // The lead's "Open the draft" link: /routines?draft=<slug>&draft_id=…
  // (&workspace=…). The routine page itself shows the draft, so the link
  // opens that page and the draft params are cleared from the address.
  const [linkedDraft, setLinkedDraft] = useUrlSelection("draft")
  const [linkedDraftId, setLinkedDraftId] = useUrlSelection("draft_id")
  const [linkedWorkspace, setLinkedWorkspace] = useUrlSelection("workspace")
  useEffect(() => {
    if (!linkedDraft) return
    const slug = linkedDraft
    setLinkedDraft(null, { replace: true })
    setLinkedDraftId(null, { replace: true })
    setLinkedWorkspace(null, { replace: true })
    setSelectedRun(null, { replace: true })
    clearRoutineView()
    setSelectedSlug(slug, { replace: true })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [linkedDraft, linkedDraftId, linkedWorkspace])

  const showView = (view: RoutinesView) => {
    setSelectedRun(null, { replace: true })
    setSelectedSlug(null, { replace: true })
    setTabParam(null, { replace: true })
    setVersionParam(null, { replace: true })
    setViewParam(view === "overview" ? null : view)
    closePanelSheet()
  }

  const openRun = (slug: string, runId: string) => {
    clearRoutineView()
    setSelectedSlug(slug, { replace: true })
    setSelectedRun(runId)
  }

  // Keyboard (mirrors /issues): `/` focuses the panel's search, `Esc`
  // clears every filter, `c` opens New routine (Manager and above), 1–5
  // switch the views. Skipped while typing.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null
      const isInputContext = target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.tagName === "SELECT" || target.isContentEditable)
      if (isInputContext || e.metaKey || e.ctrlKey || e.altKey) return
      // A dialog owns the keyboard while it is open.
      if (document.querySelector('[role="dialog"][data-state="open"], [role="alertdialog"][data-state="open"]')) return
      if (e.key === "/") {
        const el = document.querySelector<HTMLInputElement>("[data-routines-search] input")
        if (el) {
          e.preventDefault()
          el.focus()
          el.select()
        }
        return
      }
      if (e.key === "Escape") {
        const filtered =
          search ||
          filters.status !== "all" ||
          (filters.lifecycle ?? "all") !== "all" ||
          filters.crewId ||
          filters.trigger ||
          filters.invocations !== "all" ||
          filters.authorAgentId ||
          filters.showEphemeral ||
          filters.showTestRoutines
        if (filtered) {
          e.preventDefault()
          setSearch("")
          setFilters(EMPTY_FILTERS)
        }
        return
      }
      if (e.key === "c") {
        if (!canCreate) return
        e.preventDefault()
        setCreateDialogOpen(true)
        return
      }
      const n = Number(e.key)
      if (Number.isInteger(n) && n >= 1 && n <= ROUTINE_VIEWS.length) {
        e.preventDefault()
        showView(ROUTINE_VIEWS[n - 1].id)
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  })

  const handleSelect = (slug: string) => {
    setSelectedRun(null, { replace: true })
    clearRoutineView()
    setSelectedSlug(selectedSlug === slug && !selectedRun ? null : slug)
    closePanelSheet()
  }

  // Looked up from the loaded list so the breadcrumb can name the routine
  // without a second fetch; the detail fetches the full definition itself.
  const selectedRoutine = selectedSlug ? pipelines.find((p) => p.slug === selectedSlug) : null
  const crewName = selectedRoutine?.author_crew_id ? crews.get(selectedRoutine.author_crew_id)?.name : undefined
  const needsCount = useMemo(() => needsYou(visiblePipelines, activeRuns).length, [visiblePipelines, activeRuns])
  const liveCount = activeRuns.filter((r) => !isAwaitingApproval(r.status)).length
  const totalRuns = visiblePipelines.reduce((sum, p) => sum + (p.invocation_count ?? 0), 0)

  const section = selectedRun
    ? [selectedRoutine?.name || selectedSlug || "Routine", "Run"].join(" › ")
    : selectedSlug
      ? [crewName, selectedRoutine?.name || selectedSlug].filter(Boolean).join(" › ")
      : ROUTINE_VIEWS.find((v) => v.id === listView)!.label

  const createActions = (
    <>
      {!canCreate && (
        <span id={reasonId} className="sr-only">
          {CREATE_ROUTINE_REASON}
        </span>
      )}
      <span title={canCreate ? undefined : CREATE_ROUTINE_REASON} className="max-sm:hidden">
        <SubBarSecondary
          icon={Upload}
          onClick={() => setImportDialogOpen(true)}
          disabled={!canCreate}
          aria-describedby={canCreate ? undefined : reasonId}
          title={canCreate ? "Import a routine bundle from JSON" : undefined}
        >
          Import
        </SubBarSecondary>
      </span>
      <span title={canCreate ? undefined : CREATE_ROUTINE_REASON}>
        <SubBarPrimary
          icon={Plus}
          onClick={() => setCreateDialogOpen(true)}
          disabled={!canCreate}
          aria-describedby={canCreate ? undefined : reasonId}
          title={canCreate ? "New routine (C)" : undefined}
        >
          New routine
        </SubBarPrimary>
      </span>
    </>
  )

  return (
    <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col bg-background">
      {/* Routines › {view} / Routines › {crew} › {routine}. The area's own
          breadcrumb links back to the overview, so the content needs no
          "Back to routines" bar. New routine and Import belong to the list;
          a routine page carries its own actions. */}
      <SubBar
        icon={Workflow}
        title="Routines"
        section={section}
        description={
          !selectedSlug && !selectedRun ? (
            <>
              {visiblePipelines.length} {visiblePipelines.length === 1 ? "routine" : "routines"} · {totalRuns} {totalRuns === 1 ? "run" : "runs"}
            </>
          ) : undefined
        }
        ariaLabel="Routines"
        actions={!selectedSlug && !selectedRun ? createActions : <span ref={setPageActions} className="flex items-center gap-1.5" data-slot="routine-page-actions" />}
      />

      <div className="relative flex flex-1 overflow-hidden">
        <SecondaryPanel id="routines" label="Routine list">
          <RoutinesExplorer
            routines={visiblePipelines}
            search={search}
            onSearchChange={setSearch}
            selectedSlug={selectedSlug}
            onSelectRoutine={handleSelect}
            filters={filters}
            onChange={setFilters}
            view={panelView}
            onViewChange={showView}
            counts={{ needs: needsCount, live: liveCount, drafts: drafts?.length ?? null }}
            crews={crews}
            index={panelIndex}
            bulk={isMobile ? undefined : bulk}
          />
        </SecondaryPanel>

        <div className="flex min-w-0 flex-1 flex-col overflow-hidden bg-background">
          {/* On a phone the panel is a sheet; this chip is the way to it. */}
          {isMobile && (
            <div className="flex shrink-0 items-center gap-2 border-b border-hairline px-3 py-1.5">
              <Button type="button" variant="outline" size="sm" onClick={() => openPanelSheet(true)} className="h-8 gap-1.5 text-xs coarse:h-12">
                <List className="h-3.5 w-3.5" />
                {crewName && selectedSlug ? crewName : "All routines"}
              </Button>
            </div>
          )}
          <div className="relative min-h-0 flex-1">
            <AnimatePresence mode="wait">
              {selectedRun ? (
                <div key={selectedRun} className="absolute inset-0 overflow-auto">
                  <RoutineRunDetail
                    key={selectedRun}
                    workspaceId={workspaceId}
                    runId={selectedRun}
                    actionsTarget={pageActions}
                    onClose={() => setSelectedRun(null)}
                    onOpenRun={(runId) => setSelectedRun(runId)}
                  />
                </div>
              ) : selectedSlug ? (
                <motion.div
                  key={`detail-${selectedSlug}`}
                  initial={{ opacity: 0, x: 12 }}
                  animate={{ opacity: 1, x: 0 }}
                  exit={{ opacity: 0, x: -12 }}
                  transition={{ duration: 0.18, ease: "easeOut" }}
                  className="absolute inset-0 flex flex-col overflow-hidden"
                >
                  <div className="flex-1 overflow-hidden">
                    <RoutinesDetailPanel
                      workspaceId={workspaceId}
                      slug={selectedSlug}
                      onClose={leaveRoutine}
                      onChanged={changed}
                      onRunStarted={setSelectedRun}
                      crewName={crewName}
                      routines={pipelines}
                      actionsTarget={pageActions}
                      onOpenRoutine={(slug) => {
                        clearRoutineView()
                        setSelectedSlug(slug)
                      }}
                    />
                  </div>
                </motion.div>
              ) : listView !== "overview" ? (
                <motion.div key={`view-${listView}`} initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.15 }} className="absolute inset-0 overflow-hidden">
                  <RoutinesViewPane
                    view={listView}
                    workspaceId={workspaceId}
                    routines={visiblePipelines}
                    drafts={drafts}
                    onSelect={handleSelect}
                    onOpenRun={openRun}
                  />
                </motion.div>
              ) : (
                <motion.div key="overview" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.15 }} className="absolute inset-0 overflow-hidden">
                  <RoutinesWorkspace
                    search={search}
                    filters={filters}
                    workspaceId={workspaceId}
                    routines={visiblePipelines}
                    loading={loading}
                    error={error}
                    onSelect={handleSelect}
                    onRetry={refresh}
                    onNew={canCreate ? () => setCreateDialogOpen(true) : undefined}
                  />
                </motion.div>
              )}
            </AnimatePresence>
          </div>
        </div>
      </div>

      {importDialogOpen && canCreate && (
        <ImportRoutineDialog
          workspaceId={workspaceId}
          crews={[...crews.values()]}
          defaultCrewId={lastCrew && crews.has(lastCrew) ? lastCrew : null}
          onClose={() => setImportDialogOpen(false)}
          onImported={(slug) => {
            changed()
            setImportDialogOpen(false)
            setSelectedRun(null, { replace: true })
            clearRoutineView()
            setSelectedSlug(slug)
          }}
        />
      )}

      {/* New routine — the five-step wizard. Saving turns it on (or sends it
          for a Manager review); Save as draft keeps it unpublished. */}
      {canCreate && (
        <RoutineNewWizard
          workspaceId={workspaceId}
          open={createDialogOpen}
          onOpenChange={setCreateDialogOpen}
          routines={pipelines}
          onCreated={changed}
          onImport={() => setImportDialogOpen(true)}
          onOpenRoutine={(slug) => {
            setSelectedRun(null, { replace: true })
            clearRoutineView()
            setSelectedSlug(slug)
          }}
        />
      )}
    </div>
  )
}
