"use client"

import { useEffect, useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  ScrollText, Calendar, BarChart3, Workflow,
  Plus, Upload,
  X, ChevronLeft, ChevronRight,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { SubBar, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { SidebarCollapseButton } from "@/components/layout/sidebar-kit"
import { cn } from "@/lib/utils"
import { useAppStore } from "@/lib/store"
import { apiFetch } from "@/lib/api-fetch"
import { usePipelines } from "@/hooks/use-pipelines"
import { RoutinesListView } from "./routines-list-view"
import { RoutinesSchedulesView } from "./routines-schedules-view"
import { RoutinesInsightsView } from "./routines-insights-view"
import { RoutinesDetailPanel } from "./routines-detail-panel"
import { type RoutineFilters } from "./routines-filter-sidebar"
import { RoutinesExplorer } from "./routines-explorer"
import { RoutineCreateDialog } from "./routine-create-dialog"
import { BottomPanel } from "@/components/features/crews/bottom-panel"
import type { BottomPanelContext } from "@/components/features/crews/bottom-panel/types"

// RoutinesLayout — full /routines page. The IA refactor cut the
// previous 4 tabs (Routines / Graph / Timeline / Activity) down to 3:
//   - List      — the catalog, primary entry point.
//   - Schedules — workspace-wide cron triggers across all routines.
//   - Insights  — health snapshot (top usage, recent failures).
//
// Graph + Timeline + Activity moved to /activity, which is now the
// single live observability surface for the whole workspace. This
// page stays focused on the asset-management story (catalog +
// triggers + health), separating workflow definitions from runs the
// way most operator-facing workflow tools do.

const ROUTINES_TABS = [
  { id: "list" as const, label: "List", icon: ScrollText },
  { id: "schedules" as const, label: "Schedules", icon: Calendar },
  { id: "insights" as const, label: "Insights", icon: BarChart3 },
] as const

type RoutinesTab = (typeof ROUTINES_TABS)[number]["id"]

interface RoutinesLayoutProps {
  workspaceId: string
}

export function RoutinesLayout({ workspaceId }: RoutinesLayoutProps) {
  const { pipelines, loading, error, refresh } = usePipelines(workspaceId)
  const [activeTab, setActiveTab] = useState<RoutinesTab>("list")
  const [leftCollapsed, setLeftCollapsed] = useState(false)
  const [search, setSearch] = useState("")
  const [filters, setFilters] = useState<RoutineFilters>({
    status: "all",
    invocations: "all",
    authorAgentId: null,
    showEphemeral: false,
  })
  const [selectedSlug, setSelectedSlug] = useState<string | null>(null)
  const [importDialogOpen, setImportDialogOpen] = useState(false)
  const [createDialogOpen, setCreateDialogOpen] = useState(false)

  // Keyboard shortcuts (mirrors /issues): `/` focuses the routines
  // search input, `Esc` clears every filter, `c` opens the create
  // dialog. Skips when typing in inputs/textarea/contentEditable.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null
      const isInputContext = target && (
        target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable
      )
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
        if (search || filters.status !== "all" || filters.invocations !== "all" || filters.authorAgentId || filters.showEphemeral) {
          e.preventDefault()
          setSearch("")
          setFilters({ status: "all", invocations: "all", authorAgentId: null, showEphemeral: false })
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
  }, [search, filters.status, filters.invocations, filters.authorAgentId, filters.showEphemeral])

  const setBreadcrumbs = useAppStore((s) => s.setBreadcrumbs)
  // We ignore setBreadcrumbs for now; the layout's own toolbar surfaces
  // context. Future: wire selectedSlug → breadcrumb on detail open.
  void setBreadcrumbs

  const handleSelect = (slug: string) => {
    setSelectedSlug((prev) => (prev === slug ? null : slug))
  }

  const filteredRoutines = pipelines.filter((p) => {
    if (search) {
      const q = search.toLowerCase()
      const haystack = `${p.slug} ${p.name} ${p.description ?? ""} ${p.author_agent_name ?? ""}`.toLowerCase()
      if (!haystack.includes(q)) return false
    }
    if (filters.status !== "all") {
      if (filters.status === "never" && p.invocation_count !== 0) return false
      if (filters.status !== "never" && p.last_invocation_status?.toLowerCase() !== filters.status)
        return false
    }
    if (filters.invocations === "popular" && p.invocation_count < 10) return false
    if (filters.invocations === "fresh" && p.invocation_count > 0) return false
    if (filters.authorAgentId !== null && p.author_agent_id !== filters.authorAgentId) return false
    if (!filters.showEphemeral && p.ephemeral) return false
    return true
  })

  // Selected routine — looked up from the loaded pipeline list so the
  // toolbar breadcrumb can show the human name without a second fetch.
  // The detail panel does its own fetch for the full DSL body.
  const selectedRoutine = selectedSlug
    ? pipelines.find((p) => p.slug === selectedSlug)
    : null

  // Context for the bottom dock — runs / logs / schedule / spec of the
  // routine in focus. MEMOIZED: this layout re-renders on every poll tick,
  // and a fresh context object each render makes the dock tabs (logs/yaml)
  // re-fetch + flash "Loading…" forever. Identity must only change when the
  // routine actually changes.
  const routineCtx: BottomPanelContext = useMemo(
    () => (selectedSlug
      ? { kind: "routine", slug: selectedSlug, pipelineId: selectedRoutine?.id ?? null, name: selectedRoutine?.name }
      : null),
    [selectedSlug, selectedRoutine?.id, selectedRoutine?.name],
  )

  // Live sub-bar description — derived from the loaded pipelines list.
  // `pipelines.length` = routines in the workspace; `totalRuns` sums each
  // routine's invocation_count (Pipeline.invocation_count from use-pipelines).
  const totalRuns = pipelines.reduce((sum, p) => sum + (p.invocation_count ?? 0), 0)

  return (
    <div className="flex h-[calc(100vh-48px)] flex-col bg-background">
      {/* ---- Sub-bar: identity + tabs + actions ----
          Row 1 carries global context (List/Schedules/Insights + Import/New
          routine); the page-specific 'Back to routines / <name>' breadcrumb
          lives one level down inside the content area (matches /issues) so it
          doesn't compete with the global affordances. */}
      <SubBar<RoutinesTab>
        icon={Workflow}
        title="Routines"
        description={
          <>
            {pipelines.length} {pipelines.length === 1 ? "routine" : "routines"} · {totalRuns}{" "}
            {totalRuns === 1 ? "run" : "runs"}
          </>
        }
        ariaLabel="Routines"
        tabs={ROUTINES_TABS.map((t) => ({ id: t.id, label: t.label, icon: t.icon }))}
        activeTab={activeTab}
        onTabChange={(id) => setActiveTab(id)}
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
              title="Create a new routine — DSL editor with starter templates + Test & Save"
            >
              New routine
            </SubBarPrimary>
          </>
        }
      />

      {/* ---- Body: 3-column layout ---- */}
      <div className="flex flex-1 overflow-hidden">
        {/* Left filter panel — same chrome as the /issues sidebar
          * (bg-card, not bg-card/30) so the two surfaces feel like
          * pieces of one app rather than two near-misses. Width unified
          * to the shared sidebar-kit 280px (SIDEBAR_WIDTH). */}
        <aside
          className={cn(
            "shrink-0 border-r border-white/[0.06] bg-card transition-all overflow-hidden",
            leftCollapsed ? "w-9" : "w-[280px]",
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
              routines={pipelines}
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

        {/* Main content area — full-width.
            With selection: breadcrumb back-bar + routine detail
            (Overview/Editor/Runs/Versions/Schedules/Webhooks/Wait
            tabs) edge-to-edge instead of cramming it into a 520px
            right panel. The Editor tab in particular benefits — DSL
            YAML wants width.
            Without selection: the existing List / Schedules /
            Insights tabs that the toolbar above switches between. */}
        <div className="flex-1 overflow-hidden bg-background relative">
          <AnimatePresence mode="wait">
            {selectedSlug ? (
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
                  <span className="truncate text-xs font-medium text-foreground/85" title={selectedRoutine?.name || selectedSlug}>
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
                  />
                </div>
              </motion.div>
            ) : activeTab === "list" ? (
              <motion.div
                key="list"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="absolute inset-0 overflow-hidden"
              >
                <RoutinesListView
                  routines={filteredRoutines}
                  loading={loading}
                  error={error}
                  selectedSlug={selectedSlug}
                  onSelect={handleSelect}
                  onRefresh={refresh}
                />
              </motion.div>
            ) : activeTab === "schedules" ? (
              <motion.div
                key="schedules"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="absolute inset-0"
              >
                <RoutinesSchedulesView
                  workspaceId={workspaceId}
                  routines={pipelines}
                  onSelect={handleSelect}
                />
              </motion.div>
            ) : (
              <motion.div
                key="insights"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="absolute inset-0"
              >
                <RoutinesInsightsView routines={pipelines} onSelect={handleSelect} />
              </motion.div>
            )}
          </AnimatePresence>
        </div>
      </div>

      {/* ---- Bottom dock — runs / logs / schedule / spec of the selected
           routine. Appears once a routine is selected, pairing the
           definition above with its run console below. ---- */}
      {routineCtx && (
        <BottomPanel
          workspaceId={workspaceId}
          context={routineCtx}
          tabs={["runs", "logs", "schedule", "yaml"]}
        />
      )}

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
        open={createDialogOpen}
        onClose={() => setCreateDialogOpen(false)}
        onCreated={(slug) => {
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
function ImportRoutineDialog({
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
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm p-4"
      onClick={onClose}
    >
      <div
        className="w-full max-w-2xl rounded-lg border border-white/10 bg-card shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-white/[0.06] px-4 py-3">
          <h3 className="text-sm font-medium">Import routine bundle</h3>
          <Button size="sm" variant="ghost" className="h-7 w-7 p-0" onClick={onClose}>
            <X className="h-3 w-3" />
          </Button>
        </div>
        <div className="space-y-3 p-4">
          <p className="text-xs text-muted-foreground">
            Paste a routine bundle JSON (exported from another workspace via Export bundle, or
            shared by an agent). Import preserves authorship metadata; the bundle's slug must be
            unique in this workspace or the existing routine is replaced.
          </p>
          <textarea
            value={json}
            onChange={(e) => setJson(e.target.value)}
            placeholder='{"slug":"…","definition":{…},"versions":[…]}'
            className="h-64 w-full resize-none rounded-md border border-white/10 bg-background p-2 font-mono text-[11px]"
          />
          {err && <div className="text-xs text-red-400">Error: {err}</div>}
          <div className="flex justify-end gap-2">
            <Button size="sm" variant="ghost" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button size="sm" onClick={submit} disabled={busy || !json.trim()}>
              {busy ? "Importing…" : "Import"}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
