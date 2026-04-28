"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { toast } from "sonner"
import { CrewsExplorer } from "@/components/features/crews/crews-explorer"
import { CrewsSubbar } from "@/components/features/crews/crews-subbar"
import { AgentCanvas } from "@/components/features/crews/agent-canvas"
import { CrewCanvas } from "@/components/features/crews/crew-canvas"
import { EmptyRoster } from "@/components/features/crews/empty-roster"
import { BottomPanel, type BottomTab } from "@/components/features/crews/bottom-panel"
import { useIsMobile } from "@/hooks/use-mobile"
import { useCrewsSelection } from "@/hooks/use-crews-selection"

interface CrewData {
  id: string
  name: string
  slug: string
  description: string | null
  color: string | null
  icon: string | null
  created_at: string
  _count: { agents: number; members: number }
}

interface AgentData {
  id: string
  name: string
  slug: string
  status: string
  description: string | null
  role_title: string | null
  agent_role: string
  llm_provider: string
  llm_model: string
  cli_adapter: string
  crew_id: string | null
  avatar_seed?: string | null
  avatar_style?: string | null
  crew?: { name: string; slug: string; color: string | null; avatar_style?: string | null } | null
  _count?: { skills: number; credentials: number; chats: number }
  last_active_at?: string | null
}

interface MissionData {
  id: string
  title: string
  status: string
  crew_id: string
  tasks?: { id: string; status: string }[]
  created_at: string
}

export interface CrewsLayoutProps {
  crews: CrewData[]
  agents: AgentData[]
  missions: MissionData[]
  workspaceId: string
  /** Explicit load signal from the parent fetch. The stale-slug watcher
   *  needs this because `agents.length > 0` is not a reliable "loaded"
   *  flag — a legitimately empty workspace would otherwise never get
   *  its stale `?agent=` / `?crew=` param cleared. */
  loaded?: boolean
  onRefresh: () => void
}

/**
 * Selection-driven canvas layout.
 *
 *   ┌─────────────────────────────────────────────┐
 *   │  CrewsSubbar (breadcrumb · filters · CTAs)  │
 *   ├──────────────┬──────────────────────────────┤
 *   │              │                              │
 *   │  Explorer    │  AgentCanvas /               │
 *   │  (filter +   │  CrewCanvas /                │
 *   │  hierarchy)  │  EmptyRoster                 │
 *   │              │                              │
 *   ├──────────────┴──────────────────────────────┤
 *   │  BottomPanel (Messages/Exec/YAML/Docker/…)  │
 *   └─────────────────────────────────────────────┘
 *
 * Replaces the older drawer-based design (chat/logs/settings as Sheets,
 * top tabs Overview/Activity/Health, dual headers). All settings now
 * live inline in the canvas; chat moves to a dedicated /chat/[slug]
 * full-page route.
 */
export function CrewsLayout({
  crews,
  agents,
  missions,
  workspaceId,
  loaded = false,
  onRefresh,
}: CrewsLayoutProps) {
  const isMobile = useIsMobile()
  const {
    selectedAgentSlug,
    selectedCrewSlug,
    update,
    selectAgent,
    selectCrew,
  } = useCrewsSelection()

  const [explorerCollapsed, setExplorerCollapsed] = useState(false)
  const [explorerOverlayOpen, setExplorerOverlayOpen] = useState(false)
  const [bottomTab, setBottomTab] = useState<BottomTab>("messages")
  const [bottomOpen, setBottomOpen] = useState(false)

  useEffect(() => {
    if (isMobile) setExplorerCollapsed(true)
  }, [isMobile])

  // Stale-slug watcher: clear ?agent= / ?crew= if they don't exist.
  const staleSlugNotified = useRef<string | null>(null)
  useEffect(() => {
    if (!loaded) return
    if (selectedAgentSlug && !agents.find((a) => a.slug === selectedAgentSlug)) {
      if (staleSlugNotified.current !== selectedAgentSlug) {
        staleSlugNotified.current = selectedAgentSlug
        toast.warning(`Agent "${selectedAgentSlug}" not found`)
        update({ agent: null })
      }
    } else if (selectedCrewSlug && !crews.find((c) => c.slug === selectedCrewSlug)) {
      if (staleSlugNotified.current !== selectedCrewSlug) {
        staleSlugNotified.current = selectedCrewSlug
        toast.warning(`Crew "${selectedCrewSlug}" not found`)
        update({ crew: null })
      }
    } else {
      staleSlugNotified.current = null
    }
  }, [loaded, selectedAgentSlug, selectedCrewSlug, agents, crews, update])

  const selectedCrew = useMemo(
    () => (selectedCrewSlug ? crews.find((c) => c.slug === selectedCrewSlug) || null : null),
    [crews, selectedCrewSlug],
  )
  const selectedAgent = useMemo(
    () => (selectedAgentSlug ? agents.find((a) => a.slug === selectedAgentSlug) || null : null),
    [agents, selectedAgentSlug],
  )

  const crewAgents = useMemo(
    () => (selectedCrew ? agents.filter((a) => a.crew_id === selectedCrew.id) : []),
    [agents, selectedCrew],
  )

  const handleCrewSelect = useCallback((crewId: string) => {
    const crew = crews.find((c) => c.id === crewId)
    if (!crew) return
    if (selectedCrewSlug === crew.slug && !selectedAgentSlug) {
      selectCrew(null)
      return
    }
    selectCrew(crew.slug)
  }, [crews, selectedCrewSlug, selectedAgentSlug, selectCrew])

  const handleAgentSelect = useCallback((agentId: string) => {
    const agent = agents.find((a) => a.id === agentId)
    if (!agent) return
    if (selectedAgentSlug === agent.slug) {
      selectAgent(null)
      return
    }
    const parentCrew = agent.crew_id ? crews.find((c) => c.id === agent.crew_id) : null
    update({ agent: agent.slug, crew: parentCrew?.slug ?? null })
  }, [agents, crews, selectedAgentSlug, selectAgent, update])

  const handleAgentSelectBySlug = useCallback((slug: string) => {
    const agent = agents.find((a) => a.slug === slug)
    if (agent) handleAgentSelect(agent.id)
  }, [agents, handleAgentSelect])

  const handleOpenFiles = useCallback(() => {
    setBottomTab("files")
    setBottomOpen(true)
  }, [])

  // Bottom panel context: selected agent > selected crew > none.
  const bottomContext = useMemo(() => {
    if (selectedAgent) {
      return { kind: "agent" as const, agentId: selectedAgent.id, agentSlug: selectedAgent.slug, agentName: selectedAgent.name }
    }
    if (selectedCrew) {
      return { kind: "crew" as const, crewId: selectedCrew.id, crewSlug: selectedCrew.slug }
    }
    return null
  }, [selectedAgent, selectedCrew])

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      <CrewsSubbar
        workspaceId={workspaceId}
        crewSlug={selectedCrewSlug}
        agentSlug={selectedAgentSlug}
        crewName={selectedCrew?.name ?? null}
        agentName={selectedAgent?.name ?? null}
        onCrewCreated={onRefresh}
        onAgentCreated={(slug) => {
          onRefresh()
          handleAgentSelectBySlug(slug)
        }}
        onOpenExplorer={() => setExplorerOverlayOpen(true)}
        crews={crews}
      />

      {/* Main grid: explorer | canvas */}
      <div
        className="flex-1 min-h-0 grid relative"
        style={{
          gridTemplateColumns: isMobile
            ? "1fr"
            : `${explorerCollapsed ? "48px" : "260px"} 1fr`,
        }}
      >
        {/* Explorer */}
        {isMobile ? (
          <AnimatePresence>
            {explorerOverlayOpen && (
              <>
                <motion.div
                  className="fixed inset-0 bg-black/50 z-30"
                  initial={{ opacity: 0 }}
                  animate={{ opacity: 1 }}
                  exit={{ opacity: 0 }}
                  onClick={() => setExplorerOverlayOpen(false)}
                />
                <motion.div
                  className="fixed left-0 top-0 bottom-0 w-[280px] z-40"
                  initial={{ x: -280 }}
                  animate={{ x: 0 }}
                  exit={{ x: -280 }}
                  transition={{ type: "spring", damping: 25, stiffness: 300 }}
                >
                  <CrewsExplorer
                    crews={crews}
                    agents={agents}
                    selectedCrewId={selectedCrew?.id ?? null}
                    selectedAgentId={selectedAgent?.id ?? null}
                    collapsed={false}
                    onToggleCollapse={() => setExplorerOverlayOpen(false)}
                    onCrewSelect={(id) => { handleCrewSelect(id); setExplorerOverlayOpen(false) }}
                    onAgentSelect={(id) => { handleAgentSelect(id); setExplorerOverlayOpen(false) }}
                  />
                </motion.div>
              </>
            )}
          </AnimatePresence>
        ) : (
          <div className="min-h-0 overflow-hidden">
            <CrewsExplorer
              crews={crews}
              agents={agents}
              selectedCrewId={selectedCrew?.id ?? null}
              selectedAgentId={selectedAgent?.id ?? null}
              collapsed={explorerCollapsed}
              onToggleCollapse={() => setExplorerCollapsed(!explorerCollapsed)}
              onCrewSelect={handleCrewSelect}
              onAgentSelect={handleAgentSelect}
            />
          </div>
        )}

        {/* Canvas — animated cross-fade on selection change */}
        <div className="overflow-y-auto min-h-0 min-w-0">
          <AnimatePresence mode="wait">
            <motion.div
              key={selectedAgent?.slug ?? selectedCrew?.slug ?? "empty"}
              initial={{ opacity: 0, y: 6 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0, y: -4 }}
              transition={{ duration: 0.16, ease: [0.32, 0.72, 0, 1] }}
            >
              {selectedAgent ? (
                <AgentCanvas
                  workspaceId={workspaceId}
                  agentSlug={selectedAgent.slug}
                  crews={crews}
                  onAgentChanged={onRefresh}
                  onSelectCrew={(slug) => selectCrew(slug)}
                  onOpenFiles={handleOpenFiles}
                />
              ) : selectedCrew ? (
                <CrewCanvas
                  workspaceId={workspaceId}
                  crewSlug={selectedCrew.slug}
                  agentsForCrew={crewAgents}
                  missions={missions}
                  onCrewChanged={onRefresh}
                  onSelectAgent={handleAgentSelectBySlug}
                  onOpenFiles={handleOpenFiles}
                  onAddAgent={() => {
                    // Sub-bar holds the create dialog state; defer to it
                    // via the data-attribute click flow on the +Agent button.
                    document.querySelector<HTMLButtonElement>(
                      'button[data-crews-add-agent]',
                    )?.click()
                  }}
                />
              ) : (
                <EmptyRoster
                  agents={agents}
                  crews={crews}
                  onAgentSelect={handleAgentSelectBySlug}
                />
              )}
            </motion.div>
          </AnimatePresence>
        </div>
      </div>

      {/* Bottom panel — context-aware */}
      <BottomPanel
        workspaceId={workspaceId}
        context={bottomContext}
        initialTab={bottomTab}
        initialOpen={bottomOpen}
        onOpenChange={setBottomOpen}
      />
    </div>
  )
}
