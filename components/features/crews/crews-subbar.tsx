"use client"

import { useState } from "react"
import { Menu, Plus, Users } from "lucide-react"
import { SubBar, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { CreateCrewDialog } from "./create-crew-dialog"
import { CreateAgentDialog } from "./create-agent"

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
 * Page-level chrome strip: identity on the left, page-specific create CTAs
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

  // Live description: breadcrumb path when something is selected, otherwise a count.
  const description =
    crewName || agentName
      ? `${crewName ?? ""}${agentName ? ` / ${agentName}` : ""}`
      : `${crews.length} crews`

  return (
    <>
      <SubBar
        icon={Users}
        title="Crews & Agents"
        description={description}
        ariaLabel="Crews & Agents"
        leading={
          onOpenExplorer && (
            <button
              type="button"
              className="md:hidden p-1 rounded hover:bg-white/5 text-foreground/80"
              onClick={onOpenExplorer}
              aria-label="Open explorer"
            >
              <Menu className="h-3.5 w-3.5" />
            </button>
          )
        }
        actions={
          <>
            <SubBarSecondary icon={Plus} onClick={() => setCreateCrewOpen(true)}>
              Crew
            </SubBarSecondary>
            <SubBarPrimary
              icon={Plus}
              data-crews-add-agent
              onClick={() => {
                setCreateAgentDefaultCrew(crewSlug)
                setCreateAgentOpen(true)
              }}
              title={crewSlug ? `New agent in ${crewName ?? crewSlug}` : "New agent (pick crew in dialog)"}
            >
              Agent
            </SubBarPrimary>
          </>
        }
      />

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
