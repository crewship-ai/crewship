"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { toast } from "sonner"
import { Files } from "lucide-react"
import { CrewIcon } from "@/components/ui/crew-icon"
import { EditableField } from "@/components/shared/editable-field"
import { CrewIconPickerDialog } from "@/components/features/crews/crew-icon-picker-dialog"

import { ProvisioningBanner } from "./crew-canvas-banner"
import {
  CanvasShell,
  CanvasTabs,
  useEntityFetch,
  usePatchEntity,
  useResetTabOnSlugChange,
} from "./canvas-base"
import { OverviewTab } from "./crew-canvas-tabs/overview-tab"
import { RosterTab } from "./crew-canvas-tabs/roster-tab"
import { MissionsTab } from "./crew-canvas-tabs/missions-tab"
import { FilesTab } from "./crew-canvas-tabs/files-tab"
import { SettingsTab } from "./crew-canvas-tabs/settings-tab"
import type {
  AgentSummary,
  CrewIntegration,
  CrewMemberRow,
  CrewRecord,
  IssueRow,
  IssuesSnapshot,
  MissionData,
} from "./crew-canvas-tabs/types"
import { formatMemory } from "./crew-canvas-tabs/types"


type CrewTab = "overview" | "roster" | "missions" | "files" | "settings"

const TABS: Array<{ id: CrewTab; label: string }> = [
  { id: "overview", label: "Overview" },
  { id: "roster", label: "Roster" },
  { id: "missions", label: "Missions" },
  { id: "files", label: "Files" },
  { id: "settings", label: "Settings" },
]


export interface CrewCanvasProps {
  workspaceId: string
  crewSlug: string
  agentsForCrew: AgentSummary[]
  missions: MissionData[]
  onCrewChanged: () => void
  onSelectAgent: (slug: string) => void
  onOpenFiles: () => void
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
  agentsForCrew,
  missions,
  onCrewChanged,
  onSelectAgent,
  onOpenFiles,
}: CrewCanvasProps) {
  const {
    entity: crew,
    setEntity: setCrew,
    loading,
    error,
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

  const [tab, setTab] = useState<CrewTab>("overview")
  const [issues, setIssues] = useState<IssuesSnapshot | null>(null)
  const [recentIssues, setRecentIssues] = useState<IssueRow[]>([])
  const [integrations, setIntegrations] = useState<CrewIntegration[] | null>(null)
  const [members, setMembers] = useState<CrewMemberRow[] | null>(null)
  const [iconPickerOpen, setIconPickerOpen] = useState(false)
  const [activityFilter, setActivityFilter] = useState<"all" | string>("all") // "all" | agentId

  // Reset to Overview when switching crews.
  const resetActivityFilter = useCallback(() => setActivityFilter("all"), [])
  useResetTabOnSlugChange<CrewTab>(crewSlug, setTab, "overview", resetActivityFilter)

  useEffect(() => {
    if (!crew) return
    let cancelled = false
    // The crew-scoped path only has POST registered; GET lives at the
    // workspace-scoped /issues endpoint with crew_id as filter.
    fetch(`/api/v1/issues?workspace_id=${encodeURIComponent(workspaceId)}&crew_id=${encodeURIComponent(crew.id)}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data: IssueRow[]) => {
        if (cancelled || !Array.isArray(data)) return
        const buckets: IssuesSnapshot = { Backlog: 0, Todo: 0, InProgress: 0, InReview: 0, Done: 0 }
        for (const i of data) {
          const s = i.status?.toLowerCase() ?? ""
          if (s.includes("backlog")) buckets.Backlog++
          else if (s.includes("todo")) buckets.Todo++
          else if (s.includes("progress")) buckets.InProgress++
          else if (s.includes("review")) buckets.InReview++
          else if (s.includes("done") || s.includes("closed")) buckets.Done++
        }
        setIssues(buckets)
        const sorted = [...data].sort((a, b) => (b.created_at ?? "").localeCompare(a.created_at ?? ""))
        setRecentIssues(sorted.slice(0, 10))
      })
      .catch(() => {
        setIssues({ Backlog: 0, Todo: 0, InProgress: 0, InReview: 0, Done: 0 })
        setRecentIssues([])
      })
    return () => { cancelled = true }
  }, [crew, workspaceId])

  useEffect(() => {
    if (!crew) return
    let cancelled = false
    fetch(`/api/v1/crews/${crew.id}/integrations?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data: CrewIntegration[]) => {
        if (!cancelled && Array.isArray(data)) setIntegrations(data)
      })
      .catch(() => setIntegrations([]))
    return () => { cancelled = true }
  }, [crew, workspaceId])

  // Fetch workspace-user members lazily — only when the Roster tab is opened.
  useEffect(() => {
    if (!crew || tab !== "roster" || members !== null) return
    let cancelled = false
    fetch(`/api/v1/crews/${crew.id}/members?workspace_id=${workspaceId}`)
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
      const res = await fetch(url, { method: "POST" })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      toast.success(`${verb} done for ${agentsForCrew.length} agent${agentsForCrew.length === 1 ? "" : "s"}`)
      onCrewChanged()
    } catch (err) {
      toast.error(`${verb} failed: ${err instanceof Error ? err.message : err}`)
    }
  }, [crew, agentsForCrew.length, onCrewChanged, workspaceId])

  const handleDelete = useCallback(async () => {
    if (!crew) return
    if (!confirm(`Delete crew "${crew.name}"? All ${agentsForCrew.length} agents will be detached. Container will be torn down. Journal kept 30 days.`)) return
    try {
      const res = await fetch(`/api/v1/crews/${crew.id}?workspace_id=${workspaceId}`, { method: "DELETE" })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      toast.success(`Crew "${crew.name}" deleted`)
      onCrewChanged()
    } catch (err) {
      toast.error(`Delete failed: ${err instanceof Error ? err.message : err}`)
    }
  }, [crew, agentsForCrew.length, onCrewChanged, workspaceId])

  const recentMissions = useMemo(() => {
    if (!crew) return []
    return [...missions]
      .filter((m) => m.crew_id === crew.id)
      .sort((a, b) => (b.created_at ?? "").localeCompare(a.created_at ?? ""))
      .slice(0, 10)
  }, [missions, crew])

  // Health snapshot — derived from data we already have, no extra fetches.
  const health = useMemo(() => {
    const running = agentsForCrew.filter((a) => a.status === "RUNNING").length
    const errored = agentsForCrew.filter((a) => a.status === "ERROR").length
    const openIssues = issues
      ? issues.Backlog + issues.Todo + issues.InProgress + issues.InReview
      : null
    const activeMissions = missions.filter((m) => m.crew_id === crew?.id && (m.status === "RUNNING" || m.status === "PENDING")).length
    return { running, errored, openIssues, activeMissions }
  }, [agentsForCrew, issues, missions, crew])

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

  const containerSummary = `${crew.runtime_image ?? "debian:trixie-slim"} · ${formatMemory(crew.container_memory_mb)} · ${crew.container_cpus} CPU · TTL ${crew.container_ttl_hours ?? "—"}h · network: ${crew.network_mode}`

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
            <span className="absolute inset-0 rounded-2xl ring-2 ring-blue-400/0 group-hover:ring-blue-400/40 transition-all pointer-events-none" />
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
              <EditableField value={crew.name} onSave={(v) => patch({ name: v })} ariaLabel="Crew name" />
            </h1>
            <span className="text-[11px] flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-zinc-800 text-foreground/80 border border-white/10">
              Crew
            </span>
          </div>
          <div className="flex items-center gap-2 text-sm text-muted-foreground flex-wrap">
            <code className="text-foreground/80 text-xs px-1.5 py-0.5 rounded bg-zinc-900 border border-white/8">
              {crew.slug}
            </code>
            {crew.issue_prefix && (
              <>
                <span className="text-muted-foreground/50">·</span>
                <span className="text-xs">prefix <code className="font-mono uppercase text-foreground/80 px-1 py-0.5 rounded bg-zinc-900 border border-white/8">{crew.issue_prefix}</code></span>
              </>
            )}
            <span className="text-muted-foreground/50">·</span>
            <span className="text-xs">Created {new Date(crew.created_at).toLocaleDateString()}</span>
          </div>
          <div className="text-xs text-muted-foreground mt-1.5 flex items-center gap-3 flex-wrap">
            <span><span className="text-foreground/80">{crew._count?.agents ?? agentsForCrew.length}</span> agents</span>
            <span><span className="text-foreground/80">{crew._count?.members ?? 0}</span> member{crew._count?.members === 1 ? "" : "s"}</span>
            <span><span className="text-foreground/80">{recentMissions.length}</span> missions</span>
            <span className="text-muted-foreground/50">·</span>
            <span className="truncate">container: <span className="text-foreground/80">{containerSummary}</span></span>
          </div>
        </div>
        <div className="flex items-center gap-2 shrink-0">
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

      <ProvisioningBanner crewId={crew.id} crewSlug={crew.slug} workspaceId={workspaceId} />

      {/* Tabs */}
      <CanvasTabs<CrewTab> tabs={TABS} active={tab} onChange={setTab} />

      {tab === "overview" && (
        <OverviewTab
          workspaceId={workspaceId}
          crewId={crew.id}
          agentsForCrew={agentsForCrew}
          missions={missions}
          issues={issues}
          health={health}
          activityFilter={activityFilter}
          setActivityFilter={setActivityFilter}
          onOpenFiles={onOpenFiles}
          applyAvatarStyle={applyAvatarStyle}
        />
      )}

      {tab === "roster" && (
        <RosterTab
          crew={crew}
          agentsForCrew={agentsForCrew}
          members={members}
          onSelectAgent={onSelectAgent}
        />
      )}

      {tab === "missions" && (
        <MissionsTab
          crew={crew}
          recentMissions={recentMissions}
          issues={issues}
          recentIssues={recentIssues}
        />
      )}

      {tab === "files" && <FilesTab onOpenFiles={onOpenFiles} />}

      {tab === "settings" && (
        <SettingsTab
          workspaceId={workspaceId}
          crew={crew}
          agentsForCrew={agentsForCrew}
          integrations={integrations}
          patch={patch}
          applyAvatarStyle={applyAvatarStyle}
          onDelete={handleDelete}
        />
      )}
    </CanvasShell>
  )
}
