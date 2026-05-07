"use client"

import { useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  ScrollText, Workflow, Clock, Activity,
  Plus, Upload, Settings, PanelLeftClose, PanelLeftOpen,
  Search, X,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"
import { useAppStore } from "@/lib/store"
import { usePipelines } from "@/hooks/use-pipelines"
import { RoutinesListView } from "./routines-list-view"
import { RoutinesGraphView } from "./routines-graph-view"
import { RoutinesTimelineView } from "./routines-timeline-view"
import { RoutinesActivityView } from "./routines-activity-view"
import { RoutinesDetailPanel } from "./routines-detail-panel"
import { RoutinesFilterSidebar, type RoutineFilters } from "./routines-filter-sidebar"
import { RoutineCreateDialog } from "./routine-create-dialog"

// RoutinesLayout — full /routines page. Shape mirrors orchestration:
// top toolbar with tabs + actions, optional left filter panel, main
// area swapped by tab, right detail panel when a routine is selected.
//
// Why a separate page instead of just orchestration's Routines tab:
// schedules, webhooks, waitpoints fire autonomously regardless of
// open mission/issue. Routines deserve a workspace-scoped surface
// like /skills or /credentials. The orchestration tab covers
// in-context invocation; this page is the asset-management home.

const ROUTINES_TABS = [
  { id: "routines" as const, label: "Routines", icon: ScrollText },
  { id: "graph" as const, label: "Graph", icon: Workflow },
  { id: "timeline" as const, label: "Timeline", icon: Clock },
  { id: "activity" as const, label: "Activity", icon: Activity },
] as const

type RoutinesTab = (typeof ROUTINES_TABS)[number]["id"]

interface RoutinesLayoutProps {
  workspaceId: string
}

export function RoutinesLayout({ workspaceId }: RoutinesLayoutProps) {
  const { pipelines, loading, error, refresh } = usePipelines(workspaceId)
  const [activeTab, setActiveTab] = useState<RoutinesTab>("routines")
  const [leftCollapsed, setLeftCollapsed] = useState(false)
  const [search, setSearch] = useState("")
  const [filters, setFilters] = useState<RoutineFilters>({
    status: "all",
    invocations: "all",
    authoredVia: "all",
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
      const haystack = `${p.slug} ${p.name} ${p.description ?? ""}`.toLowerCase()
      if (!haystack.includes(q)) return false
    }
    if (filters.status !== "all") {
      if (filters.status === "never" && p.invocation_count !== 0) return false
      if (filters.status !== "never" && p.last_invocation_status?.toLowerCase() !== filters.status)
        return false
    }
    if (filters.invocations === "popular" && p.invocation_count < 10) return false
    if (filters.invocations === "fresh" && p.invocation_count > 0) return false
    if (filters.authoredVia !== "all" && p.authored_via !== filters.authoredVia) return false
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

        {/* Search (when on Routines tab) */}
        {activeTab === "routines" && (
          <div className="relative w-56 mr-2">
            <Search className="absolute left-2 top-1/2 h-3 w-3 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search routines…"
              className="h-7 pl-7 pr-6 text-xs"
            />
            {search && (
              <button
                onClick={() => setSearch("")}
                className="absolute right-1.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
              >
                <X className="h-3 w-3" />
              </button>
            )}
          </div>
        )}

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
        {/* Left filter panel */}
        <aside
          className={cn(
            "shrink-0 border-r border-white/[0.06] bg-card/30 transition-all overflow-hidden",
            leftCollapsed ? "w-9" : "w-60",
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
            <div className="flex h-full flex-col">
              <div className="flex items-center justify-between border-b border-white/[0.06] px-3 h-8 shrink-0">
                <span className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
                  Filters
                </span>
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-6 w-6 p-0"
                  onClick={() => setLeftCollapsed(true)}
                  title="Collapse"
                >
                  <PanelLeftClose className="h-3 w-3" />
                </Button>
              </div>
              <RoutinesFilterSidebar
                filters={filters}
                onChange={setFilters}
                routines={pipelines}
                totalRoutines={pipelines.length}
                filteredCount={filteredRoutines.length}
              />
            </div>
          )}
        </aside>

        {/* Main content area */}
        <div className="flex-1 overflow-hidden bg-background relative">
          <AnimatePresence mode="wait">
            {activeTab === "routines" && (
              <motion.div
                key="routines"
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
            {activeTab === "graph" && (
              <motion.div
                key="graph"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="absolute inset-0"
              >
                <RoutinesGraphView workspaceId={workspaceId} routines={pipelines} onSelect={handleSelect} />
              </motion.div>
            )}
            {activeTab === "timeline" && (
              <motion.div
                key="timeline"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="absolute inset-0 overflow-auto"
              >
                <RoutinesTimelineView workspaceId={workspaceId} routines={pipelines} onSelect={handleSelect} />
              </motion.div>
            )}
            {activeTab === "activity" && (
              <motion.div
                key="activity"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="absolute inset-0 overflow-auto"
              >
                <RoutinesActivityView workspaceId={workspaceId} />
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
