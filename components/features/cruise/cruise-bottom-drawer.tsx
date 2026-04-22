"use client"

import { useState, useCallback } from "react"
import { motion } from "motion/react"
import {
  Activity, Square, FileJson, Layers, Download,
  ChevronUp, ChevronDown, Loader2,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { CruiseActivityFeed } from "@/components/features/cruise/cruise-activity-feed"

interface CrewExport {
  name: string
  slug: string
  color: string | null
  icon: string | null
}

interface AgentExport {
  id: string
  name: string
  slug: string
  status: string
  agent_role: string
  crew_id: string | null
  llm_provider: string
  llm_model: string
  avatar_seed?: string | null
  avatar_style?: string | null
}

interface CruiseBottomDrawerProps {
  crews: CrewExport[]
  agents: AgentExport[]
  isMobile: boolean
  /**
   * Called by child actions (bulk stop, etc.) after a successful mutation
   * so the parent can re-fetch its agent list. Without this, runningAgents
   * stays stale and duplicate POSTs become easy.
   */
  onAgentsChanged?: () => void
}

export function CruiseBottomDrawer({ crews, agents, isMobile, onAgentsChanged }: CruiseBottomDrawerProps) {
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [drawerTab, setDrawerTab] = useState<"activity" | "bulk" | "export">("activity")

  return (
    <motion.div
      className={cn("border-t border-white/[0.1] bg-card flex flex-col overflow-hidden", isMobile ? "col-span-1" : "col-span-3")}
      animate={{ height: drawerOpen ? 220 : 32 }}
      transition={{ duration: 0.2, ease: "easeInOut" }}
    >
      {/* Drawer tab bar */}
      <div
        className="flex items-center gap-0 px-2 shrink-0 h-8 cursor-pointer select-none"
        onClick={() => { if (!drawerOpen) setDrawerOpen(true) }}
      >
        {([
          { id: "activity" as const, label: "Activity", icon: Activity },
          { id: "bulk" as const, label: "Bulk Actions", icon: Layers },
          { id: "export" as const, label: "Export", icon: Download },
        ]).map(({ id, label, icon: Icon }) => {
          const isActive = drawerOpen && drawerTab === id
          return (
            <button
              key={id}
              type="button"
              aria-label={`Open ${label} tab`}
              aria-pressed={isActive}
              className={cn(
                "flex items-center gap-1.5 px-3 py-1 text-[11px] font-medium rounded-t transition-colors",
                isActive
                  ? "text-foreground bg-accent/50"
                  : "text-muted-foreground hover:text-foreground/70",
              )}
              onClick={(e) => {
                e.stopPropagation()
                setDrawerTab(id)
                setDrawerOpen(true)
              }}
            >
              <Icon className="h-3 w-3" />
              {!isMobile && label}
            </button>
          )
        })}

        <div className="ml-auto">
          <Button
            variant="ghost"
            size="icon-xs"
            className="text-muted-foreground/70 hover:text-foreground/70"
            aria-label={drawerOpen ? "Collapse drawer" : "Expand drawer"}
            aria-expanded={drawerOpen}
            onClick={(e) => { e.stopPropagation(); setDrawerOpen(!drawerOpen) }}
          >
            {drawerOpen ? <ChevronDown className="h-3 w-3" /> : <ChevronUp className="h-3 w-3" />}
          </Button>
        </div>
      </div>

      {/* Drawer content */}
      {drawerOpen && (
        <div className="flex-1 min-h-0 border-t border-border overflow-auto">
          {drawerTab === "activity" && (
            <CruiseActivityFeed agents={agents} />
          )}
          {drawerTab === "bulk" && (
            <CruiseBulkActions agents={agents} onSuccessRefresh={onAgentsChanged} />
          )}
          {drawerTab === "export" && (
            <div className="p-4 space-y-3">
              <p className="text-[12px] text-muted-foreground mb-3">Export your workspace configuration.</p>
              <div className="flex items-center gap-2">
                <Button variant="outline" size="sm" className="h-7 text-[11px] gap-1.5" onClick={() => {
                  const data = { crews: crews.map((c) => ({ name: c.name, slug: c.slug, color: c.color, icon: c.icon })), agents: agents.map((a) => ({ name: a.name, slug: a.slug, role: a.agent_role, crew_id: a.crew_id, llm_provider: a.llm_provider, llm_model: a.llm_model })) }
                  const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" })
                  const url = URL.createObjectURL(blob)
                  // Canonical cross-browser download pattern: append anchor
                  // to the DOM before click(), remove after, and defer
                  // revokeObjectURL so Safari/Firefox don't race the
                  // download initiation against URL teardown.
                  const a = document.createElement("a")
                  a.href = url
                  a.download = "cruise-export.json"
                  document.body.appendChild(a)
                  a.click()
                  document.body.removeChild(a)
                  setTimeout(() => URL.revokeObjectURL(url), 0)
                }}>
                  <FileJson className="h-3 w-3" /> Export JSON
                </Button>
              </div>
              <p className="text-[10px] text-muted-foreground/40">Exports crews and agents configuration (no credentials)</p>
            </div>
          )}
        </div>
      )}
    </motion.div>
  )
}

// ── Bulk Actions ──

function CruiseBulkActions({
  agents,
  onSuccessRefresh,
}: {
  agents: AgentExport[]
  onSuccessRefresh?: () => void
}) {
  const [running, setRunning] = useState<boolean>(false)
  const [result, setResult] = useState<string | null>(null)

  // "Start All Idle" was removed: Crewship's backend has no
  // /api/v1/agents/{id}/start route (only /stop — see
  // internal/api/router.go), so the Start button was guaranteed to 404.
  // Agents transition to RUNNING implicitly when a chat is sent to them,
  // not via an explicit start action — there's nothing to wire up here.
  // Until a backend start handler exists this drawer only exposes Stop.
  const runningAgents = agents.filter((a) => a.status === "RUNNING")

  const bulkStop = useCallback(async (targets: AgentExport[]) => {
    setRunning(true)
    setResult(null)
    let ok = 0
    let fail = 0
    // Per-request timeout — without this a single hung /stop would freeze
    // the whole batch loop and leave the button stuck in the loading state
    // forever.
    const REQUEST_TIMEOUT_MS = 30_000
    for (const agent of targets) {
      const controller = new AbortController()
      const timeoutId = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS)
      try {
        const resp = await fetch(`/api/v1/agents/${agent.id}/stop`, {
          method: "POST",
          signal: controller.signal,
        })
        if (resp.ok) ok++
        else fail++
      } catch {
        fail++
      } finally {
        clearTimeout(timeoutId)
      }
    }
    setResult(`${ok} succeeded, ${fail} failed`)
    setRunning(false)
    // Re-fetch parent state so runningAgents filter reflects post-action
    // reality and users cannot accidentally fire the same bulk op twice.
    if (ok > 0) {
      onSuccessRefresh?.()
    }
  }, [onSuccessRefresh])

  return (
    <div className="p-4 space-y-3">
      <p className="text-[12px] text-muted-foreground mb-3">Apply bulk operations to all agents by status.</p>
      <div className="flex items-center gap-2 flex-wrap">
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-7 text-[11px] gap-1.5"
          disabled={runningAgents.length === 0 || running}
          onClick={() => bulkStop(runningAgents)}
        >
          {running ? <Loader2 className="h-3 w-3 animate-spin" /> : <Square className="h-3 w-3" />}
          Stop All Running ({runningAgents.length})
        </Button>
      </div>
      {result && (
        <p
          role="status"
          aria-live="polite"
          aria-atomic="true"
          className="text-[10px] text-muted-foreground"
        >
          {result}
        </p>
      )}
    </div>
  )
}
