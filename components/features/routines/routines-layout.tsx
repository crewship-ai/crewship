"use client"

import { useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  ScrollText, Calendar, BarChart3,
  Plus, Upload, Settings, PanelLeftClose, PanelLeftOpen,
  X,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { useAppStore } from "@/lib/store"
import { usePipelines } from "@/hooks/use-pipelines"
import { RoutinesListView } from "./routines-list-view"
import { RoutinesSchedulesView } from "./routines-schedules-view"
import { RoutinesInsightsView } from "./routines-insights-view"
import { RoutinesDetailPanel } from "./routines-detail-panel"
import { RoutinesFilterSidebar, type RoutineFilters } from "./routines-filter-sidebar"
import { RoutineCreateDialog } from "./routine-create-dialog"

// RoutinesLayout — full /routines page. The IA refactor cut the
// previous 4 tabs (Routines / Graph / Timeline / Activity) down to 3:
//   - List      — the catalog, primary entry point.
//   - Schedules — workspace-wide cron triggers across all routines.
//   - Insights  — health snapshot (top usage, recent failures).
//
// Graph + Timeline + Activity moved to /activity, which is now the
// single live observability surface for the whole workspace. This
// page stays focused on the asset-management story (catalog +
// triggers + health), matching how Trigger.dev/Inngest/Dagster
// separate definitions from runs.

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

  return (
    <div className="flex h-[calc(100vh-48px)] flex-col bg-background">
      {/* ---- Toolbar ---- */}
      <div className="shrink-0 z-20 flex items-center h-9 bg-card border-b border-white/[0.08] px-2 sm:px-3 gap-0 overflow-x-auto [&::-webkit-scrollbar]:hidden">
        {/* Tabs */}
        {ROUTINES_TABS.map(({ id, label, icon: Icon }) => (
          <button
            key={id}
            onClick={() => setActiveTab(id)}
            className={cn(
              "flex items-center gap-1.5 px-2.5 h-full text-xs font-medium border-b-2 transition-all duration-100 relative top-px whitespace-nowrap shrink-0",
              activeTab === id
                ? "border-blue-400 text-blue-400"
                : "border-transparent text-muted-foreground hover:text-foreground/80",
            )}
          >
            <Icon className="h-3 w-3 opacity-75" />
            {label}
          </button>
        ))}

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
              {/* The filter sidebar's "Showing X of Y" only matches
                * reality on the List tab — Schedules + Insights walk
                * the unfiltered pipelines list internally. To avoid
                * misleading counts (and to disable filter UX that has
                * no effect on those views), surface filteredCount
                * verbatim on List, and totalRoutines on the others
                * so the strip reads as "showing all". */}
              <RoutinesFilterSidebar
                filters={filters}
                onChange={setFilters}
                routines={pipelines}
                totalRoutines={pipelines.length}
                filteredCount={activeTab === "list" ? filteredRoutines.length : pipelines.length}
                search={search}
                onSearchChange={setSearch}
              />
              {/* Collapse handle floats top-right so the sidebar's own
                * search bar reaches edge-to-edge; matches the explorer
                * pattern in /issues. */}
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

        {/* Main content area */}
        <div className="flex-1 overflow-hidden bg-background relative">
          <AnimatePresence mode="wait">
            {activeTab === "list" && (
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
            )}
            {activeTab === "schedules" && (
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
            )}
            {activeTab === "insights" && (
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

        {/* Right detail panel */}
        {selectedSlug && (
          <aside className="w-[520px] shrink-0 border-l border-white/[0.06] bg-card/30 overflow-hidden">
            <RoutinesDetailPanel
              workspaceId={workspaceId}
              slug={selectedSlug}
              onClose={() => setSelectedSlug(null)}
              onChanged={refresh}
            />
          </aside>
        )}
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
      const res = await fetch(`/api/v1/workspaces/${workspaceId}/pipelines/import`, {
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
