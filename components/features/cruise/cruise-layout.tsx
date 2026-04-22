"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { toast } from "sonner"
import {
  Plus, LayoutGrid, Activity,
  HeartPulse,
  ChevronLeft, PanelLeftOpen,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { CruiseExplorer } from "@/components/features/cruise/cruise-explorer"
import { CruiseCrewDetail } from "@/components/features/cruise/cruise-crew-detail"
import { CruiseAgentInbox } from "@/components/features/cruise/cruise-agent-inbox"
import { CruiseAgentInline } from "@/components/features/cruise/cruise-agent-inline"
import { AllCrewsOverview } from "@/components/features/cruise/cruise-all-crews-overview"
import { HealthOverview } from "@/components/features/cruise/cruise-health-overview"
import { CruiseBottomDrawer } from "@/components/features/cruise/cruise-bottom-drawer"
import { CrewActivityFeed } from "@/components/features/crews/crew-activity-feed"
import { useIsMobile } from "@/hooks/use-mobile"
import { useCruiseSelection } from "@/hooks/use-cruise-selection"
import { useKeyboardShortcuts } from "@/hooks/use-keyboard-shortcuts"
import Link from "next/link"

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

type TabId = "overview" | "activity" | "health"

export interface CruiseLayoutProps {
  crews: CrewData[]
  agents: AgentData[]
  missions: MissionData[]
  workspaceId: string
  onRefresh: () => void
}

const CRUISE_TABS = [
  { id: "overview" as const, label: "Overview", icon: LayoutGrid },
  { id: "activity" as const, label: "Activity", icon: Activity },
  { id: "health" as const, label: "Health", icon: HeartPulse },
]

// CRUISE_BOTTOM_TABS lives inside cruise-bottom-drawer.tsx now (extracted
// during the drawer component split). The top-level layout only renders
// <CruiseBottomDrawer> and does not need the tab list here.

export function CruiseLayout({ crews, agents, missions, workspaceId, onRefresh: _onRefresh }: CruiseLayoutProps) {
  const isMobile = useIsMobile()
  const { selectedAgentSlug, selectedCrewSlug, update, selectAgent } = useCruiseSelection()

  const [leftCollapsed, setLeftCollapsed] = useState(false)
  const [activeTab, setActiveTab] = useState<TabId>("overview")

  useEffect(() => {
    if (isMobile) setLeftCollapsed(true)
  }, [isMobile])

  const staleSlugNotified = useRef<string | null>(null)
  useEffect(() => {
    // Don't clear URL params until entity lists have actually loaded,
    // otherwise deep-links like /cruise?agent=filip get wiped on first paint
    // before agents[] has arrived from the parent fetch.
    if (agents.length === 0 && crews.length === 0) return
    if (selectedAgentSlug && agents.length > 0 && !agents.find((a) => a.slug === selectedAgentSlug)) {
      if (staleSlugNotified.current !== selectedAgentSlug) {
        staleSlugNotified.current = selectedAgentSlug
        toast.warning(`Agent "${selectedAgentSlug}" not found`)
        update({ agent: null })
      }
    } else if (selectedCrewSlug && crews.length > 0 && !crews.find((c) => c.slug === selectedCrewSlug)) {
      if (staleSlugNotified.current !== selectedCrewSlug) {
        staleSlugNotified.current = selectedCrewSlug
        toast.warning(`Crew "${selectedCrewSlug}" not found`)
        update({ crew: null })
      }
    } else {
      staleSlugNotified.current = null
    }
  }, [selectedAgentSlug, selectedCrewSlug, agents, crews, update])

  const selectedCrew = useMemo(
    () => (selectedCrewSlug ? crews.find((c) => c.slug === selectedCrewSlug) || null : null),
    [crews, selectedCrewSlug],
  )

  const selectedAgent = useMemo(
    () => (selectedAgentSlug ? agents.find((a) => a.slug === selectedAgentSlug) || null : null),
    [agents, selectedAgentSlug],
  )

  const selectedCrewId = selectedCrew?.id ?? null
  const selectedAgentId = selectedAgent?.id ?? null

  const crewAgents = useMemo(
    () => (selectedCrewId ? agents.filter((a) => a.crew_id === selectedCrewId) : []),
    [agents, selectedCrewId],
  )

  const handleCrewSelect = useCallback((crewId: string) => {
    const crew = crews.find((c) => c.id === crewId)
    if (!crew) return
    update({ crew: crew.slug, agent: null })
  }, [crews, update])

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

  const handleAgentClose = useCallback(() => {
    selectAgent(null)
  }, [selectAgent])

  const showRightPanel = selectedAgent !== null

  const cycleAgent = useCallback((delta: 1 | -1) => {
    if (agents.length === 0) return
    const currentIdx = selectedAgent ? agents.findIndex((a) => a.slug === selectedAgent.slug) : -1
    const nextIdx = currentIdx < 0
      ? (delta === 1 ? 0 : agents.length - 1)
      : (currentIdx + delta + agents.length) % agents.length
    const next = agents[nextIdx]
    if (!next) return
    const parentCrew = next.crew_id ? crews.find((c) => c.id === next.crew_id) : null
    update({ agent: next.slug, crew: parentCrew?.slug ?? null })
  }, [agents, crews, selectedAgent, update])

  useKeyboardShortcuts([
    { keys: "Escape", handler: handleAgentClose, enabled: showRightPanel },
    { keys: "j", handler: () => cycleAgent(1) },
    { keys: "k", handler: () => cycleAgent(-1) },
  ])

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {/* Toolbar: tabs | actions */}
      <div className="shrink-0 z-20 flex items-stretch h-8 bg-card border-b border-border px-3 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
        {/* Left: tabs */}
        <div className="flex items-stretch">
          {CRUISE_TABS.map(({ id, label, icon: Icon }) => (
            <button
              key={id}
              onClick={() => setActiveTab(id)}
              className={cn(
                "flex items-center gap-1.5 px-3 text-label font-medium border-b-2 transition-all duration-100 relative top-px",
                activeTab === id
                  ? "border-primary text-primary"
                  : "border-transparent text-muted-foreground hover:text-foreground/80",
              )}
            >
              <Icon className="h-3 w-3 opacity-75" />
              {label}
            </button>
          ))}
        </div>

        {/* Right: actions */}
        <div className="flex items-center gap-1.5 ml-auto shrink-0">
          <Button size="sm" className="h-[22px] px-2 text-label font-medium gap-1" asChild>
            <Link href="/cruise/crews/new">
              <Plus className="h-3 w-3" />
              Crew
            </Link>
          </Button>
          <Button variant="outline" size="sm" className="h-[22px] px-2 text-label font-medium gap-1 bg-muted border-border" asChild>
            <Link href="/cruise/agents/new">
              <Plus className="h-3 w-3" />
              Agent
            </Link>
          </Button>
        </div>
      </div>

      {/* Main 3-column layout */}
      <div
        className="flex-1 min-h-0 grid transition-all duration-200 relative"
        style={{
          gridTemplateColumns: isMobile
            ? "1fr"
            : `${leftCollapsed ? "48px" : "280px"} 1fr ${showRightPanel ? "380px" : "0px"}`,
          gridTemplateRows: "1fr auto",
        }}
      >
        {/* Left panel */}
        {isMobile ? (
          <>
            {leftCollapsed && (
              <button
                className="absolute top-2 left-2 z-20 h-8 w-8 min-h-[44px] min-w-[44px] rounded-md bg-card border border-border flex items-center justify-center text-muted-foreground hover:text-foreground"
                onClick={() => setLeftCollapsed(false)}
              >
                <PanelLeftOpen className="h-3.5 w-3.5" />
              </button>
            )}
            <AnimatePresence>
              {!leftCollapsed && (
                <>
                  <motion.div
                    className="fixed inset-0 bg-black/50 z-30"
                    initial={{ opacity: 0 }}
                    animate={{ opacity: 1 }}
                    exit={{ opacity: 0 }}
                    onClick={() => setLeftCollapsed(true)}
                  />
                  <motion.div
                    className="fixed left-0 top-0 bottom-0 w-[280px] z-40"
                    initial={{ x: -280 }}
                    animate={{ x: 0 }}
                    exit={{ x: -280 }}
                    transition={{ type: "spring", damping: 25, stiffness: 300 }}
                  >
                    <CruiseExplorer
                      crews={crews}
                      agents={agents}
                      selectedCrewId={selectedCrewId}
                      selectedAgentId={selectedAgentId}
                      collapsed={false}
                      onToggleCollapse={() => setLeftCollapsed(true)}
                      onCrewSelect={(id) => { handleCrewSelect(id); setLeftCollapsed(true) }}
                      onAgentSelect={(id) => { handleAgentSelect(id); setLeftCollapsed(true) }}
                    />
                  </motion.div>
                </>
              )}
            </AnimatePresence>
          </>
        ) : (
          <div className="row-span-1 min-h-0 overflow-hidden">
            <CruiseExplorer
              crews={crews}
              agents={agents}
              selectedCrewId={selectedCrewId}
              selectedAgentId={selectedAgentId}
              collapsed={leftCollapsed}
              onToggleCollapse={() => setLeftCollapsed(!leftCollapsed)}
              onCrewSelect={handleCrewSelect}
              onAgentSelect={handleAgentSelect}
            />
          </div>
        )}

        {/* Center content */}
        <div className="row-span-1 relative overflow-hidden min-h-0">
          <AnimatePresence mode="wait">
            {activeTab === "overview" && (
              <motion.div
                key={`overview-${selectedAgentId || selectedCrewId || "none"}`}
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="h-full"
              >
                {selectedAgent ? (
                  <CruiseAgentInline
                    agent={selectedAgent}
                    workspaceId={workspaceId}
                  />
                ) : selectedCrew ? (
                  <CruiseCrewDetail
                    crew={selectedCrew}
                    agents={crewAgents}
                    missions={missions}
                    onAgentClick={handleAgentSelect}
                  />
                ) : (
                  <AllCrewsOverview
                    crews={crews}
                    agents={agents}
                    onCrewSelect={handleCrewSelect}
                    onAgentSelect={handleAgentSelect}
                  />
                )}
              </motion.div>
            )}

            {activeTab === "activity" && (
              <motion.div
                key="activity"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="p-4 h-full overflow-auto"
              >
                <CrewActivityFeed workspaceId={workspaceId} />
              </motion.div>
            )}

            {activeTab === "health" && (
              <motion.div
                key="health"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="p-4 h-full overflow-auto"
              >
                <HealthOverview crews={crews} agents={agents} />
              </motion.div>
            )}
          </AnimatePresence>
        </div>

        {/* Right panel */}
        {isMobile ? (
          <AnimatePresence>
            {showRightPanel && selectedAgent && (
              <motion.div
                className="fixed inset-0 z-40 bg-background flex flex-col"
                initial={{ x: "100%" }}
                animate={{ x: 0 }}
                exit={{ x: "100%" }}
                transition={{ type: "spring", damping: 25, stiffness: 300 }}
              >
                <div className="flex items-center gap-2 px-3 py-2 border-b border-border shrink-0 bg-card">
                  <button
                    onClick={handleAgentClose}
                    className="h-8 w-8 min-h-[44px] min-w-[44px] flex items-center justify-center text-muted-foreground hover:text-foreground"
                    aria-label="Back"
                  >
                    <ChevronLeft className="h-4 w-4" />
                  </button>
                  <span className="text-label font-semibold truncate">{selectedAgent.name}</span>
                </div>
                <div className="flex-1 overflow-y-auto">
                  <CruiseAgentInline agent={selectedAgent} workspaceId={workspaceId} />
                </div>
              </motion.div>
            )}
          </AnimatePresence>
        ) : (
          <div className={cn(
            "row-span-1 transition-all duration-200 overflow-hidden min-h-0",
            showRightPanel ? "w-[380px]" : "w-0",
          )}>
            <AnimatePresence mode="wait">
              {showRightPanel && selectedAgent && (
                <CruiseAgentInbox agent={selectedAgent} workspaceId={workspaceId} onClose={handleAgentClose} />
              )}
            </AnimatePresence>
          </div>
        )}

        {/* Bottom drawer */}
        <CruiseBottomDrawer crews={crews} agents={agents} isMobile={isMobile} />
      </div>
    </div>
  )
}
