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
import { CrewsExplorer } from "@/components/features/crews/crews-explorer"
import { CrewsCrewDetail } from "@/components/features/crews/crews-crew-detail"
import { CrewsAgentInline } from "@/components/features/crews/crews-agent-inline"
import { CrewsContextHeader } from "@/components/features/crews/crews-context-header"
import { AllCrewsOverview } from "@/components/features/crews/crews-all-crews-overview"
import { HealthOverview } from "@/components/features/crews/crews-health-overview"
import { CrewsBottomDrawer } from "@/components/features/crews/crews-bottom-drawer"
import { CrewActivityFeed } from "@/components/features/crews/crew-activity-feed"
import { ChatDrawer } from "@/components/features/crews/drawers/chat-drawer"
import { LogsDrawer } from "@/components/features/crews/drawers/logs-drawer"
import { SettingsDrawer } from "@/components/features/crews/drawers/settings-drawer"
import { useIsMobile } from "@/hooks/use-mobile"
import { useCrewsSelection, type CrewsTab, type CrewsDrawer } from "@/hooks/use-crews-selection"
import { useKeyboardShortcuts } from "@/hooks/use-keyboard-shortcuts"
import { useRouter } from "next/navigation"
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

const CREWS_TABS: Array<{ id: CrewsTab; label: string; icon: typeof LayoutGrid }> = [
  { id: "overview", label: "Overview", icon: LayoutGrid },
  { id: "activity", label: "Activity", icon: Activity },
  { id: "health", label: "Health", icon: HeartPulse },
]

// CREWS_BOTTOM_TABS lives inside crews-bottom-drawer.tsx now (extracted
// during the drawer component split). The top-level layout only renders
// <CrewsBottomDrawer> and does not need the tab list here.

export function CrewsLayout({ crews, agents, missions, workspaceId, loaded = false, onRefresh: _onRefresh }: CrewsLayoutProps) {
  const isMobile = useIsMobile()
  const router = useRouter()
  const {
    selectedAgentSlug,
    selectedCrewSlug,
    activeTab,
    activeDrawer,
    update,
    selectAgent,
    setTab,
    openDrawer,
    closeDrawer,
  } = useCrewsSelection()

  const [leftCollapsed, setLeftCollapsed] = useState(false)

  useEffect(() => {
    if (isMobile) setLeftCollapsed(true)
  }, [isMobile])

  const staleSlugNotified = useRef<string | null>(null)
  useEffect(() => {
    // Wait for the parent fetch to actually finish before judging a slug
    // as stale. `agents.length > 0` / `crews.length > 0` would otherwise
    // treat a legitimately empty workspace as "still loading" forever,
    // and would also flip deep-links like `/crews?agent=filip` to null
    // on first paint before the fetch had a chance to populate.
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

  const selectedCrewId = selectedCrew?.id ?? null
  const selectedAgentId = selectedAgent?.id ?? null

  const crewAgents = useMemo(
    () => (selectedCrewId ? agents.filter((a) => a.crew_id === selectedCrewId) : []),
    [agents, selectedCrewId],
  )

  // Click on a crew in the explorer navigates to the full crew page.
  // Crew config (network policy, runtime, containers, MCP, avatar, issue
  // prefix, terminal, peer conversations, escalations, journal, danger
  // zone) lives across six tabs on /crews/<id> and doesn't fit into the
  // center preview. Agents keep the shallow ?agent=slug preview because
  // their data is lighter and the right-panel inbox adds real value.
  const handleCrewSelect = useCallback((crewId: string) => {
    if (!crews.find((c) => c.id === crewId)) return
    router.push(`/crews/${crewId}`)
  }, [crews, router])

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

  // Mobile still needs a full-screen agent panel because the explorer
  // and context header share the same narrow viewport. Desktop renders
  // the agent inline in the center column.
  const showMobileAgentPanel = isMobile && selectedAgent !== null

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
    { keys: "Escape", handler: handleAgentClose, enabled: selectedAgent !== null && activeDrawer === null },
    { keys: "j", handler: () => cycleAgent(1) },
    { keys: "k", handler: () => cycleAgent(-1) },
  ])

  const handleDrawerOpenChange = useCallback(
    (drawer: CrewsDrawer) => (open: boolean) => {
      if (!open && activeDrawer === drawer) closeDrawer()
    },
    [activeDrawer, closeDrawer],
  )

  // Settings drawer operates on the current entity (agent first, then crew).
  // Kept as a single drawer component because the editor surface is
  // interchangeable at the Sheet level — the body will branch by entity kind
  // once Phase 3 inlines the real forms.
  const settingsEntity = selectedAgent
    ? { kind: "agent" as const, id: selectedAgent.id, name: selectedAgent.name }
    : selectedCrew
      ? { kind: "crew" as const, id: selectedCrew.id, name: selectedCrew.name }
      : null

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {/* Toolbar: tabs | actions */}
      <div className="shrink-0 z-20 flex items-stretch h-8 bg-card border-b border-border px-3 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
        {/* Left: tabs */}
        <div className="flex items-stretch">
          {CREWS_TABS.map(({ id, label, icon: Icon }) => (
            <button
              key={id}
              onClick={() => setTab(id)}
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
            <Link href="/crews/agents/new">
              <Plus className="h-3 w-3" />
              Agent
            </Link>
          </Button>
        </div>
      </div>

      {/* Context header — compact identity strip for the selected entity.
          Renders nothing in the workspace (all-crews) view, so no vertical
          space is eaten when nothing is selected. */}
      <CrewsContextHeader
        agent={selectedAgent}
        crew={!selectedAgent ? selectedCrew : null}
        onOpenDrawer={openDrawer}
      />

      {/* Main 2-column layout (explorer + canvas). The right-hand Inbox
          panel retired in Phase 4 — its approvals/escalations counters
          moved to context-header alert pills, peer messages relocate to
          the Activity tab (Phase 5), and Lead/Memory/Schedule chips
          move to the Health tab (Phase 6). */}
      <div
        className="flex-1 min-h-0 grid transition-all duration-200 relative"
        style={{
          gridTemplateColumns: isMobile
            ? "1fr"
            : `${leftCollapsed ? "48px" : "280px"} 1fr`,
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
                    <CrewsExplorer
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
            <CrewsExplorer
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
                  <CrewsAgentInline
                    agent={selectedAgent}
                    workspaceId={workspaceId}
                  />
                ) : selectedCrew ? (
                  <CrewsCrewDetail
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
                key={`activity-${selectedAgentId || selectedCrewId || "workspace"}`}
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="p-4 h-full overflow-auto"
              >
                <CrewActivityFeed
                  workspaceId={workspaceId}
                  agentId={selectedAgentId ?? undefined}
                  crewId={!selectedAgentId && selectedCrewId ? selectedCrewId : undefined}
                />
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

        {/* Mobile full-screen agent panel. The desktop grid renders the
            agent inline in the center column, but on a narrow viewport
            the explorer overlay + header strip eat the entire width, so
            selecting an agent promotes the center into a dedicated
            slide-over panel with its own back button. */}
        {isMobile && (
          <AnimatePresence>
            {showMobileAgentPanel && selectedAgent && (
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
                  <CrewsAgentInline agent={selectedAgent} workspaceId={workspaceId} />
                </div>
              </motion.div>
            )}
          </AnimatePresence>
        )}

        {/* Bottom drawer */}
        <CrewsBottomDrawer crews={crews} agents={agents} isMobile={isMobile} />
      </div>

      {/* Slide-over drawers — Sheets controlled by the URL ?drawer= param so
          reload/back preserves the open panel. Phase 3 will replace the stub
          bodies with inline Chat / Logs / Settings content, making the
          existing full-page routes redundant. */}
      <ChatDrawer
        agent={selectedAgent}
        open={activeDrawer === "chat" && selectedAgent !== null}
        onOpenChange={handleDrawerOpenChange("chat")}
      />
      <LogsDrawer
        agent={selectedAgent}
        open={activeDrawer === "logs" && selectedAgent !== null}
        onOpenChange={handleDrawerOpenChange("logs")}
      />
      <SettingsDrawer
        entity={settingsEntity}
        open={activeDrawer === "settings" && settingsEntity !== null}
        onOpenChange={handleDrawerOpenChange("settings")}
      />
    </div>
  )
}
