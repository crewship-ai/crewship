"use client"

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react"
import { Users } from "lucide-react"

import { CrewsPanel } from "@/components/features/crews/crews-panel"
import type { AgentRecord } from "./agent-canvas-tabs/types"
import type { CrewRecord } from "./crew-canvas-tabs/types"
import { AgentCanvas } from "@/components/features/crews/agent-canvas"
import { CrewCanvas } from "@/components/features/crews/crew-canvas"
import { BottomPanel, type BottomTab } from "@/components/features/crews/bottom-panel"
import { SecondaryPanel, useCloseSecondaryPanelSheet } from "@/components/layout/secondary-panel"
import { PageEmpty } from "@/components/layout/page-state"
import { useCrewsSelection } from "@/hooks/use-crews-selection"
import { useIsMobile } from "@/hooks/use-mobile"
import { useProvisioningStatus } from "@/hooks/use-provisioning-status"
import { useCredentialReadiness } from "@/hooks/use-credential-readiness"
import { useUserPreference } from "@/hooks/use-user-preference"

import { CrewsCreateProvider, useCrewsCreate, type CrewsCreateApi } from "./crews-create"
import type { CrewStart } from "./create-crew/types"
import { CrewsRootBar } from "./crews-bar"
import { DEFAULT_PANEL_STATE, buildPanel, firstCrewSlug } from "./crews-panel-model"
import { agentTabFromParam, crewTabFromParam, tabParam } from "./crews-tabs"

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
  avatar_url?: string | null
  crew?: { name: string; slug: string; color: string | null; avatar_style?: string | null } | null
  _count?: { skills: number; credentials: number; chats: number }
  last_active_at?: string | null
  ephemeral?: boolean
  expires_at?: string | null
  expired_at?: string | null
  parent_lead_id?: string | null
  hire_reason?: string | null
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
  /** Both lists answered at least once; an empty workspace is then really empty. */
  loaded?: boolean
  crewsTotal?: number | null
  agentsTotal?: number | null
  hasMore?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void
  onRefresh: () => void
}

/**
 * Identity of the canvas, keyed on what the USER selected, never on the
 * entity currently resolved from the list: those differ while a fetch is in
 * flight, and keying on the resolved one rebuilt the canvas after every save.
 */
export function canvasKey(agentSlug: string | null, crewSlug: string | null) {
  return agentSlug ?? crewSlug ?? "empty"
}

/**
 * /crews (Harbor 2): the secondary panel of crews and agents, the crew or
 * agent page beside it, and the dock (Terminal, Files, and an agent's log).
 * With nothing selected, desktop opens the last crew seen (or the first in
 * the panel); a phone shows the panel as the page.
 */
/** Hands the create dialogs' API to the layout that hosts them. */
function CreateBridge({ target }: { target: { current: CrewsCreateApi | null } }) {
  target.current = useCrewsCreate()
  return null
}

export function CrewsLayout({
  crews,
  agents,
  missions,
  workspaceId,
  loaded = false,
  crewsTotal = null,
  hasMore = false,
  loadingMore = false,
  onLoadMore,
  onRefresh,
}: CrewsLayoutProps) {
  const isMobile = useIsMobile()
  const closePanelSheet = useCloseSecondaryPanelSheet()
  const createApi = useRef<CrewsCreateApi | null>(null)
  const newCrew = useCallback((start: CrewStart) => createApi.current?.newCrew(start), [])
  const provisioning = useProvisioningStatus(workspaceId)
  const provisioningByCrew = useMemo(
    () => new Map(provisioning.detail.map((d) => [d.id, d.status] as const)),
    [provisioning.detail],
  )
  const readiness = useCredentialReadiness(workspaceId)
  const gapsByCrew = useMemo(() => {
    const out = new Map<string, number>()
    for (const gaps of readiness.gapsByCredential.values()) {
      for (const g of gaps) out.set(g.crewId, (out.get(g.crewId) ?? 0) + 1)
    }
    return out
  }, [readiness.gapsByCredential])

  const { selectedAgentSlug, selectedCrewSlug, tab, setTab, update, selectCrew } = useCrewsSelection()
  const [lastCrew, setLastCrew] = useUserPreference<string | null>("crews.lastCrew", null)

  const [resolvedCrew, setResolvedCrew] = useState<CrewRecord | null>(null)
  const [resolvedAgent, setResolvedAgent] = useState<AgentRecord | null>(null)
  const selectedCrew = useMemo(
    () => (selectedCrewSlug ? (resolvedCrew?.slug === selectedCrewSlug ? resolvedCrew : crews.find((c) => c.slug === selectedCrewSlug)) || null : null),
    [crews, selectedCrewSlug, resolvedCrew],
  )
  const selectedAgent = useMemo(
    () => (selectedAgentSlug ? (resolvedAgent?.slug === selectedAgentSlug ? resolvedAgent : agents.find((a) => a.slug === selectedAgentSlug)) || null : null),
    [agents, selectedAgentSlug, resolvedAgent],
  )
  const crewAgents = useMemo(
    () => (selectedCrew ? agents.filter((a) => a.crew_id === selectedCrew.id) : []),
    [agents, selectedCrew],
  )

  // The crew whose agents the panel lists: the open one, or the open agent's.
  const openCrewId = selectedAgent ? selectedAgent.crew_id : selectedCrew?.id ?? null

  useEffect(() => {
    if (selectedCrewSlug && !selectedAgentSlug && selectedCrewSlug !== lastCrew) setLastCrew(selectedCrewSlug)
  }, [selectedCrewSlug, selectedAgentSlug, lastCrew, setLastCrew])

  // Desktop and tablet never show an empty canvas: open the last crew seen,
  // or the first one in the panel's order (Needs you → Working → Idle).
  useEffect(() => {
    // useIsMobile() is false until its own effect has measured, so this reads
    // the width directly: on a phone the first pass would otherwise open a
    // crew and the list would never show.
    const phone = isMobile || (typeof window !== "undefined" && window.innerWidth < 768)
    if (phone || !loaded || selectedCrewSlug || selectedAgentSlug || crews.length === 0) return
    const remembered = lastCrew && crews.some((c) => c.slug === lastCrew) ? lastCrew : null
    const first = remembered ?? firstCrewSlug(buildPanel({ crews, agents, provisioningByCrew, gapsByCrew, state: DEFAULT_PANEL_STATE }))
    if (first) update({ crew: first })
  }, [isMobile, loaded, selectedCrewSlug, selectedAgentSlug, crews, agents, lastCrew, provisioningByCrew, gapsByCrew, update])

  const [dockRequest, setDockRequest] = useState<{ tab: BottomTab; at: number } | null>(null)
  const openTerminal = useCallback(() => setDockRequest({ tab: "terminal", at: Date.now() }), [])

  const bottomContext = useMemo(() => {
    if (selectedAgent) {
      const parentCrew = selectedAgent.crew_id ? crews.find((c) => c.id === selectedAgent.crew_id) : null
      return {
        kind: "agent" as const,
        agentId: selectedAgent.id,
        agentSlug: selectedAgent.slug,
        agentName: selectedAgent.name,
        crewId: selectedAgent.crew_id,
        crewSlug: parentCrew?.slug ?? selectedAgent.crew?.slug ?? null,
      }
    }
    if (selectedCrew) return { kind: "crew" as const, crewId: selectedCrew.id, crewSlug: selectedCrew.slug }
    return null
  }, [selectedAgent, selectedCrew, crews])

  const nothingOpen = !selectedAgentSlug && !selectedCrewSlug
  const emptyWorkspace = loaded && crews.length === 0 && agents.length === 0

  const panel = (
    <SecondaryPanel id="crews" label="Crews and agents" phoneInline={nothingOpen}>
      <CrewsPanel
        workspaceId={workspaceId}
        crews={crews}
        agents={agents}
        openCrewId={openCrewId}
        selectedCrewId={selectedCrew?.id ?? null}
        selectedAgentId={selectedAgent?.id ?? null}
        onCrewSelect={(slug) => { closePanelSheet(); selectCrew(slug) }}
        onAgentSelect={({ slug, crewSlug }) => { closePanelSheet(); update({ agent: slug, crew: crewSlug }) }}
        onNewCrew={() => newCrew("suggest")}
        crewsTotal={crewsTotal}
        hasMore={hasMore}
        loadingMore={loadingMore}
        onLoadMore={onLoadMore}
        provisioningByCrew={provisioningByCrew}
        gapsByCrew={gapsByCrew}
      />
    </SecondaryPanel>
  )

  const host = (children: ReactNode) => (
    <CrewsCreateProvider
      workspaceId={workspaceId}
      crews={crews}
      currentCrewSlug={selectedCrewSlug ?? (selectedAgent?.crew_id ? crews.find((c) => c.id === selectedAgent.crew_id)?.slug ?? null : null)}
      onCrewCreated={onRefresh}
      onAgentCreated={(slug) => {
        onRefresh()
        if (slug !== selectedAgentSlug) {
          const a = agents.find((x) => x.slug === slug)
          update({ agent: slug, crew: a?.crew_id ? crews.find((c) => c.id === a.crew_id)?.slug ?? null : selectedCrewSlug })
        }
      }}
    >
      <CreateBridge target={createApi} />
      {children}
    </CrewsCreateProvider>
  )

  if (emptyWorkspace) {
    return host(
      <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col bg-background">
        <CrewsRootBar />
        <PageEmpty
          icon={Users}
          title="No crews yet"
          description="A crew is a team of agents with one container, memory and set of tools."
          primary={{ label: "New crew", onClick: () => newCrew("suggest") }}
          cli="crewship crew create"
        />
      </div>,
    )
  }

  return host(
    <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col bg-background">
      {nothingOpen && <CrewsRootBar />}
      <div className="relative flex min-h-0 flex-1">
        {panel}
        {!(isMobile && nothingOpen) && (
          <div className="min-h-0 min-w-0 flex-1 overflow-y-auto" key={canvasKey(selectedAgentSlug, selectedCrewSlug)}>
            {selectedAgentSlug ? (
              <AgentCanvas
                workspaceId={workspaceId}
                agentSlug={selectedAgentSlug}
                onLoaded={setResolvedAgent}
                crews={crews}
                tab={agentTabFromParam(tab)}
                onTabChange={(t) => setTab(tabParam(t))}
                onOpenTerminal={openTerminal}
                onAgentChanged={(nextSlug) => {
                  if (nextSlug && nextSlug !== selectedAgentSlug) update({ agent: nextSlug, tab })
                  onRefresh()
                }}
                onSelectCrew={(slug) => selectCrew(slug)}
              />
            ) : selectedCrewSlug ? (
              <CrewCanvas
                onLoaded={setResolvedCrew}
                workspaceId={workspaceId}
                crewSlug={selectedCrewSlug}
                agentsForCrew={crewAgents}
                missions={missions}
                tab={crewTabFromParam(tab)}
                onTabChange={(t) => setTab(tabParam(t))}
                onCrewChanged={onRefresh}
                onSelectAgent={(slug) => {
                  const a = agents.find((x) => x.slug === slug)
                  update({ agent: slug, crew: a?.crew_id ? crews.find((c) => c.id === a.crew_id)?.slug ?? selectedCrewSlug : selectedCrewSlug })
                }}
                onOpenTerminal={openTerminal}
                onBackToCrews={() => { setLastCrew(null); selectCrew(null) }}
                provisioning={provisioning}
              />
            ) : null}
          </div>
        )}
      </div>
      {/* Terminal and Files of the crew container; a phone has no dock. */}
      {!isMobile && !nothingOpen && (
        <BottomPanel
          workspaceId={workspaceId}
          context={bottomContext}
          tabs={selectedAgent ? ["terminal", "files", "exec"] : ["terminal", "files"]}
          initialTab="terminal"
          request={dockRequest}
        />
      )}
    </div>,
  )
}
