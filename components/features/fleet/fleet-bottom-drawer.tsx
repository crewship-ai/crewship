"use client"

import { useState } from "react"
import { motion } from "motion/react"
import {
  Activity, Play, Square, FileJson, Layers, Download,
  ChevronUp, ChevronDown, CheckSquare,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { FleetActivityFeed } from "@/components/features/fleet/fleet-activity-feed"

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

interface FleetBottomDrawerProps {
  crews: CrewExport[]
  agents: AgentExport[]
  isMobile: boolean
}

export function FleetBottomDrawer({ crews, agents, isMobile }: FleetBottomDrawerProps) {
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
        ]).map(({ id, label, icon: Icon }) => (
          <button
            key={id}
            className={cn(
              "flex items-center gap-1.5 px-3 py-1 text-[11px] font-medium rounded-t transition-colors",
              drawerOpen && drawerTab === id
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
        ))}

        <div className="ml-auto">
          <Button
            variant="ghost"
            size="icon-xs"
            className="text-muted-foreground/70 hover:text-foreground/70"
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
            <FleetActivityFeed agents={agents} />
          )}
          {drawerTab === "bulk" && (
            <div className="p-4 space-y-3">
              <p className="text-[12px] text-muted-foreground mb-3">Select agents from the explorer, then apply bulk operations.</p>
              <div className="flex items-center gap-2 flex-wrap">
                <Button variant="outline" size="sm" className="h-7 text-[11px] gap-1.5" disabled>
                  <Play className="h-3 w-3" /> Start All Idle
                </Button>
                <Button variant="outline" size="sm" className="h-7 text-[11px] gap-1.5" disabled>
                  <Square className="h-3 w-3" /> Stop All Running
                </Button>
                <Button variant="outline" size="sm" className="h-7 text-[11px] gap-1.5" disabled>
                  <CheckSquare className="h-3 w-3" /> Assign Crew
                </Button>
              </div>
              <p className="text-[10px] text-muted-foreground/40">Multi-select coming in Phase 2</p>
            </div>
          )}
          {drawerTab === "export" && (
            <div className="p-4 space-y-3">
              <p className="text-[12px] text-muted-foreground mb-3">Export your workspace configuration.</p>
              <div className="flex items-center gap-2">
                <Button variant="outline" size="sm" className="h-7 text-[11px] gap-1.5" onClick={() => {
                  const data = { crews: crews.map((c) => ({ name: c.name, slug: c.slug, color: c.color, icon: c.icon })), agents: agents.map((a) => ({ name: a.name, slug: a.slug, role: a.agent_role, crew_id: a.crew_id, llm_provider: a.llm_provider, llm_model: a.llm_model })) }
                  const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" })
                  const url = URL.createObjectURL(blob)
                  const a = document.createElement("a"); a.href = url; a.download = "fleet-export.json"; a.click()
                  URL.revokeObjectURL(url)
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
