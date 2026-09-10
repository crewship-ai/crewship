"use client"

import { useEffect, useId, useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { Workflow, Plus, Upload, ChevronLeft, ChevronRight } from "lucide-react"
import { SubBar, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
} from "@/components/layout/create-surface"
import { useAppStore } from "@/lib/store"
import { apiFetch } from "@/lib/api-fetch"
import { useRoutinePurposes } from "@/hooks/use-routine-purposes"
import { usePipelines } from "@/hooks/use-pipelines"
import { useUrlSelection } from "@/hooks/use-issue-detail"
import { SidebarCollapseButton } from "@/components/layout/sidebar-kit"
import { isRoutineTestFixture } from "@/lib/routine-filters"
import { cn } from "@/lib/utils"
import { useIsMobile } from "@/hooks/use-mobile"
import type { RoutineFilters } from "./routines-filter-sidebar"
import { RoutinesExplorer } from "./routines-explorer"
import { RoutineRunDetail } from "./routine-run-detail"
import { RoutinesWorkspace } from "./routines-workspace"
import { RoutinesDetailPanel } from "./routines-detail-panel"
import { RoutineCreateDialog } from "./routine-create-dialog"

// Keep the shared explorer mounted across overview, definition and historical
// run views. The URL identifies the routine and optional execution; filters
// belong to the explorer and survive navigation inside this workspace.

interface RoutinesLayoutProps {
  workspaceId: string
}

/** `?routine=` is the older spelling two callers still emit. */
const ROUTINE_SLUG_OPTIONS = { aliases: ["routine"] as const }

export function RoutinesLayout({ workspaceId }: RoutinesLayoutProps) {
  const { pipelines: loadedPipelines, loading, error, refresh } = usePipelines(workspaceId)
  const pipelines = useRoutinePurposes(workspaceId, loadedPipelines)
  const isMobile = useIsMobile()
  const [leftCollapsed, setLeftCollapsed] = useState(false)
  // On a phone the sidebar is 280px of a 390px screen — it does not
  // sit BESIDE the content, it replaces it. Collapse it when the
  // viewport narrows, and let it open as an overlay instead of a
  // column, so the overview keeps the full width it was designed for.
  useEffect(() => {
    if (isMobile) setLeftCollapsed(true)
  }, [isMobile])
  const [search, setSearch] = useState("")
  const [filters, setFilters] = useState<RoutineFilters>({
    status: "all",
    invocations: "all",
    authorAgentId: null,
    showEphemeral: false,
  })
  const visiblePipelines = useMemo(
    () =>
      filters.showTestRoutines
        ? pipelines
        : pipelines.filter((p) => !isRoutineTestFixture(p.slug)),
    [pipelines, filters.showTestRoutines],
  )
  // The selected routine lives in the URL: /routines?slug=<slug>.
  //
  // It used to be read from the URL once and then kept in component state,
  // so picking a routine left the address bar at /routines — a reload lost
  // the selection, Back left the page instead of closing the detail, and a
  // routine could not be linked to except by typing. Every routine link in
  // the app (routineHref, entityHref) points here with ?slug=; the dashboard's
  // "Up next" and the issue's routine chip still say ?routine=, which is read
  // as an alias and rewritten on the first pick.
  const [selectedRun, setSelectedRun] = useUrlSelection("run")
  const [selectedSlug, setSelectedSlug] = useUrlSelection("slug", ROUTINE_SLUG_OPTIONS)
  const [importDialogOpen, setImportDialogOpen] = useState(false)
  const [createDialogOpen, setCreateDialogOpen] = useState(false)
  const [linkedDraft, setLinkedDraft] = useUrlSelection("draft")
  const [linkedDraftId, setLinkedDraftId] = useUrlSelection("draft_id")
  const [linkedWorkspace, setLinkedWorkspace] = useUrlSelection("workspace")

  // Keyboard shortcuts (mirrors /issues): `/` focuses the routines
  // search input, `Esc` clears every filter, `c` opens the create
  // dialog. Skips when typing in inputs/textarea/contentEditable.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null
      const isInputContext =
        target &&
        (target.tagName === "INPUT" ||
          target.tagName === "TEXTAREA" ||
          target.isContentEditable)
      if (e.key === "/" && !isInputContext) {
        const el = document.querySelector<HTMLInputElement>("[data-routines-search] input")
        if (el) {
          e.preventDefault()
          el.focus()
          el.select()
        }
        return
      }
      if (e.key === "Escape" && !isInputContext) {
        if (
          search ||
          filters.status !== "all" ||
          filters.invocations !== "all" ||
          filters.authorAgentId ||
          filters.showEphemeral ||
          filters.showTestRoutines
        ) {
          e.preventDefault()
          setSearch("")
          setFilters({
            status: "all",
            invocations: "all",
            authorAgentId: null,
            showEphemeral: false,
          })
        }
        return
      }
      if (e.key === "c" && !isInputContext && !e.metaKey && !e.ctrlKey) {
        e.preventDefault()
        setCreateDialogOpen(true)
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [
    search,
    filters.status,
    filters.invocations,
    filters.authorAgentId,
    filters.showEphemeral,
    filters.showTestRoutines,
  ])

  const setBreadcrumbs = useAppStore((s) => s.setBreadcrumbs)
  // We ignore setBreadcrumbs for now; the layout's own toolbar surfaces
  // context. Future: wire selectedSlug → breadcrumb on detail open.
  void setBreadcrumbs

  const handleSelect = (slug: string) => {
    setSelectedRun(null, { replace: true })
    setSelectedSlug(selectedSlug === slug && !selectedRun ? null : slug)
    // Picking a routine on a phone means "show me that", and the
    // overlay covering it would be the opposite.
    if (isMobile) setLeftCollapsed(true)
  }

  // Selected routine — looked up from the loaded pipeline list so the
  // toolbar breadcrumb can show the human name without a second fetch.
  // The detail panel does its own fetch for the full DSL body.
  const selectedRoutine = selectedSlug ? pipelines.find((p) => p.slug === selectedSlug) : null

  const totalRuns = visiblePipelines.reduce((sum, p) => sum + (p.invocation_count ?? 0), 0)

  return (
    <div className="flex h-[calc(100vh-48px)] flex-col bg-background">
      {/* ---- Sub-bar: identity + actions ----
          Row 1 carries global context (Import / New routine); the
          page-specific 'Back to routines / <name>' breadcrumb lives one level
          down inside the content area (matches /issues) so it doesn't compete
          with the global affordances. */}
      <SubBar
        icon={Workflow}
        title="Routines"
        description={
          <>
            {visiblePipelines.length} {visiblePipelines.length === 1 ? "routine" : "routines"} ·{" "}
            {totalRuns} {totalRuns === 1 ? "run" : "runs"}
          </>
        }
        ariaLabel="Routines"
        actions={
          <>
            <SubBarSecondary
              icon={Upload}
              onClick={() => setImportDialogOpen(true)}
              title="Import a routine bundle from JSON"
            >
              Import
            </SubBarSecondary>
            <SubBarPrimary
              icon={Plus}
              onClick={() => setCreateDialogOpen(true)}
              title="Create a routine"
            >
              New routine
            </SubBarPrimary>
          </>
        }
      />

      {/* ---- Body: 3-column layout ---- */}
      <div className="relative flex flex-1 overflow-hidden">
        {/* Left filter panel — same chrome as the /issues sidebar
         * (bg-card, not bg-card/30) so the two surfaces feel like
         * pieces of one app rather than two near-misses. Width unified
         * to the shared sidebar-kit 280px (SIDEBAR_WIDTH). */}
        {/* Overlay on a phone, column everywhere else. The collapsed
            rail stays in flow at both sizes so the expand button never
            moves. */}
        {isMobile && !leftCollapsed && (
          <button
            type="button"
            aria-label="Close routine list"
            onClick={() => setLeftCollapsed(true)}
            className="absolute inset-0 z-20 bg-black/50"
          />
        )}
        <aside
          className={cn(
            "shrink-0 border-r border-white/[0.06] bg-card transition-all overflow-hidden",
            leftCollapsed ? "w-9" : "w-[280px]",
            isMobile && !leftCollapsed && "absolute inset-y-0 left-0 z-30 shadow-2xl",
          )}
        >
          {leftCollapsed ? (
            <div className="flex h-full flex-col items-center pt-1.5">
              <SidebarCollapseButton collapsed onToggle={() => setLeftCollapsed(false)} />
            </div>
          ) : (
            /* Explorer-style sidebar built on the shared sidebar-kit —
               SidebarToolbar (search + Filter + collapse), a collapsible
               STATUS bucket section, and the ROUTINES list. The collapse
               toggle lives inside the toolbar (next to search), not as a
               floating button. */
            <RoutinesExplorer
              routines={visiblePipelines}
              search={search}
              onSearchChange={setSearch}
              selectedSlug={selectedSlug}
              onSelectRoutine={handleSelect}
              filters={filters}
              onChange={setFilters}
              onToggleCollapse={() => setLeftCollapsed(true)}
            />
          )}
        </aside>

        {/* Overview, definition or historical run beside the explorer. */}
        <div className="min-w-0 flex-1 overflow-hidden bg-background relative">
          <AnimatePresence mode="wait">
            {selectedRun ? (
              <div key={selectedRun} className="absolute inset-0 overflow-auto">
                <RoutineRunDetail
                  key={selectedRun}
                  workspaceId={workspaceId}
                  runId={selectedRun}
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
                {/* Breadcrumb back-bar — matches the /issues pattern:
                    sits inside the content area, not in the global
                    toolbar. Keeps global affordances (List/Schedules/
                    Insights tabs, Import, New routine) where they
                    belong. */}
                <div className="flex shrink-0 items-center gap-2 border-b border-border bg-card/40 px-4 py-2">
                  <button
                    type="button"
                    onClick={() => setSelectedSlug(null)}
                    className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                  >
                    <ChevronLeft className="h-3.5 w-3.5" />
                    Back to routines
                  </button>
                  <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
                  <span
                    className="truncate text-xs font-medium text-foreground/85"
                    title={selectedRoutine?.name || selectedSlug}
                  >
                    {selectedRoutine?.name || selectedSlug}
                  </span>
                  {selectedRoutine?.slug && (
                    <span className="ml-1 truncate font-mono text-[11px] text-muted-foreground">
                      {selectedRoutine.slug}
                    </span>
                  )}
                </div>
                <div className="flex-1 overflow-hidden">
                  <RoutinesDetailPanel
                    workspaceId={workspaceId}
                    slug={selectedSlug}
                    onClose={() => setSelectedSlug(null)}
                    onChanged={refresh}
                    onRunStarted={setSelectedRun}
                  />
                </div>
              </motion.div>
            ) : (
              <motion.div
                key="overview"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="absolute inset-0 overflow-hidden"
              >
                <RoutinesWorkspace
                  search={search}
                  filters={filters}
                  workspaceId={workspaceId}
                  routines={visiblePipelines}
                  loading={loading}
                  error={error}
                  onSelect={handleSelect}
                  onFilter={(status) =>
                    setFilters((f) => ({ ...f, status: status as RoutineFilters["status"] }))
                  }
                />
              </motion.div>
            )}
          </AnimatePresence>
        </div>
      </div>

      {/* Import dialog */}
      {importDialogOpen && (
        <ImportRoutineDialog
          workspaceId={workspaceId}
          onClose={() => setImportDialogOpen(false)}
          onImported={() => {
            refresh()
            setImportDialogOpen(false)
          }}
        />
      )}

      {/* Create dialog — Test & Save flow with starter templates */}
      <RoutineCreateDialog
        workspaceId={workspaceId}
        open={createDialogOpen || !!linkedDraft}
        savedDraftLink={
          linkedDraft
            ? {
                slug: linkedDraft,
                id: linkedDraftId || undefined,
                workspaceId: linkedWorkspace || undefined,
              }
            : undefined
        }
        onClose={() => {
          setCreateDialogOpen(false)
          void setLinkedDraft(null, { replace: true })
          void setLinkedDraftId(null, { replace: true })
          void setLinkedWorkspace(null, { replace: true })
        }}
        onCreated={(slug) => {
          setSelectedRun(null, { replace: true })
          refresh()
          setSelectedSlug(slug)
        }}
      />
    </div>
  )
}

// Inline import dialog. Plain JSON paste flow — agents and the CLI use
// the same /pipelines/import endpoint. Drag-and-drop and URL import
// are follow-ups; paste covers the demo case.
//
// One question, one paste: `sm` on the shared create shell. Exported so its
// own test can drive it without standing the whole page up.
export function ImportRoutineDialog({
  workspaceId,
  onClose,
  onImported,
}: {
  workspaceId: string
  onClose: () => void
  onImported: () => void
}) {
  const [json, setJson] = useState("")
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const bundleFieldId = useId()

  const submit = async () => {
    setErr(null)
    setBusy(true)
    try {
      const parsed = JSON.parse(json)
      const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/import`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(parsed),
      })
      if (!res.ok) {
        const t = await res.text().catch(() => "")
        throw new Error(`${res.status}: ${t || res.statusText}`)
      }
      onImported()
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <CreateSurface
      open
      onOpenChange={(next) => {
        if (!next) onClose()
      }}
      size="sm"
      dirty={json.trim() !== ""}
      discardLabel="this bundle"
      onSubmit={() => {
        if (!busy && json.trim()) void submit()
      }}
    >
      <CreateSurfaceHeader
        icon={Upload}
        accent="purple"
        context="Routines"
        title="Import a bundle"
        description="Exported from another workspace, or shared by an agent. Authorship metadata is preserved."
        onClose={onClose}
      />

      <CreateSurfaceBody className="flex flex-col gap-3">
        <CreateSurfaceField
          label="Bundle JSON"
          // What the endpoint actually does on a collision: 409, and the
          // routine already here is left exactly as it was. The old wording
          // ("…or the existing routine is replaced") described a replace this
          // import has never performed.
          hint="A slug already in use here is refused — nothing already saved is overwritten."
        >
          <textarea
            id={bundleFieldId}
            value={json}
            onChange={(e) => setJson(e.target.value)}
            placeholder='{"slug":"…","definition":{…},"versions":[…]}'
            className="h-64 w-full resize-none rounded-md border border-hairline bg-background p-2 font-mono text-[11px] text-foreground outline-none transition-shadow focus:border-primary focus:ring-2 focus:ring-primary/20"
          />
        </CreateSurfaceField>
      </CreateSurfaceBody>

      {/* The parse failure and the server's refusal arrive at the same place,
          out of the scrollport, instead of under a 256px textarea. */}
      <CreateSurfaceRefusal
        message={err == null ? null : `Error: ${err}`}
        onDismiss={() => setErr(null)}
      />

      <CreateSurfaceFooter
        onCancel={onClose}
        primaryLabel={busy ? "Importing…" : "Import"}
        primaryIcon={Upload}
        onPrimary={submit}
        primaryDisabled={!json.trim()}
        busy={busy}
      />
    </CreateSurface>
  )
}
