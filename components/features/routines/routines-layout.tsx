"use client"

import { useEffect, useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { useRouter } from "next/navigation"
import {
  ScrollText, Calendar, BarChart3,
  Plus, Upload, Settings, PanelLeftClose, PanelLeftOpen,
  X, ChevronLeft, ChevronRight,
} from "lucide-react"
import { Button } from "@/components/ui/button"
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
import { TabBar } from "@/components/ui/tab-bar"
import type { Mission } from "@/lib/types/mission"
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
  // Missions (with tasks) fed to the sidebar's Inbox section. Same
  // endpoint + shape as orchestration-page-shell.tsx — fetched here so
  // the inbox is live on /routines without depending on a separate
  // shell. One-shot on mount; the inbox is intended as a peek + proklik
  // to /inbox, not a live triage surface, so polling isn't justified.
  const [missions, setMissions] = useState<Mission[]>([])
  const router = useRouter()

  useEffect(() => {
    let cancelled = false
    if (!workspaceId) return
    apiFetch(`/api/v1/missions?workspace_id=${workspaceId}&limit=50&include_tasks=true`)
      .then((res) => (res.ok ? res.json() : Promise.reject(new Error(`HTTP ${res.status}`))))
      .then((data: Mission[]) => { if (!cancelled) setMissions(Array.isArray(data) ? data : []) })
      .catch(() => { if (!cancelled) setMissions([]) })
    return () => { cancelled = true }
  }, [workspaceId])

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
        const el = document.querySelector<HTMLInputElement>("input[data-routines-search-input]")
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

  return (
    <div className="flex h-[calc(100vh-48px)] flex-col bg-background">
      {/* ---- Toolbar ---- */}
      {/* Global toolbar always shows TabBar + actions. Breadcrumb back-bar
          in detail mode is rendered separately inside the main content
          area (matches the /issues pattern — top bar stays for global
          context like List/Schedules/Insights + Import/New routine; the
          page-specific 'Back to routines / <name>' lives one level down
          so it doesn't compete with the global affordances). */}
      <div className="shrink-0 z-20 flex items-center h-9 bg-card border-b border-white/[0.08] px-2 sm:px-3 gap-1 overflow-x-auto [&::-webkit-scrollbar]:hidden">
        <TabBar
          value={activeTab}
          onValueChange={(v) => setActiveTab(v as RoutinesTab)}
          layoutId="routines-tabs-indicator"
          ariaLabel="Routines view"
          className="h-full border-b-0 shrink-0"
        >
          {ROUTINES_TABS.map(({ id, label, icon: Icon }) => (
            <TabBar.Item key={id} value={id} className="h-full whitespace-nowrap">
              <span className="inline-flex items-center gap-1.5">
                <Icon className="h-3 w-3 opacity-75" />
                {label}
              </span>
            </TabBar.Item>
          ))}
        </TabBar>

        <div className="flex-1" />

        {/* Search now lives in the left filter sidebar (mirroring the
          * /issues UnifiedExplorer); the toolbar stays focused on tabs
          * + actions. Removed the duplicate toolbar search to avoid
          * confusing two visible inputs. */}

        {/* Action buttons */}
        <Button
          size="sm"
          variant="ghost"
          className="h-7 gap-1.5 text-xs"
          onClick={() => setImportDialogOpen(true)}
          title="Import a routine bundle from JSON"
        >
          <Upload className="h-3 w-3" />
          Import
        </Button>
        <Button
          size="sm"
          variant="default"
          className="h-7 gap-1.5 text-xs"
          onClick={() => setCreateDialogOpen(true)}
          title="Create a new routine — DSL editor with starter templates + Test & Save"
        >
          <Plus className="h-3 w-3" />
          New routine
        </Button>
        <Button size="sm" variant="ghost" className="h-7 px-2" title="Routines settings">
          <Settings className="h-3 w-3" />
        </Button>
      </div>

      {/* ---- Body: 3-column layout ---- */}
      <div className="flex flex-1 overflow-hidden">
        {/* Left filter panel — same chrome as the /issues sidebar
          * (bg-card, not bg-card/30) so the two surfaces feel like
          * pieces of one app rather than two near-misses. The width
          * also matches the orchestration explorer (300px expanded). */}
        <aside
          className={cn(
            "shrink-0 border-r border-white/[0.06] bg-card transition-all overflow-hidden",
            leftCollapsed ? "w-9" : "w-[300px]",
          )}
        >
          {leftCollapsed ? (
            <div className="flex h-full flex-col items-center pt-2">
              <Button
                size="sm"
                variant="ghost"
                className="h-7 w-7 p-0"
                onClick={() => setLeftCollapsed(false)}
                title="Expand filters"
              >
                <PanelLeftOpen className="h-3 w-3" />
              </Button>
            </div>
          ) : (
            <div className="relative flex h-full flex-col">
              {/* New explorer-style sidebar — mirrors /issues UnifiedExplorer
                  chrome (search + Filter dropdown), with a STATUS bucket
                  section (like Projects), a ROUTINES list (like Issues),
                  and an INBOX section at the bottom with hover proklik
                  to /inbox. */}
              <RoutinesExplorer
                routines={pipelines}
                search={search}
                onSearchChange={setSearch}
                selectedSlug={selectedSlug}
                onSelectRoutine={handleSelect}
                filters={filters}
                onChange={setFilters}
                missions={missions}
                onTaskSelect={(_task, mission) => {
                  // Inbox click navigates to the related issue page so
                  // the user can resolve/approve where the full context
                  // lives. Falls back to /inbox if the mission has no
                  // identifier yet.
                  if (mission.identifier) {
                    router.push(`/issues/${encodeURIComponent(mission.identifier)}`)
                  } else {
                    router.push("/inbox")
                  }
                }}
              />
              <Button
                size="sm"
                variant="ghost"
                className="absolute right-1 top-1.5 h-6 w-6 p-0"
                onClick={() => setLeftCollapsed(true)}
                title="Collapse"
              >
                <PanelLeftClose className="h-3 w-3" />
              </Button>
            </div>
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
