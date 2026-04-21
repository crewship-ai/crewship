"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  Plus, LayoutGrid, Activity,
  Share2, HeartPulse,
  ChevronLeft, PanelLeftOpen,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { FleetExplorer } from "@/components/features/fleet/fleet-explorer"
import { FleetCrewDetail } from "@/components/features/fleet/fleet-crew-detail"
import { FleetAgentDetail } from "@/components/features/fleet/fleet-agent-detail"
import { AllCrewsOverview } from "@/components/features/fleet/fleet-all-crews-overview"
import { HealthOverview } from "@/components/features/fleet/fleet-health-overview"
import { FleetBottomDrawer } from "@/components/features/fleet/fleet-bottom-drawer"
import { CrewActivityFeed } from "@/components/features/crews/crew-activity-feed"
import { useIsMobile } from "@/hooks/use-mobile"
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

type TabId = "overview" | "activity" | "connections" | "health"

export interface FleetLayoutProps {
  crews: CrewData[]
  agents: AgentData[]
  missions: MissionData[]
  workspaceId: string
  onRefresh: () => void
}

const FLEET_TABS = [
  { id: "overview" as const, label: "Overview", icon: LayoutGrid },
  { id: "activity" as const, label: "Activity", icon: Activity },
  { id: "connections" as const, label: "Connections", icon: Share2 },
  { id: "health" as const, label: "Health", icon: HeartPulse },
]

// FLEET_BOTTOM_TABS lives inside fleet-bottom-drawer.tsx now (extracted
// during the drawer component split). The top-level layout only renders
// <FleetBottomDrawer> and does not need the tab list here.

export function FleetLayout({ crews, agents, missions, workspaceId, onRefresh: _onRefresh }: FleetLayoutProps) {
  const isMobile = useIsMobile()

  // Panel state
  const [leftCollapsed, setLeftCollapsed] = useState(false)
  const [activeTab, setActiveTab] = useState<TabId>("overview")

  // Selection state
  const [selectedCrewId, setSelectedCrewId] = useState<string | null>(null)
  const [selectedAgentId, setSelectedAgentId] = useState<string | null>(null)

  // Auto-collapse on mobile
  useEffect(() => {
    if (isMobile) setLeftCollapsed(true)
  }, [isMobile])

  // Derived data
  const selectedCrew = useMemo(
    () => (selectedCrewId ? crews.find((c) => c.id === selectedCrewId) || null : null),
    [crews, selectedCrewId],
  )

  const selectedAgent = useMemo(
    () => (selectedAgentId ? agents.find((a) => a.id === selectedAgentId) || null : null),
    [agents, selectedAgentId],
  )

  const crewAgents = useMemo(
    () => (selectedCrewId ? agents.filter((a) => a.crew_id === selectedCrewId) : []),
    [agents, selectedCrewId],
  )

  // Handlers
  const handleCrewSelect = useCallback((crewId: string) => {
    setSelectedCrewId(crewId)
    setSelectedAgentId(null)
  }, [])

  const handleAgentSelect = useCallback((agentId: string) => {
    setSelectedAgentId((prev) => prev === agentId ? null : agentId)
    // Also select the agent's crew
    const agent = agents.find((a) => a.id === agentId)
    if (agent?.crew_id) setSelectedCrewId(agent.crew_id)
  }, [agents])

  const handleAgentClose = useCallback(() => {
    setSelectedAgentId(null)
  }, [])

  const showRightPanel = selectedAgent !== null

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {/* Toolbar: tabs | actions */}
      <div className="shrink-0 z-20 flex items-stretch h-8 bg-card border-b border-border px-3 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
        {/* Left: tabs */}
        <div className="flex items-stretch">
          {FLEET_TABS.map(({ id, label, icon: Icon }) => (
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
            <Link href="/crews/new">
              <Plus className="h-3 w-3" />
              Crew
            </Link>
          </Button>
          <Button variant="outline" size="sm" className="h-[22px] px-2 text-label font-medium gap-1 bg-muted border-border" asChild>
            <Link href="/agents/new">
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
                    <FleetExplorer
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
            <FleetExplorer
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
                key={`overview-${selectedCrewId || "none"}`}
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="h-full"
              >
                {selectedCrew ? (
                  <FleetCrewDetail
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

            {activeTab === "connections" && (
              <motion.div
                key="connections"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="flex items-center justify-center h-full text-muted-foreground/50"
              >
                <div className="text-center">
                  <Share2 className="h-8 w-8 mx-auto mb-2 opacity-50" />
                  <p className="text-body font-medium">Crew Connections</p>
                  <p className="text-micro text-muted-foreground/40 mt-1">
                    Configure inter-crew communication in{" "}
                    <Link href="/orchestration" className="text-primary hover:underline">Orchestration</Link>
                  </p>
                </div>
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
                className="fixed inset-0 z-40 bg-card flex flex-col"
                initial={{ x: "100%" }}
                animate={{ x: 0 }}
                exit={{ x: "100%" }}
                transition={{ type: "spring", damping: 25, stiffness: 300 }}
              >
                <div className="flex items-center gap-2 px-3 py-2 border-b border-border shrink-0">
                  <button
                    onClick={handleAgentClose}
                    className="h-8 w-8 min-h-[44px] min-w-[44px] flex items-center justify-center text-muted-foreground hover:text-foreground"
                  >
                    <ChevronLeft className="h-4 w-4" />
                  </button>
                  <span className="text-label font-semibold text-muted-foreground uppercase tracking-wider">Agent Detail</span>
                </div>
                <div className="flex-1 overflow-y-auto">
                  <FleetAgentDetail agent={selectedAgent} workspaceId={workspaceId} onClose={handleAgentClose} />
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
                <FleetAgentDetail agent={selectedAgent} workspaceId={workspaceId} onClose={handleAgentClose} />
              )}
            </AnimatePresence>
          </div>
        )}

        {/* Bottom drawer */}
        <FleetBottomDrawer crews={crews} agents={agents} isMobile={isMobile} />
      </div>
    </div>
  )
}
