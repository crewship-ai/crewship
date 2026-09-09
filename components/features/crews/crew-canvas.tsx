"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { toast } from "sonner"
import { Files, Pencil, Settings2 } from "lucide-react"
import { CrewIcon } from "@/components/ui/crew-icon"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "@/components/ui/dialog"
import { CreateCrewDialog } from "./create-crew-dialog"
import { CrewRestartButton } from "./crew-restart-button"
import { MemoryWorkspace } from "./memory-workspace"
import { CrewIconPickerDialog } from "@/components/features/crews/crew-icon-picker-dialog"
import { usePagedList } from "@/hooks/use-paged-list"
import { apiFetch } from "@/lib/api-fetch"

import { ProvisioningBanner } from "./crew-canvas-banner"
import { CrewNeedsYou } from "./crew-needs-you"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { deriveCrewNeeds, withDevcontainerFeature, type CrewNeed, type NeedGap } from "./crew-needs"
import { useProvisioningStatus, type ProvisioningStatus } from "@/hooks/use-provisioning-status"
import { entityHref } from "@/lib/entity-links"
import { CrewPrivilegedBadge } from "./crew-privileged-badge"
import {
  CanvasShell,
  CanvasTabPanel,
  CanvasTabs,
  useEntityFetch,
  usePatchEntity,
  useResetTabOnSlugChange,
} from "./canvas-base"
import { OverviewTab } from "./crew-canvas-tabs/overview-tab"
import { RosterTab } from "./crew-canvas-tabs/roster-tab"
import { EntityWork } from "./entity-work"
import { SettingsTab } from "./crew-canvas-tabs/settings-tab"
import type {
  AgentSummary,
  CrewIntegration,
  CrewMemberRow,
  CrewRecord,
  MissionData,
} from "./crew-canvas-tabs/types"


type CrewTab = "overview" | "roster" | "missions" | "memory"

const TABS: Array<{ id: CrewTab; label: string }> = [
  { id: "overview", label: "Overview" },
  { id: "roster", label: "Team" },
  { id: "missions", label: "Work" },
  { id: "memory", label: "Memory" },
]


export interface CrewCanvasProps {
  workspaceId: string
  crewSlug: string
  agentsForCrew: AgentSummary[]
  missions: MissionData[]
  onCrewChanged: () => void
  onLoaded?: (crew: CrewRecord) => void
  onSelectAgent: (slug: string) => void
  onOpenFiles: () => void
  /** The layout's one provisioning poller; the canvas does not start its own. */
  provisioning?: ProvisioningStatus
}

/**
 * Crew canvas — drives the right pane when ?crew=<slug> is selected.
 * Tabbed layout: Overview / Roster / Missions / Files / Settings.
 *
 * Header (always visible) shows icon + name + slug + container summary +
 * the two primary CTAs (Files, Add agent). Tabs below let users focus
 * on one concern at a time without scrolling 700+ lines.
 */

export function CrewCanvas({
  workspaceId,
  crewSlug,
  agentsForCrew: agentsSnapshot,
  missions: _missions,
  onCrewChanged,
  onLoaded,
  onSelectAgent,
  onOpenFiles,
  provisioning: provisioningProp,
}: CrewCanvasProps) {
  const {
    entity: crew,
    setEntity: setCrew,
    loading,
    error,
    refetch: fetchCrew,
  } = useEntityFetch<CrewRecord>({
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

  const roster = usePagedList<AgentSummary>({ url: crew ? `/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}&crew_id=${encodeURIComponent(crew.id)}` : null, reloadKey: agentsSnapshot })
  const agentsForCrew = roster.items
  const [editOpen, setEditOpen] = useState(false)
  const [adminOpen, setAdminOpen] = useState(false)
  const [tab, setTab] = useState<CrewTab>("overview")
  const [integrations, setIntegrations] = useState<CrewIntegration[] | null>(null)
  const [gaps, setGaps] = useState<NeedGap[]>([])
  const [needBusy, setNeedBusy] = useState<string | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  // Polls only when no parent supplies the status (a null workspace id
  // disables the hook), so /crews runs one poller, not four.
  const ownProvisioning = useProvisioningStatus(provisioningProp ? null : workspaceId)
  const provisioning = provisioningProp ?? ownProvisioning
  const provisioningCrew = provisioning.detail.find((d) => d.id === crew?.id)
  const [members, setMembers] = useState<CrewMemberRow[] | null>(null)
  const [iconPickerOpen, setIconPickerOpen] = useState(false)
  const [activityFilter, setActivityFilter] = useState<"all" | string>("all") // "all" | agentId

  // Reset to Overview when switching crews.
  const resetActivityFilter = useCallback(() => setActivityFilter("all"), [])
  useResetTabOnSlugChange<CrewTab>(crewSlug, setTab, "overview", resetActivityFilter)


  useEffect(() => {
    if (!crew) return
    let cancelled = false
    apiFetch(`/api/v1/crews/${crew.id}/integrations?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data: CrewIntegration[]) => {
        if (!cancelled && Array.isArray(data)) setIntegrations(data)
      })
      .catch(() => setIntegrations([]))
    return () => { cancelled = true }
  }, [crew, workspaceId])

  // Which bound credentials this crew cannot actually read: the image lacks
  // the CLI. GET /crews/{id}/credential-readiness answers per crew; a failed
  // call contributes nothing rather than a false "nothing missing".
  useEffect(() => {
    if (!crew) return
    let cancelled = false
    apiFetch(`/api/v1/crews/${crew.id}/credential-readiness?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((data: { gaps?: NeedGap[] } | null) => {
        if (!cancelled) setGaps(Array.isArray(data?.gaps) ? data.gaps : [])
      })
      .catch(() => { if (!cancelled) setGaps([]) })
    return () => { cancelled = true }
  }, [crew, workspaceId, provisioningCrew?.status])

  // Fetch workspace-user members lazily — only when the Roster tab is opened.
  useEffect(() => {
    if (!crew || tab !== "roster" || members !== null) return
    let cancelled = false
    apiFetch(`/api/v1/crews/${crew.id}/members?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data: CrewMemberRow[]) => {
        if (!cancelled && Array.isArray(data)) setMembers(data)
      })
      .catch(() => setMembers([]))
    return () => { cancelled = true }
  }, [crew, tab, workspaceId, members])

  const patch = usePatchEntity<CrewRecord>({
    workspaceId,
    entity: crew,
    patchUrl: (c) => `/api/v1/crews/${c.id}`,
    setEntity: setCrew,
    onChanged: onCrewChanged,
  })

  const applyAvatarStyle = useCallback(async (resetOverrides: boolean) => {
    if (!crew) return
    const verb = resetOverrides ? "Reset" : "Apply"
    if (!confirm(`${verb} avatar style "${crew.avatar_style ?? "robots"}" ${resetOverrides ? "and clear per-agent overrides" : ""} for all ${agentsForCrew.length} agents in ${crew.name}?`)) return
    try {
      const url = `/api/v1/crews/${crew.id}/apply-avatar-style?workspace_id=${workspaceId}${resetOverrides ? "&reset_overrides=true" : ""}`
      const res = await apiFetch(url, { method: "POST" })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      toast.success(`${verb} done for ${agentsForCrew.length} agent${agentsForCrew.length === 1 ? "" : "s"}`)
      onCrewChanged()
    } catch (err) {
      toast.error(`${verb} failed: ${err instanceof Error ? err.message : err}`)
    }
  }, [crew, agentsForCrew.length, onCrewChanged, workspaceId])

  const needs = useMemo(
    () => (crew ? deriveCrewNeeds({
      crewSlug: crew.slug,
      agents: agentsForCrew,
      provisioning: provisioningCrew?.status,
      provisioningError: provisioningCrew?.error,
      gaps,
      integrations: integrations ?? [],
    }) : []),
    [crew, agentsForCrew, provisioningCrew?.status, provisioningCrew?.error, gaps, integrations],
  )

  const triggerBuild = useCallback(async () => {
    if (!crew) return
    setNeedBusy("build")
    try {
      const r = await apiFetch(`/api/v1/crews/${crew.id}/provision?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "POST" })
      if (!r.ok) throw new Error(await r.text())
      toast.success(`Rebuilding ${crew.name}'s image — usually 30–90 s`)
    } catch (err) {
      toast.error(`Build did not start: ${err instanceof Error ? err.message : err}`)
    } finally {
      setNeedBusy(null)
    }
  }, [crew, workspaceId])

  // "Install gh": add the devcontainer feature the readiness check named,
  // then rebuild — the one click the audit asked for, instead of prose that
  // said "add the feature and rebuild".
  const installTool = useCallback(async (need: CrewNeed & { action: { kind: "install" } }) => {
    if (!crew) return
    setNeedBusy(need.id)
    try {
      await patch({ devcontainer_config: withDevcontainerFeature(crew.devcontainer_config, need.action.feature) })
      const r = await apiFetch(`/api/v1/crews/${crew.id}/provision?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "POST" })
      if (!r.ok) throw new Error(await r.text())
      toast.success(`Installing ${need.action.tool} — ${crew.name}'s image is rebuilding`)
    } catch (err) {
      toast.error(`Could not install ${need.action.tool}: ${err instanceof Error ? err.message : err}`)
    } finally {
      setNeedBusy(null)
    }
  }, [crew, patch, workspaceId])

  // The dialog says what is lost and where to recover (README §2); a crew
  // takes its container with it, so its slug is typed back first.
  const handleDelete = useCallback(async () => {
    if (!crew) return
    try {
      const res = await apiFetch(`/api/v1/crews/${crew.id}?workspace_id=${workspaceId}`, { method: "DELETE" })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      toast.success(`Crew "${crew.name}" deleted`)
      onCrewChanged()
    } catch (err) {
      toast.error(`Delete failed: ${err instanceof Error ? err.message : err}`)
      throw err
    }
  }, [crew, onCrewChanged, workspaceId])

  if (loading || error || !crew) {
    return (
      <CanvasShell
        loading={loading}
        error={loading ? null : (error ?? "crew not found")}
        notLoadedLabel="Could not load crew"
      >
        {null}
      </CanvasShell>
    )
  }


  return (
    <CanvasShell loading={false} error={null} notLoadedLabel="">
      {/* Header */}
      <header className="flex items-start gap-5 pb-5 border-b border-white/8">
        <button
          type="button"
          onClick={() => setIconPickerOpen(true)}
          title="Customize icon and color"
          className="shrink-0 group rounded-2xl transition-transform hover:scale-[1.03]"
        >
          <div className="relative">
            <CrewIcon icon={crew.icon || "briefcase"} color={crew.color} size="xl" />
            <span className="absolute inset-0 rounded-2xl ring-2 ring-primary/0 group-hover:ring-primary/40 transition-all pointer-events-none" />
          </div>
        </button>
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
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-1">
            <h1 className="text-2xl font-semibold">
              {crew.name}
            </h1>
            <span className="text-[11px] flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-muted text-foreground/80 border border-white/10">
              Crew
            </span>
            {/* #1380 — surface the effective isolation posture on the surface
                operators actually open, not only inside the Security tab. */}
            <CrewPrivilegedBadge devcontainerConfig={crew.devcontainer_config} />
          </div>
          <div className="flex items-center gap-2 text-sm text-muted-foreground flex-wrap">
            <code className="text-foreground/80 text-xs px-1.5 py-0.5 rounded bg-muted border border-white/8">
              {crew.slug}
            </code>
            {crew.issue_prefix && (
              <>
                <span className="text-muted-foreground-soft">·</span>
                <span className="text-xs">prefix <code className="font-mono uppercase text-foreground/80 px-1 py-0.5 rounded bg-muted border border-white/8">{crew.issue_prefix}</code></span>
              </>
            )}
            <span className="text-muted-foreground-soft">·</span>
            <span className="text-xs">Created {new Date(crew.created_at).toLocaleDateString()}</span>
          </div>
          {crew.description && <p className="text-sm text-muted-foreground mt-2 max-w-prose">{crew.description}</p>}
        </div>
        <div className="flex flex-wrap items-center gap-2 shrink-0">
          <Button variant="outline" size="sm" onClick={() => setEditOpen(true)}><Pencil /> Edit</Button>
          <Button variant="outline" size="icon-sm" aria-label="Crew administration" onClick={() => setAdminOpen(true)}><Settings2 /></Button>
          <button
            type="button"
            onClick={onOpenFiles}
            className="px-3 py-2 text-sm rounded-lg border border-white/10 hover:bg-white/5 flex items-center gap-2"
            title="Open files in bottom panel"
          >
            <Files className="h-3.5 w-3.5" />
            Files
          </button>
        </div>
      </header>

      <CrewNeedsYou
        needs={needs}
        busyId={needBusy === "build" ? needs.find((n) => n.action.kind === "build")?.id ?? null : needBusy}
        onBuild={triggerBuild}
        onInstall={installTool}
        inboxHref={entityHref({ kind: "inbox" })}
      />
      <ProvisioningBanner crewId={crew.id} crewSlug={crew.slug} workspaceId={workspaceId} needsOwnedByStrip provisioning={provisioning} />

      {/* Tabs */}
      <CanvasTabs<CrewTab> tabs={TABS} active={tab} onChange={setTab} idPrefix="crew-canvas" label="Crew sections" />

      <CanvasTabPanel idPrefix="crew-canvas" active={tab} className="space-y-6">
      {tab === "overview" && (
        <OverviewTab
          workspaceId={workspaceId}
          crewId={crew.id}
          crewSlug={crew.slug}
          crewName={crew.name}
          avatarStyle={crew.avatar_style}
          agentsForCrew={agentsForCrew}
          onSelectAgent={onSelectAgent}
          onOpenTeam={() => setTab("roster")}
          teamLoading={roster.loading}
          teamError={roster.error}
          activityFilter={activityFilter}
          setActivityFilter={setActivityFilter}
          onOpenFiles={onOpenFiles}
          applyAvatarStyle={applyAvatarStyle}
        />
      )}

      {tab === "roster" && (<>
        {roster.error && <p role="alert">Team could not be loaded. <button onClick={() => void roster.refresh()}>Retry</button></p>}
        {roster.loading && <p role="status">Loading team…</p>}
        <RosterTab
          crew={crew}
          agentsForCrew={agentsForCrew}
          members={members}
          onSelectAgent={onSelectAgent}
        />
        {roster.hasMore && <Button variant="outline" disabled={roster.loadingMore} onClick={() => void roster.loadMore()}>Load more agents</Button>}
      </>)}

      {tab === "missions" && <EntityWork workspaceId={workspaceId} crewId={crew.id} slug={crew.slug} name={crew.name} />}

      {tab === "memory" && <MemoryWorkspace key={crew.id} workspaceId={workspaceId} crewId={crew.id} />}
      </CanvasTabPanel>
      <CreateCrewDialog workspaceId={workspaceId} crew={crew} open={editOpen} onOpenChange={setEditOpen} onCreated={() => { onCrewChanged(); void fetchCrew() }} />
      <Dialog open={adminOpen} onOpenChange={setAdminOpen}><DialogContent className="sm:max-w-4xl max-h-[85vh] overflow-y-auto"><DialogHeader><DialogTitle>Crew administration</DialogTitle><DialogDescription>Access policies, integrations and container operations. Each control applies its own change.</DialogDescription></DialogHeader>
        <CrewRestartButton workspaceId={workspaceId} crewId={crew.id} name={crew.name} onRestart={onCrewChanged} />
        <SettingsTab workspaceId={workspaceId} crew={crew} agentsForCrew={agentsForCrew} integrations={integrations} patch={patch} applyAvatarStyle={applyAvatarStyle} onDelete={() => setConfirmDelete(true)} />
      </DialogContent></Dialog>
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete crew ${crew.name}?`}
        description="This tears down the crew container and detaches its agents. It cannot be undone."
        consequences={[
          { tone: "lost", text: `${agentsForCrew.length === 1 ? "1 agent becomes" : `${agentsForCrew.length} agents become`} unassigned — they keep their skills and chats` },
          { tone: "lost", text: "The container image and its files are removed" },
          { tone: "kept", text: "Issues and routines stay in the workspace, unowned" },
          { tone: "kept", text: "The journal is kept 30 days; a workspace backup restores the crew" },
        ]}
        confirmLabel="Delete crew"
        destructive
        typeToConfirm={crew.slug}
        onConfirm={handleDelete}
      />
    </CanvasShell>
  )
}
