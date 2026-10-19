"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { toast } from "@/lib/toast"

import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { PageSaveBar, PageSaveProvider } from "@/components/ui/page-save-bar"
import { PageError, PageNotFound } from "@/components/layout/page-state"
import { CrewIconPickerDialog } from "@/components/features/crews/crew-icon-picker-dialog"
import { useAbilities } from "@/hooks/use-abilities"
import { usePagedList } from "@/hooks/use-paged-list"
import { useProvisioningStatus, type ProvisioningStatus } from "@/hooks/use-provisioning-status"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"
import { toApiError } from "@/lib/api-error"
import { backfillAgentAvatars } from "@/lib/agent-avatar-persist"

import { CanvasShell, CanvasTabPanel, CanvasTabs, useEntityFetch, usePatchEntity } from "./canvas-base"
import { CrewPageBar } from "./crews-bar"
import { useCrewYaml } from "./crew-yaml"
import { useCrewsCreate } from "./crews-create"
import { CREW_TABS, type AgentTab, type CrewTab } from "./crews-tabs"
import { crewStatus } from "./crews-panel-model"
import { CrewHeader } from "./crew-header"
import { CrewOverview } from "./crew-overview"
import { CrewTeam, hireState } from "./crew-team"
import { CrewTools } from "./crew-tools"
import { CrewEnvironment } from "./crew-environment"
import { CrewSettings } from "./crew-settings"
import { HireDialog, RehireDialog } from "./crew-hire-dialog"
import { useCrewSpend } from "./crew-spend"
import { MemoryWorkspace } from "./memory-workspace"
import { CrewMemoryCards } from "./crew-memory"
import { deriveCrewNeeds, withDevcontainerFeature, type CrewNeed, type NeedGap } from "./crew-needs"
import type { ContainerStatus } from "./bottom-panel/types"
import type { AgentSummary, CrewIntegration, CrewRecord, MissionData } from "./crew-canvas-tabs/types"

export interface CrewCanvasProps {
  workspaceId: string
  crewSlug: string
  agentsForCrew: AgentSummary[]
  missions?: MissionData[]
  tab: CrewTab
  onTabChange: (tab: CrewTab) => void
  onCrewChanged: () => void
  onLoaded?: (crew: CrewRecord) => void
  /** Opens an agent, on `tab` when given (Memory › Open). */
  onSelectAgent: (slug: string, tab?: AgentTab) => void
  /** Opens the dock on the Terminal tab for this crew. */
  onOpenTerminal: () => void
  /** Opens the dock on the Files tab (Environment › Folders). */
  onOpenFiles?: () => void
  /** Back to the crews list after a delete or from "not found". */
  onBackToCrews: () => void
  /** The layout's one provisioning poller; the canvas does not start its own. */
  provisioning?: ProvisioningStatus
}

/**
 * The crew page (Harbor 2): the top bar's crumbs and actions, the crew head,
 * and the tabs Overview · Team · Tools and access · Environment · Memory ·
 * Settings. The tab is part of the URL (?tab=).
 */
export function CrewCanvas({
  workspaceId,
  crewSlug,
  agentsForCrew: agentsSnapshot,
  tab,
  onTabChange,
  onCrewChanged,
  onLoaded,
  onSelectAgent,
  onOpenTerminal,
  onOpenFiles,
  onBackToCrews,
  provisioning: provisioningProp,
}: CrewCanvasProps) {
  const { entity: crew, setEntity: setCrew, loading, error, refetch: fetchCrew } = useEntityFetch<CrewRecord>({
    workspaceId,
    slug: crewSlug,
    listUrl: "/api/v1/crews",
    detailUrl: (id) => `/api/v1/crews/${id}`,
    matchSlug: (c) => c.slug,
    notFoundMessage: "crew not found",
    listErrorMessage: "crew fetch failed",
    detailErrorMessage: "crew detail fetch failed",
  })
  useEffect(() => { if (crew) onLoaded?.(crew) }, [crew, onLoaded])

  const { abilities, role } = useAbilities()
  const canUpdate = abilities.can("update", "Crew")
  const isAdmin = role === "OWNER" || role === "ADMIN"
  const { newAgent } = useCrewsCreate()

  const ws = encodeURIComponent(workspaceId)
  const roster = usePagedList<AgentSummary>({ url: crew ? `/api/v1/agents?workspace_id=${ws}&crew_id=${encodeURIComponent(crew.id)}` : null, reloadKey: agentsSnapshot })
  const agents = roster.items
  const [integrations, setIntegrations] = useState<CrewIntegration[] | null>(null)
  const [gaps, setGaps] = useState<NeedGap[]>([])
  const [autonomy, setAutonomy] = useState<string | null>(null)
  const [containers, setContainers] = useState<ContainerStatus[] | null>(null)
  const [runsByAgent, setRunsByAgent] = useState<Map<string, number> | null>(null)
  const [needBusy, setNeedBusy] = useState<string | null>(null)
  const [busyAgent, setBusyAgent] = useState<string | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [confirmRestart, setConfirmRestart] = useState(false)
  const [avatarConfirm, setAvatarConfirm] = useState<null | { reset: boolean }>(null)
  const [iconPickerOpen, setIconPickerOpen] = useState(false)
  const [hireOpen, setHireOpen] = useState(false)
  const [rehireAgent, setRehireAgent] = useState<AgentSummary | null>(null)
  const [extendingHire, setExtendingHire] = useState(false)
  const [tick, setTick] = useState(0)

  const ownProvisioning = useProvisioningStatus(provisioningProp ? null : workspaceId)
  const provisioning = provisioningProp ?? ownProvisioning
  const provisioningCrew = provisioning.detail.find((d) => d.id === crew?.id)
  const crewId = crew?.id
  // Activity › Spend is for owners and admins; the server does not check, so
  // nothing is requested for anyone else (#3476).
  const spend = useCrewSpend(workspaceId, crewId, isAdmin)

  useRealtimeEvent("crew.updated", useCallback(() => setTick((n) => n + 1), []))
  // The crew options menu offers the same YAML as Settings > Command line.
  const menuYaml = useCrewYaml(workspaceId, { slug: crew?.slug ?? "", name: crew?.name ?? "" })

  // Everything the head, Overview and Health read, per crew. A failed call
  // contributes nothing rather than a false "all fine".
  useEffect(() => {
    if (!crewId) return
    let cancelled = false
    const get = <T,>(path: string, fallback: T, apply: (v: T) => void) =>
      apiFetch(`${path}${path.includes("?") ? "&" : "?"}workspace_id=${ws}`)
        .then((r) => (r.ok ? r.json() : fallback))
        .then((v: T) => { if (!cancelled) apply(v) })
        .catch(() => { if (!cancelled) apply(fallback) })
    void get<CrewIntegration[]>(`/api/v1/crews/${crewId}/integrations`, [], (v) => setIntegrations(Array.isArray(v) ? v : []))
    void get<{ gaps?: NeedGap[] } | null>(`/api/v1/crews/${crewId}/credential-readiness`, null, (v) => setGaps(Array.isArray(v?.gaps) ? v.gaps : []))
    void get<{ autonomy_level?: string } | null>(`/api/v1/crews/${crewId}/policy`, null, (v) => setAutonomy(v?.autonomy_level ?? null))
    void get<ContainerStatus[] | null>(`/api/v1/crews/${crewId}/containers`, null, (v) => setContainers(Array.isArray(v) ? v : null))
    void get<{ by_agent?: Array<{ agent_id: string; total: number }> } | null>(`/api/v1/runs/insights?window=30d`, null, (v) =>
      setRunsByAgent(Array.isArray(v?.by_agent) ? new Map(v.by_agent.map((r) => [r.agent_id, r.total])) : null))
    return () => { cancelled = true }
  }, [crewId, ws, tick, provisioningCrew?.status])

  const patch = usePatchEntity<CrewRecord>({
    workspaceId,
    entity: crew,
    patchUrl: (c) => `/api/v1/crews/${c.id}`,
    setEntity: setCrew,
    onChanged: onCrewChanged,
  })

  const status = useMemo(() => crewStatus(agents, provisioningCrew?.status, gaps.length), [agents, provisioningCrew?.status, gaps.length])
  const lead = useMemo(() => agents.find((a) => a.agent_role === "LEAD" && !a.expired_at) ?? null, [agents])
  const needs = useMemo(
    () => (crew ? deriveCrewNeeds({
      crewSlug: crew.slug,
      agents,
      provisioning: provisioningCrew?.status,
      provisioningError: provisioningCrew?.error,
      gaps,
      integrations: integrations ?? [],
    }) : []),
    [crew, agents, provisioningCrew?.status, provisioningCrew?.error, gaps, integrations],
  )
  const runtime = containers?.find((c) => c.kind === "crew") ?? null
  const hiresInUse = agents.filter((a) => hireState(a) === "active" || hireState(a) === "pending").length

  const triggerBuild = useCallback(async () => {
    if (!crew) return
    const need = needs.find((n) => n.action.kind === "build")
    setNeedBusy(need?.id ?? "build")
    try {
      const r = await apiFetch(`/api/v1/crews/${crew.id}/provision?workspace_id=${ws}`, { method: "POST" })
      if (!r.ok) throw await toApiError(r, `HTTP ${r.status}`)
      toast.success(`Rebuilding ${crew.name}'s image — usually 30–90 s`)
    } catch (err) {
      toast.error("Build did not start", { description: err instanceof Error ? err.message : String(err) })
    } finally {
      setNeedBusy(null)
    }
  }, [crew, needs, ws])

  const installTool = useCallback(async (need: CrewNeed & { action: { kind: "install" } }) => {
    if (!crew) return
    setNeedBusy(need.id)
    try {
      await patch({ devcontainer_config: withDevcontainerFeature(crew.devcontainer_config, need.action.feature) })
      const r = await apiFetch(`/api/v1/crews/${crew.id}/provision?workspace_id=${ws}`, { method: "POST" })
      if (!r.ok) throw await toApiError(r, `HTTP ${r.status}`)
      toast.success(`Installing ${need.action.tool} — ${crew.name}'s image is rebuilding`)
    } catch (err) {
      toast.error(`Could not install ${need.action.tool}`, { description: err instanceof Error ? err.message : String(err) })
    } finally {
      setNeedBusy(null)
    }
  }, [crew, patch, ws])

  const approveHire = useCallback(async (agent: AgentSummary) => {
    setBusyAgent(agent.id)
    try {
      const res = await apiFetch(`/api/v1/agents/${agent.id}/approve-hire?workspace_id=${ws}`, { method: "POST", headers: { "Content-Type": "application/json" } })
      if (!res.ok) throw await toApiError(res, `HTTP ${res.status}`)
      toast.success(`Approved · ${agent.name} starts now`)
      void roster.refresh()
      onCrewChanged()
    } catch (err) {
      toast.error(`Could not approve ${agent.name}`, { description: err instanceof Error ? err.message : String(err) })
    } finally {
      setBusyAgent(null)
    }
  }, [ws, roster, onCrewChanged])

  // Applying a style clears every stored render in the crew; the new faces
  // are stored as part of this edit (#2876).
  const applyAvatarStyle = useCallback(async (resetOverrides: boolean) => {
    if (!crew) return
    const verb = resetOverrides ? "Reset" : "Apply"
    try {
      const url = `/api/v1/crews/${crew.id}/apply-avatar-style?workspace_id=${ws}${resetOverrides ? "&reset_overrides=true" : ""}`
      const res = await apiFetch(url, { method: "POST" })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      await backfillAgentAvatars(workspaceId, { crewId: crew.id }).catch(() => undefined)
      toast.success(`${verb} done for ${agents.length} agent${agents.length === 1 ? "" : "s"}`)
      onCrewChanged()
    } catch (err) {
      toast.error(`${verb} failed`, { description: err instanceof Error ? err.message : String(err) })
      throw err
    }
  }, [crew, agents.length, onCrewChanged, workspaceId, ws])

  const restartAgents = useCallback(async () => {
    if (!crew) return
    const r = await apiFetch(`/api/v1/crews/${encodeURIComponent(crew.id)}/restart-agents?workspace_id=${ws}`, { method: "POST" })
    if (!r.ok) {
      toast.error("Agents could not be restarted", { description: `The server answered ${r.status}. You can retry.` })
      throw new Error(`HTTP ${r.status}`)
    }
    toast.success("Agents restarted", { description: "A fresh container starts on the next agent run." })
    setTick((n) => n + 1)
  }, [crew, ws])

  const handleDelete = useCallback(async () => {
    if (!crew) return
    const res = await apiFetch(`/api/v1/crews/${crew.id}?workspace_id=${ws}`, { method: "DELETE" })
    if (!res.ok) {
      toast.error("Delete failed", { description: `HTTP ${res.status}` })
      throw new Error(`HTTP ${res.status}`)
    }
    toast.success(`Crew "${crew.name}" deleted`)
    onCrewChanged()
    onBackToCrews()
  }, [crew, onCrewChanged, onBackToCrews, ws])

  if (loading) return <CanvasShell loading error={null} notLoadedLabel="">{null}</CanvasShell>
  if (error === "crew not found") {
    return <PageNotFound what="crew" description={<>No crew is called “{crewSlug}” in this workspace. It may have been deleted, or the link is wrong.</>} back={{ label: "Back to crews", onClick: onBackToCrews }} />
  }
  if (error || !crew) {
    return <PageError title="The crew could not be loaded" description={error ?? undefined} onRetry={() => void fetchCrew()} />
  }

  return (
    <PageSaveProvider>
      <CrewPageBar
        crew={crew}
        lead={lead}
        onTerminal={onOpenTerminal}
        onHire={canUpdate ? () => setHireOpen(true) : undefined}
        onRestartAgents={canUpdate ? () => setConfirmRestart(true) : undefined}
        onViewYaml={menuYaml.view}
        onDownloadYaml={() => void menuYaml.download()}
        onDelete={isAdmin ? () => setConfirmDelete(true) : undefined}
      />
      <CanvasShell loading={false} error={null} notLoadedLabel="">
        <CrewHeader
          crew={crew}
          status={status}
          lead={lead}
          agentCount={agents.filter((a) => !a.expired_at).length}
          autonomy={autonomy}
          containerState={runtime?.status ?? null}
          onPickIcon={canUpdate ? () => setIconPickerOpen(true) : undefined}
        />

        <CanvasTabs<CrewTab>
          tabs={CREW_TABS.map((t) => (t.id === "team" ? { ...t, label: `Team ${agents.filter((a) => !a.expired_at).length}` } : t))}
          active={tab}
          onChange={onTabChange}
          idPrefix="crew-canvas"
          label="Crew sections"
        />

        <CanvasTabPanel idPrefix="crew-canvas" active={tab} className="space-y-4 pb-24">
          {roster.error && (
            <p role="alert" className="text-body text-chip-danger-fg">The team could not be loaded. <button type="button" className="underline" onClick={() => void roster.refresh()}>Retry</button></p>
          )}
          {tab === "overview" && (
            <CrewOverview
              workspaceId={workspaceId}
              crew={crew}
              agents={agents}
              needs={needs}
              busyNeedId={needBusy}
              onBuild={triggerBuild}
              onInstall={installTool}
              containers={containers}
              provisioning={provisioningCrew?.status}
              gapCount={gaps.length}
              integrations={integrations}
              onSelectAgent={onSelectAgent}
              onTab={onTabChange}
              onNewAgent={() => newAgent(crew.slug)}
              onHire={canUpdate ? () => setHireOpen(true) : undefined}
              spend={spend}
            />
          )}
          {tab === "team" && (
            <>
              <CrewTeam
                agents={agents}
                runsByAgent={runsByAgent}
                autonomy={autonomy}
                hireLimit={crew.max_ephemeral_agents ?? null}
                leadName={lead?.name ?? null}
                onSelectAgent={onSelectAgent}
                onNewAgent={() => newAgent(crew.slug)}
                onHire={() => setHireOpen(true)}
                onApprove={(a) => void approveHire(a)}
                onRehire={(a) => { setExtendingHire(false); setRehireAgent(a) }}
                onExtend={canUpdate ? (a) => { setExtendingHire(true); setRehireAgent(a) } : undefined}
                costByAgent={spend?.byAgent ?? null}
                busyAgentId={busyAgent}
              />
              {roster.hasMore && <button type="button" className="text-body text-primary-hover hover:underline" disabled={roster.loadingMore} onClick={() => void roster.loadMore()}>Load more agents</button>}
            </>
          )}
          {tab === "tools" && <CrewTools workspaceId={workspaceId} crewSlug={crew.slug} agents={agents} integrations={integrations} />}
          {tab === "env" && (
            <CrewEnvironment
              workspaceId={workspaceId}
              crew={crew}
              canEdit={canUpdate}
              isAdmin={isAdmin}
              containers={containers}
              runningAgents={agents.filter((a) => a.status === "RUNNING").length}
              onRestartAgents={() => setConfirmRestart(true)}
              onOpenTerminal={onOpenTerminal}
              onOpenFiles={onOpenFiles}
              onContainersChanged={() => setTick((n) => n + 1)}
              patch={patch}
              onSaved={() => { void fetchCrew(); onCrewChanged() }}
            />
          )}
          {tab === "memory" && (
            <>
              <CrewMemoryCards workspaceId={workspaceId} crewId={crew.id} agents={agents.filter((a) => !a.expired_at)} onOpenAgent={(slug) => onSelectAgent(slug, "memory")} canDecideProposals={isAdmin} />
              <MemoryWorkspace key={crew.id} workspaceId={workspaceId} crewId={crew.id} />
            </>
          )}
          {tab === "settings" && (
            <CrewSettings
              workspaceId={workspaceId}
              crew={crew}
              agents={agents}
              patch={patch}
              onApplyAvatarStyle={(reset) => setAvatarConfirm({ reset })}
              onDelete={isAdmin ? () => setConfirmDelete(true) : undefined}
            />
          )}
        </CanvasTabPanel>
      </CanvasShell>
      <PageSaveBar />

      <CrewIconPickerDialog
        open={iconPickerOpen}
        onOpenChange={setIconPickerOpen}
        crewName={crew.name}
        icon={crew.icon}
        color={crew.color}
        onSave={async ({ icon, color }) => {
          await patch({ icon, color })
          toast.success("Icon updated")
        }}
      />
      <HireDialog
        open={hireOpen}
        onOpenChange={setHireOpen}
        workspaceId={workspaceId}
        crew={crew}
        autonomy={autonomy}
        inUse={hiresInUse}
        limit={crew.max_ephemeral_agents ?? null}
        onHired={() => { void roster.refresh(); onCrewChanged() }}
      />
      <RehireDialog
        open={rehireAgent !== null}
        onOpenChange={(o) => { if (!o) setRehireAgent(null) }}
        workspaceId={workspaceId}
        agent={rehireAgent}
        extend={extendingHire}
        onRehired={() => { void roster.refresh(); onCrewChanged() }}
      />
      {menuYaml.surface}
      <ConfirmDialog
        open={confirmRestart}
        onOpenChange={setConfirmRestart}
        title={`Restart the agents of ${crew.name}?`}
        description="This stops the shared container and can interrupt work by every agent in this crew. The next agent run creates a fresh container."
        consequences={[
          { tone: "lost", text: `${agents.filter((a) => a.status === "RUNNING").length} running agent${agents.filter((a) => a.status === "RUNNING").length === 1 ? " stops" : "s stop"} where it is` },
          { tone: "kept", text: "Files in /crew, memory and run history stay" },
        ]}
        confirmLabel="Restart agents"
        onConfirm={restartAgents}
      />
      <ConfirmDialog
        open={avatarConfirm !== null}
        onOpenChange={(o) => { if (!o) setAvatarConfirm(null) }}
        title={avatarConfirm?.reset ? `Reset every agent's avatar in ${crew.name}?` : `Apply the crew's avatar style to every agent in ${crew.name}?`}
        description={avatarConfirm?.reset
          ? `Uses “${crew.avatar_style ?? "robots"}” for all ${agents.length} agents and clears their own avatars.`
          : `Uses “${crew.avatar_style ?? "robots"}” for all ${agents.length} agents that follow the crew.`}
        confirmLabel={avatarConfirm?.reset ? "Reset avatars" : "Apply style"}
        onConfirm={() => applyAvatarStyle(avatarConfirm?.reset ?? false)}
      />
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete crew ${crew.name}?`}
        description="This deletes the crew and its agents. Background cleanup removes runtimes whose ownership is verified; older runtimes may need manual cleanup. Removing a runtime stops its processes and discards files stored only in its container writable layer."
        consequences={[
          { tone: "lost", text: `${agents.length === 1 ? "1 agent is" : `${agents.length} agents are`} deleted along with the crew` },
          { tone: "lost", text: "Declared sidecar containers and their data volumes are also deleted" },
          { tone: "lost", text: "Crew issues are deleted" },
          { tone: "kept", text: "Background runtime cleanup preserves mounted volumes, host data and shared images" },
        ]}
        confirmLabel="Delete crew"
        destructive
        typeToConfirm={crew.slug}
        onConfirm={handleDelete}
      />
    </PageSaveProvider>
  )
}
