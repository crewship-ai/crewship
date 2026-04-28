"use client"

import { useState } from "react"
import Link from "next/link"
import { Plus } from "lucide-react"
import { cn } from "@/lib/utils"
import { CreateCrewDialog } from "./create-crew-dialog"
import { CreateAgentDialog } from "./create-agent-dialog"

export interface CrewsSubbarProps {
  workspaceId: string
  crewSlug: string | null
  agentSlug?: string | null
  crewName: string | null
  agentName: string | null
  onCrewCreated: () => void
  onAgentCreated: (slug: string) => void
  /** Optional: shown only on mobile to open the explorer drawer. */
  onOpenExplorer?: () => void
  /** Crews list for the agent-creation form. */
  crews: { id: string; name: string; slug: string }[]
}

/**
 * Page-level chrome strip: breadcrumb on the left, page-specific create CTAs
 * on the right. Status/role filtering lives in the explorer (left panel) —
 * search box + colored status dots + role badges make a separate filter
 * dropdown redundant.
 */
export function CrewsSubbar({
  workspaceId,
  crewSlug,
  agentSlug: _agentSlug,
  crewName,
  agentName,
  onCrewCreated,
  onAgentCreated,
  onOpenExplorer,
  crews,
}: CrewsSubbarProps) {
  const [createCrewOpen, setCreateCrewOpen] = useState(false)
  const [createAgentOpen, setCreateAgentOpen] = useState(false)
  const [createAgentDefaultCrew, setCreateAgentDefaultCrew] = useState<string | null>(null)

  return (
    <>
      <div className="h-10 shrink-0 border-b border-white/8 bg-card flex items-center px-3 gap-2 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
        {onOpenExplorer && (
          <button
            type="button"
            className="md:hidden p-1 rounded hover:bg-white/5 text-foreground/80"
            onClick={onOpenExplorer}
            aria-label="Open explorer"
          >
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <line x1="3" y1="12" x2="21" y2="12" />
              <line x1="3" y1="6" x2="21" y2="6" />
              <line x1="3" y1="18" x2="21" y2="18" />
            </svg>
          </button>
        )}

        {/* Breadcrumb */}
        <nav className="flex items-center gap-1.5 text-xs min-w-0">
          <Link href="/crews" className="text-muted-foreground hover:text-foreground/80 shrink-0">
            Crews
          </Link>
          {(crewName || agentName) && <span className="text-muted-foreground/50 shrink-0">/</span>}
          {crewName && (
            <span className={cn("truncate", agentName ? "text-foreground/70" : "text-foreground/90 font-medium")}>
              {crewName}
            </span>
          )}
          {agentName && <span className="text-muted-foreground/50 shrink-0">/</span>}
          {agentName && <span className="text-foreground font-medium truncate">{agentName}</span>}
        </nav>

        {/* Create CTAs */}
        <div className="ml-auto flex items-center gap-1.5 shrink-0">
          <button
            type="button"
            onClick={() => setCreateCrewOpen(true)}
            className="text-xs px-2.5 py-1 rounded border border-white/10 hover:bg-white/5 text-foreground flex items-center gap-1.5"
          >
            <Plus className="h-3 w-3" />
            Crew
          </button>
          <button
            type="button"
            data-crews-add-agent
            onClick={() => {
              setCreateAgentDefaultCrew(crewSlug)
              setCreateAgentOpen(true)
            }}
            className="text-xs px-2.5 py-1 rounded bg-blue-500 hover:bg-blue-400 text-white flex items-center gap-1.5 transition-colors"
            title={crewSlug ? `New agent in ${crewName ?? crewSlug}` : "New agent (pick crew in dialog)"}
          >
            <Plus className="h-3 w-3" />
            Agent
          </button>
        </div>
      </div>

      <CreateCrewDialog
        workspaceId={workspaceId}
        open={createCrewOpen}
        onOpenChange={setCreateCrewOpen}
        onCreated={onCrewCreated}
      />
      <CreateAgentDialog
        workspaceId={workspaceId}
        open={createAgentOpen}
        onOpenChange={setCreateAgentOpen}
        defaultCrewSlug={createAgentDefaultCrew}
        crews={crews}
        onCreated={onAgentCreated}
      />
    </>
  )
}
