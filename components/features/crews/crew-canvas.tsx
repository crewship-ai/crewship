"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { toast } from "sonner"
import { AlertTriangle, ChevronDown, Files, Loader2, Plus, RotateCcw, Trash2 } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { CrewIcon } from "@/components/ui/crew-icon"
import { EditableField } from "@/components/shared/editable-field"
import { CrewActivityFeed } from "@/components/features/crews/crew-activity-feed"
import { CrewIconPickerDialog } from "@/components/features/crews/crew-icon-picker-dialog"
import { CrewRuntimeConfig } from "@/components/features/crews/crew-runtime-config"
import { CrewContainerConfig } from "@/components/features/crews/crew-container-config"
import { CrewNetworkPolicy } from "@/components/features/crews/crew-network-policy"
import { CrewMCPConfig } from "@/components/features/crews/crew-mcp-config"
import { CrewEscalations } from "@/components/features/crews/crew-escalations"
import { AVATAR_STYLES, getAgentAvatarUrl } from "@/lib/agent-avatar"
import { fetchWithRetry } from "@/lib/fetch-with-retry"
import { cn } from "@/lib/utils"

interface AgentSummary {
  id: string
  name: string
  slug: string
  status: string
  role_title: string | null
  agent_role: string
  avatar_seed?: string | null
  avatar_style?: string | null
  llm_provider?: string | null
  llm_model?: string | null
  _count?: { skills: number; credentials: number }
}

interface CrewRecord {
  id: string
  workspace_id: string
  name: string
  slug: string
  description: string | null
  color: string | null
  icon: string | null
  avatar_style: string | null
  issue_prefix: string | null
  network_mode: string
  allowed_domains: string[] | string | null
  container_memory_mb: number
  container_cpus: number
  container_ttl_hours: number | null
  runtime_image: string | null
  devcontainer_config: string | null
  mise_config: string | null
  escalation_config: string | null
  cached_image: string | null
  created_at: string
  updated_at: string
  _count?: { agents: number; members: number }
}

interface MissionData {
  id: string
  title: string
  status: string
  crew_id: string
  created_at: string
}

interface IssuesSnapshot {
  Backlog: number
  Todo: number
  InProgress: number
  InReview: number
  Done: number
}

interface IssueRow {
  id: string
  identifier: string | null
  title: string
  status: string
  created_at?: string
}

interface CrewIntegration {
  id: string
  integration_id: string
  name: string
  type: string
  status: string
}

interface MemberUser {
  id: string
  email: string
  full_name: string | null
  avatar_url: string | null
}

interface CrewMemberRow {
  id: string
  crew_id: string
  user_id: string
  created_at: string
  user?: MemberUser | null
}

const STYLE_OPTIONS = (Object.entries(AVATAR_STYLES) as Array<[
  string,
  { label: string; style: unknown },
]>).map(([value, meta]) => ({ value, label: meta.label }))

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
  onAddAgent: (defaultCrewSlug: string) => void
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
  onAddAgent,
}: CrewCanvasProps) {
  const [crew, setCrew] = useState<CrewRecord | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState<CrewTab>("overview")
  const [issues, setIssues] = useState<IssuesSnapshot | null>(null)
  const [recentIssues, setRecentIssues] = useState<IssueRow[]>([])
  const [integrations, setIntegrations] = useState<CrewIntegration[] | null>(null)
  const [members, setMembers] = useState<CrewMemberRow[] | null>(null)
  const [iconPickerOpen, setIconPickerOpen] = useState(false)
  const [activityFilter, setActivityFilter] = useState<"all" | string>("all") // "all" | agentId

  // Reset to Overview when switching crews.
  const lastCrewSlug = useRef(crewSlug)
  useEffect(() => {
    if (lastCrewSlug.current !== crewSlug) {
      setTab("overview")
      setActivityFilter("all")
      lastCrewSlug.current = crewSlug
    }
  }, [crewSlug])

  const fetchCrew = useCallback(async (signal?: AbortSignal) => {
    try {
      const listRes = await fetchWithRetry(`/api/v1/crews?workspace_id=${workspaceId}`, { signal })
      if (!listRes.ok) throw new Error(`crew fetch failed (${listRes.status})`)
      const list: CrewRecord[] = await listRes.json()
      const match = list.find((c) => c.slug === crewSlug)
      if (!match) throw new Error("crew not found")
      const detailRes = await fetchWithRetry(`/api/v1/crews/${match.id}?workspace_id=${workspaceId}`, { signal })
      if (!detailRes.ok) throw new Error(`crew detail fetch failed (${detailRes.status})`)
      const detail: CrewRecord = await detailRes.json()
      if (!signal?.aborted) {
        setCrew(detail)
        setError(null)
      }
    } catch (err) {
      if ((err as { name?: string })?.name === "AbortError") return
      setError(err instanceof Error ? err.message : "Failed to load crew")
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [crewSlug, workspaceId])

  useEffect(() => {
    setLoading(true)
    const controller = new AbortController()
    void fetchCrew(controller.signal)
    return () => controller.abort()
  }, [crewSlug, fetchCrew])

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

  const patch = useCallback(async (body: Record<string, unknown>) => {
    if (!crew) return
    const res = await fetch(`/api/v1/crews/${crew.id}?workspace_id=${workspaceId}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    })
    if (!res.ok) {
      const text = await res.text()
      throw new Error(text || `HTTP ${res.status}`)
    }
    const updated: CrewRecord = await res.json()
    setCrew(updated)
    onCrewChanged()
  }, [crew, workspaceId, onCrewChanged])

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

  if (loading) {
    return <div className="px-6 md:px-8 lg:px-12 py-6 max-w-[1180px] mx-auto w-full"><Skeleton className="h-[600px] w-full rounded-xl" /></div>
  }
  if (error || !crew) {
    return (
      <div className="px-6 md:px-8 lg:px-12 py-12 max-w-[1180px] mx-auto w-full text-center">
        <p className="text-sm text-red-300 mb-2">Could not load crew</p>
        <p className="text-xs text-muted-foreground">{error}</p>
      </div>
    )
  }

  const containerSummary = `${crew.runtime_image ?? "debian:trixie-slim"} · ${formatMemory(crew.container_memory_mb)} · ${crew.container_cpus} CPU · TTL ${crew.container_ttl_hours ?? "—"}h · network: ${crew.network_mode}`

  return (
    <div className="px-6 md:px-8 lg:px-12 py-6 space-y-6 max-w-[1180px] mx-auto w-full">
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
              <EditableField value={crew.name} onSave={(v) => patch({ name: v })} />
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
          <button
            type="button"
            onClick={() => onAddAgent(crew.slug)}
            className="px-3.5 py-2 rounded-lg bg-blue-500 hover:bg-blue-400 text-white text-sm font-medium flex items-center gap-1.5"
          >
            <Plus className="h-3.5 w-3.5" />
            Add agent
          </button>
        </div>
      </header>

      <ProvisioningBanner crewId={crew.id} crewSlug={crew.slug} workspaceId={workspaceId} />

      {/* Tabs */}
      <div className="flex items-center gap-5 border-b border-white/8 -mx-6 md:-mx-8 lg:-mx-12 px-6 md:px-8 lg:px-12 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
        {TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            onClick={() => setTab(t.id)}
            aria-selected={tab === t.id}
            className={cn(
              "text-sm py-2 px-1 border-b-2 transition-colors shrink-0",
              tab === t.id
                ? "border-blue-400 text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground/80",
            )}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === "overview" && (
        <div className="space-y-7">
          {/* Health 3-card strip — derived stats, no extra fetches */}
          <section className="grid grid-cols-1 sm:grid-cols-3 gap-4">
            <HealthCard
              label="Agents"
              value={`${agentsForCrew.length}`}
              hint={
                health.errored > 0
                  ? `${health.errored} error${health.errored === 1 ? "" : "s"} · ${health.running} running`
                  : `${health.running} running · ${agentsForCrew.length - health.running} idle`
              }
              tone={health.errored > 0 ? "danger" : health.running > 0 ? "active" : "neutral"}
            />
            <HealthCard
              label="Open issues"
              value={health.openIssues !== null ? String(health.openIssues) : "–"}
              hint={
                issues
                  ? `${issues.InProgress} in progress · ${issues.InReview} in review`
                  : "loading…"
              }
              tone={(health.openIssues ?? 0) > 0 ? "active" : "neutral"}
              href="/orchestration"
            />
            <HealthCard
              label="Missions"
              value={`${health.activeMissions}`}
              hint={
                health.activeMissions > 0
                  ? "active missions running"
                  : "no active missions"
              }
              tone={health.activeMissions > 0 ? "active" : "neutral"}
            />
          </section>

          {/* Activity with per-agent filter chips */}
          <section className="space-y-3">
            <div className="flex items-baseline justify-between flex-wrap gap-2">
              <h2 className="text-lg font-semibold">Recent activity</h2>
              <div className="flex items-center gap-1.5 text-xs flex-wrap">
                <button
                  type="button"
                  onClick={() => setActivityFilter("all")}
                  aria-pressed={activityFilter === "all"}
                  className={cn(
                    "px-2 py-0.5 rounded border transition-colors",
                    activityFilter === "all"
                      ? "border-blue-500/45 bg-blue-500/15 text-blue-300"
                      : "border-white/10 text-muted-foreground hover:text-foreground/80",
                  )}
                >
                  All
                </button>
                {agentsForCrew.slice(0, 6).map((a) => (
                  <button
                    key={a.id}
                    type="button"
                    onClick={() => setActivityFilter(a.id)}
                    aria-pressed={activityFilter === a.id}
                    className={cn(
                      "px-2 py-0.5 rounded border transition-colors",
                      activityFilter === a.id
                        ? "border-blue-500/45 bg-blue-500/15 text-blue-300"
                        : "border-white/10 text-muted-foreground hover:text-foreground/80",
                    )}
                  >
                    {a.name}
                  </button>
                ))}
              </div>
            </div>
            <div className="rounded-xl border border-white/8 bg-card max-h-[420px] overflow-hidden">
              <CrewActivityFeed
                workspaceId={workspaceId}
                crewId={activityFilter === "all" ? crew.id : undefined}
                agentId={activityFilter === "all" ? undefined : activityFilter}
              />
            </div>
          </section>

          {/* Quick actions */}
          <section className="grid grid-cols-2 lg:grid-cols-4 gap-2">
            <QuickAction
              icon={<Files className="h-3.5 w-3.5" />}
              label="Open Files"
              onClick={onOpenFiles}
            />
            <QuickAction
              icon={<Plus className="h-3.5 w-3.5" />}
              label="Add agent"
              onClick={() => onAddAgent(crew.slug)}
            />
            <QuickAction
              icon={<RotateCcw className="h-3.5 w-3.5" />}
              label="Apply avatar style"
              onClick={() => applyAvatarStyle(false)}
              disabled={agentsForCrew.length === 0}
            />
            <QuickAction
              icon={<RotateCcw className="h-3.5 w-3.5" />}
              label="Reset avatar overrides"
              onClick={() => applyAvatarStyle(true)}
              disabled={agentsForCrew.length === 0}
            />
          </section>
        </div>
      )}

      {tab === "roster" && (
        <div className="space-y-7">
          {/* Agents */}
          <section className="space-y-3">
            <div className="flex items-baseline justify-between">
              <h2 className="text-lg font-semibold">
                Agents <span className="text-muted-foreground text-sm font-normal ml-1">{agentsForCrew.length}</span>
              </h2>
              <button
                type="button"
                onClick={() => onAddAgent(crew.slug)}
                className="text-xs px-2.5 py-1 rounded bg-blue-500 hover:bg-blue-400 text-white flex items-center gap-1.5"
              >
                <Plus className="h-3 w-3" />
                Add agent
              </button>
            </div>
            {agentsForCrew.length === 0 ? (
              <div className="rounded-xl border border-white/8 bg-card p-6 text-center text-xs text-muted-foreground">
                No agents in this crew. Click <strong className="text-foreground/80">Add agent</strong> to start.
              </div>
            ) : (
              <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                {agentsForCrew.map((a) => (
                  <button
                    key={a.id}
                    type="button"
                    onClick={() => onSelectAgent(a.slug)}
                    className="rounded-xl border border-white/8 bg-card p-3.5 text-left hover:border-white/15 transition-colors"
                  >
                    <div className="flex items-center gap-3">
                      <img
                        src={getAgentAvatarUrl(a.avatar_seed || a.name, a.avatar_style || crew.avatar_style)}
                        alt=""
                        className="w-10 h-10 rounded-xl"
                      />
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="font-medium truncate">{a.name}</span>
                          <span className="text-[10px] text-muted-foreground">{a.status?.toLowerCase()}</span>
                          {a.agent_role !== "AGENT" && (
                            <span className="text-[8px] px-1 rounded bg-violet-500/20 text-violet-300">{a.agent_role}</span>
                          )}
                        </div>
                        <div className="text-xs text-muted-foreground truncate">{a.role_title || "—"}</div>
                      </div>
                    </div>
                    <div className="flex items-center gap-3 mt-3 text-[11px] text-muted-foreground">
                      {a.llm_model && (
                        <span className="px-1.5 py-0.5 rounded bg-zinc-800 border border-white/10 truncate">
                          {a.llm_model}
                        </span>
                      )}
                      {a._count?.skills !== undefined && <span>{a._count.skills} skills</span>}
                      {a._count?.credentials !== undefined && <span>{a._count.credentials} keys</span>}
                    </div>
                  </button>
                ))}
              </div>
            )}
          </section>

          {/* Workspace users — humans with crew access (different from agents) */}
          <section className="space-y-3">
            <div className="flex items-baseline justify-between">
              <h2 className="text-lg font-semibold">
                Workspace users <span className="text-muted-foreground text-sm font-normal ml-1">{members?.length ?? 0}</span>
              </h2>
              <Link
                href="/settings?tab=members"
                className="text-xs px-2.5 py-1 rounded border border-white/10 hover:bg-white/5 text-foreground/80 flex items-center gap-1.5"
              >
                <Plus className="h-3 w-3" />
                Manage in settings
              </Link>
            </div>
            <div className="rounded-xl border border-white/8 bg-card overflow-hidden divide-y divide-white/5">
              {members === null ? (
                <div className="px-4 py-6 text-xs text-muted-foreground">Loading…</div>
              ) : members.length === 0 ? (
                <div className="px-4 py-6 text-xs text-muted-foreground italic">
                  No workspace users assigned yet. By default, OWNERs and ADMINs of the workspace already have full access — assign individual MEMBERs here to scope their reach to this crew only.
                </div>
              ) : (
                members.map((m) => (
                  <div key={m.id} className="px-4 py-2.5 flex items-center gap-3">
                    {m.user?.avatar_url ? (
                      <img src={m.user.avatar_url} alt="" className="w-8 h-8 rounded-full" />
                    ) : (
                      <div className="w-8 h-8 rounded-full bg-violet-600 grid place-items-center text-[11px]">
                        {(m.user?.full_name ?? m.user?.email ?? "?").slice(0, 2).toUpperCase()}
                      </div>
                    )}
                    <div className="flex-1 min-w-0">
                      <div className="text-sm text-foreground truncate">
                        {m.user?.full_name ?? m.user?.email ?? "Unknown user"}
                      </div>
                      <div className="text-[10px] text-muted-foreground truncate">
                        {m.user?.email}
                        {m.created_at && ` · joined ${new Date(m.created_at).toLocaleDateString()}`}
                      </div>
                    </div>
                  </div>
                ))
              )}
            </div>
          </section>
        </div>
      )}

      {tab === "missions" && (
        <div className="space-y-7">
          {/* Recent missions */}
          <section className="space-y-3">
            <div className="flex items-baseline justify-between">
              <h2 className="text-lg font-semibold">
                Recent missions
                {recentMissions.length > 0 && (
                  <span className="text-muted-foreground text-sm font-normal ml-2">{recentMissions.length}</span>
                )}
              </h2>
              <Link href="/orchestration" className="text-xs text-blue-300 hover:underline">
                Open in /orchestration →
              </Link>
            </div>
            {recentMissions.length === 0 ? (
              <div className="rounded-xl border border-white/8 bg-card p-6 text-center text-xs text-muted-foreground">
                No missions yet for this crew.
              </div>
            ) : (
              <ul className="rounded-xl border border-white/8 bg-card divide-y divide-white/5">
                {recentMissions.map((m) => (
                  <li key={m.id}>
                    <Link
                      href={`/missions/${encodeURIComponent(m.id)}/timeline`}
                      className="px-4 py-2 flex items-center gap-3 text-sm hover:bg-white/[0.03] transition-colors"
                    >
                      <span className={cn(
                        "w-1.5 h-1.5 rounded-full shrink-0",
                        m.status === "RUNNING" ? "bg-emerald-400" : m.status === "FAILED" ? "bg-red-500" : "bg-zinc-500",
                      )} />
                      <span className="truncate flex-1 text-foreground/85">{m.title}</span>
                      <span className="text-[10px] text-muted-foreground shrink-0 uppercase">
                        {m.status?.replace(/_/g, " ").toLowerCase()}
                      </span>
                      <span className="text-[10px] text-muted-foreground shrink-0">
                        {new Date(m.created_at).toLocaleDateString()}
                      </span>
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </section>

          {/* Issues */}
          <section className="space-y-3">
            <div className="flex items-baseline justify-between">
              <h2 className="text-lg font-semibold">
                Issues
                {crew.issue_prefix && (
                  <span className="text-muted-foreground text-sm font-normal ml-2 font-mono uppercase">{crew.issue_prefix}</span>
                )}
              </h2>
              <Link href="/orchestration" className="text-xs text-blue-300 hover:underline">
                Open in /orchestration →
              </Link>
            </div>
            <div className="rounded-xl border border-white/8 bg-card grid grid-cols-5 divide-x divide-white/5">
              {(["Backlog", "Todo", "InProgress", "InReview", "Done"] as const).map((bucket) => (
                <div key={bucket} className="px-4 py-3">
                  <div className="text-[10px] text-muted-foreground uppercase">{bucket.replace(/([A-Z])/g, " $1").trim()}</div>
                  <div className={cn("text-2xl font-semibold mt-1", issues?.[bucket] ? "text-foreground" : "text-muted-foreground")}>
                    {issues?.[bucket] ?? "—"}
                  </div>
                </div>
              ))}
            </div>
            {recentIssues.length > 0 && (
              <ul className="rounded-xl border border-white/8 bg-card divide-y divide-white/5">
                {recentIssues.map((i) => (
                  <li key={i.id}>
                    <Link
                      href={i.identifier ? `/orchestration/issues/${encodeURIComponent(i.identifier)}` : "/orchestration"}
                      className="px-4 py-2 flex items-center gap-3 text-sm hover:bg-white/[0.03] transition-colors"
                    >
                      <span className={cn(
                        "w-1.5 h-1.5 rounded-full shrink-0",
                        issueStatusColor(i.status),
                      )} />
                      {i.identifier && (
                        <code className="text-[11px] text-muted-foreground shrink-0 font-mono">
                          {i.identifier}
                        </code>
                      )}
                      <span className="truncate flex-1 text-foreground/85">{i.title}</span>
                      <span className="text-[10px] text-muted-foreground shrink-0 uppercase">
                        {i.status?.replace(/_/g, " ").toLowerCase()}
                      </span>
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </div>
      )}

      {tab === "files" && (
        <div className="space-y-4">
          <div className="flex items-baseline justify-between">
            <h2 className="text-lg font-semibold">Crew files</h2>
            <span className="text-xs text-muted-foreground">shared at <code className="text-foreground/80">/crew/shared</code></span>
          </div>
          <div className="rounded-xl border border-white/8 bg-card p-6 flex items-center gap-4">
            <div className="flex-1">
              <div className="text-sm font-medium">Crew-wide shared files</div>
              <div className="text-xs text-muted-foreground mt-1">
                Browse and edit files in <code className="text-foreground/80">/crew/shared</code>. All agents in this crew read from the same tree —
                use it for runbooks, policies, and templates that should be visible to every agent.
              </div>
            </div>
            <button
              type="button"
              onClick={onOpenFiles}
              className="text-sm px-3 py-2 rounded-lg bg-blue-500 hover:bg-blue-400 text-white"
            >
              Open Files panel
            </button>
          </div>
        </div>
      )}

      {tab === "settings" && (
        <div className="space-y-7">
          {/* Profile */}
          <section className="space-y-3">
            <h2 className="text-lg font-semibold">Profile</h2>
            <div className="rounded-xl border border-white/8 bg-card divide-y divide-white/5">
              <Row label="Name">
                <EditableField value={crew.name} onSave={(v) => patch({ name: v })} />
              </Row>
              <Row label="Slug">
                <EditableField value={crew.slug} onSave={(v) => patch({ slug: v })} mono />
              </Row>
              <Row label="Description" align="start">
                <EditableField value={crew.description} onSave={(v) => patch({ description: v })} />
              </Row>
              <Row label="Issue prefix">
                <EditableField
                  value={crew.issue_prefix ?? ""}
                  onSave={(v) => patch({ issue_prefix: (v || null) && v.toUpperCase().slice(0, 5) })}
                  mono
                  placeholder="ENG"
                />
                <span className="text-[10px] text-muted-foreground ml-1">max 5 · uppercase</span>
              </Row>
              <Row label="Avatar style">
                <div className="flex items-center gap-2 flex-wrap">
                  <EditableField
                    value={crew.avatar_style ?? "bottts-neutral"}
                    onSave={(v) => patch({ avatar_style: v })}
                    options={STYLE_OPTIONS}
                    format={(v) => STYLE_OPTIONS.find((o) => o.value === v)?.label ?? v}
                  />
                  {agentsForCrew.length > 0 && (
                    <>
                      <button
                        type="button"
                        onClick={() => applyAvatarStyle(false)}
                        className="text-[10px] px-2 py-0.5 rounded border border-white/10 text-foreground/80 hover:bg-white/5"
                      >
                        Apply to all
                      </button>
                      <button
                        type="button"
                        onClick={() => applyAvatarStyle(true)}
                        className="text-[10px] px-2 py-0.5 rounded border border-white/10 text-foreground/80 hover:bg-white/5"
                        title="Apply this style and clear per-agent overrides"
                      >
                        Reset overrides
                      </button>
                    </>
                  )}
                </div>
              </Row>
            </div>
          </section>

          {/* Runtime &amp; security — collapsibles per wireframe spec */}
          <section className="space-y-3">
            <h2 className="text-lg font-semibold">Runtime &amp; security</h2>
            <Collapsible
              title="Container resources"
              summary={`${formatMemory(crew.container_memory_mb)} · ${crew.container_cpus} CPU · TTL ${crew.container_ttl_hours ?? "—"}h`}
            >
              <CrewContainerConfig
                memoryMb={crew.container_memory_mb}
                cpus={crew.container_cpus}
                ttlHours={crew.container_ttl_hours}
                canEdit
                onSave={async (config) => { await patch(config) }}
              />
            </Collapsible>

            <Collapsible
              title="Network policy"
              summary={`${crew.network_mode}${Array.isArray(crew.allowed_domains) && crew.allowed_domains.length > 0 ? ` · ${crew.allowed_domains.length} allowed` : ""}`}
            >
              <CrewNetworkPolicy
                networkMode={crew.network_mode === "restricted" ? "restricted" : "free"}
                allowedDomains={Array.isArray(crew.allowed_domains)
                  ? crew.allowed_domains
                  : (crew.allowed_domains ? String(crew.allowed_domains).split(",").map((s) => s.trim()).filter(Boolean) : [])}
                canEdit
                onSave={async (mode, domains) => {
                  await patch({ network_mode: mode, allowed_domains: domains.length > 0 ? domains : null })
                }}
              />
            </Collapsible>

            <Collapsible
              title="MCP servers"
              summary="crew-wide model context protocol servers"
            >
              <CrewMCPConfig crewId={crew.id} workspaceId={workspaceId} />
            </Collapsible>

            <Collapsible
              title="Container image &amp; features"
              summary={crew.runtime_image ?? "debian:trixie-slim"}
            >
              <CrewRuntimeConfig
                crewId={crew.id}
                workspaceId={workspaceId}
                runtimeImage={crew.runtime_image}
                devcontainerConfig={crew.devcontainer_config}
                miseConfig={crew.mise_config}
                cachedImage={crew.cached_image}
                canEdit
                onSave={async (config) => { await patch(config) }}
              />
            </Collapsible>

            <Collapsible
              title="Escalations"
              summary="harbormaster sync · deny on miss"
            >
              <CrewEscalations crewId={crew.id} workspaceId={workspaceId} />
            </Collapsible>
          </section>

          {/* Integrations */}
          <section className="space-y-3">
            <div className="flex items-baseline justify-between">
              <h2 className="text-lg font-semibold">
                Integrations
                <span className="text-muted-foreground text-sm font-normal ml-1">{integrations?.length ?? 0}</span>
              </h2>
              <Link href="/integrations" className="text-xs text-blue-300 hover:underline">
                Manage workspace integrations →
              </Link>
            </div>
            {!integrations || integrations.length === 0 ? (
              <div className="rounded-xl border border-white/8 bg-card p-4 text-xs text-muted-foreground">
                No integrations bound to this crew.
              </div>
            ) : (
              <div className="rounded-xl border border-white/8 bg-card divide-y divide-white/5">
                {integrations.map((i) => (
                  <div key={i.id} className="px-4 py-2.5 flex items-center gap-3">
                    <div className="w-7 h-7 rounded bg-violet-500/20 text-violet-300 grid place-items-center text-xs font-semibold">
                      {i.name.charAt(0).toUpperCase()}
                    </div>
                    <div className="flex-1">
                      <div className="text-sm">{i.name}</div>
                      <div className="text-[11px] text-muted-foreground">{i.type}</div>
                    </div>
                    <span className={cn(
                      "text-[10px]",
                      i.status === "connected" ? "text-emerald-400" : "text-muted-foreground",
                    )}>
                      {i.status}
                    </span>
                  </div>
                ))}
              </div>
            )}
          </section>

          {/* Danger */}
          <section className="space-y-3">
            <h2 className="text-lg font-semibold text-red-400">Danger zone</h2>
            <div className="rounded-xl border border-red-500/30 bg-red-500/5 p-4 flex items-center justify-between">
              <div>
                <div className="text-sm font-medium">Delete this crew</div>
                <div className="text-xs text-muted-foreground">
                  All {agentsForCrew.length} agent{agentsForCrew.length === 1 ? "" : "s"} will be detached. Container torn down. Journal kept 30 days.
                </div>
              </div>
              <button
                type="button"
                onClick={handleDelete}
                className="text-xs px-3 py-1.5 rounded bg-red-500/20 text-red-300 border border-red-500/40 hover:bg-red-500/30 flex items-center gap-1.5"
              >
                <Trash2 className="h-3 w-3" />
                Delete {crew.name}
              </button>
            </div>
          </section>
        </div>
      )}
    </div>
  )
}

// =============================================================================
// Layout helpers
// =============================================================================

function HealthCard({ label, value, hint, tone, href }: {
  label: string
  value: string
  hint: string
  tone: "active" | "neutral" | "danger"
  href?: string
}) {
  const inner = (
    <div
      className={cn(
        "rounded-xl border bg-card p-4 transition-colors",
        tone === "danger" ? "border-red-500/30 ring-1 ring-red-500/20" :
        tone === "active" ? "border-white/10" : "border-white/8",
        href && "hover:border-white/20",
      )}
    >
      <div className="flex items-center justify-between mb-2">
        <span className="text-xs text-muted-foreground uppercase tracking-wide">{label}</span>
        {tone === "danger" && <span className="text-[10px] text-red-300">action needed</span>}
      </div>
      <div className={cn(
        "text-2xl font-semibold mb-1 tabular-nums",
        tone === "danger" ? "text-red-200" : "text-foreground",
      )}>
        {value}
      </div>
      <div className="text-[11px] text-muted-foreground">{hint}</div>
    </div>
  )
  return href ? <Link href={href}>{inner}</Link> : inner
}

function QuickAction({ icon, label, onClick, disabled }: {
  icon: React.ReactNode
  label: string
  onClick: () => void
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className="rounded-lg border border-white/8 bg-card px-3 py-2.5 flex items-center gap-2.5 text-left hover:border-white/15 hover:bg-white/[0.02] disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
    >
      <span className="text-foreground/70">{icon}</span>
      <span className="text-xs text-foreground/85">{label}</span>
    </button>
  )
}

/**
 * Surfaces three states the user otherwise can't see until they hit
 * "send message" and get a backend error:
 *   - "needs_provision": user edited devcontainer/runtime config and saved.
 *     The PATCH cleared cached_image; a chat now would 500 with
 *     "Crew has devcontainer configuration but no provisioned image".
 *     Show an amber banner with a Provision button.
 *   - "running": polled job is mid-build. Show progress + ETA-ish hint.
 *   - "failed": the last build crashed. Show the error inline so the user
 *     sees WHY (e.g. a feature with a missing required parameter), not
 *     a generic toast.
 *
 * Polls every 3s while busy, every 30s when idle. Bails as soon as a
 * stable terminal state is reached.
 */
function ProvisioningBanner({ crewId, crewSlug, workspaceId }: { crewId: string; crewSlug: string; workspaceId: string }) {
  const [state, setState] = useState<{ status: string; error?: string; cached?: string | null; hasConfig: boolean } | null>(null)
  const [triggering, setTriggering] = useState(false)

  const refresh = useCallback(async () => {
    try {
      // wsCtx middleware mandates workspace_id; without it the endpoint
      // 400s and the polling loop re-renders forever.
      const r = await fetch(`/api/v1/crews/${crewId}/provision?workspace_id=${encodeURIComponent(workspaceId)}`)
      if (!r.ok) return
      const data = await r.json()
      setState({
        status: data.status ?? "idle",
        error: data.error,
        cached: data.cached_image,
        hasConfig: Boolean(data.devcontainer_config),
      })
    } catch { /* tolerate */ }
  }, [crewId, workspaceId])

  useEffect(() => { void refresh() }, [refresh])

  // Poll fast while a build is in flight, slowly when idle/healthy.
  useEffect(() => {
    const isBusy = state?.status === "running"
    const interval = isBusy ? 3000 : 30000
    const id = setInterval(() => { void refresh() }, interval)
    return () => clearInterval(id)
  }, [state?.status, refresh])

  const trigger = useCallback(async () => {
    setTriggering(true)
    try {
      const r = await fetch(`/api/v1/crews/${crewId}/provision?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "POST" })
      if (!r.ok) {
        const text = await r.text()
        toast.error(`Provision failed to start: ${text}`)
      } else {
        toast.success(`Provisioning started for ${crewSlug}`)
        void refresh()
      }
    } catch (err) {
      toast.error(`Provision failed: ${err instanceof Error ? err.message : err}`)
    } finally {
      setTriggering(false)
    }
  }, [crewId, crewSlug, workspaceId, refresh])

  if (!state) return null

  const needsProvision = state.hasConfig && !state.cached && state.status === "idle"
  if (state.status === "completed" || (!needsProvision && state.status !== "running" && state.status !== "failed")) {
    return null
  }

  if (state.status === "running") {
    return (
      <div className="rounded-xl border border-blue-500/30 bg-blue-500/5 px-4 py-3 flex items-center gap-3">
        <Loader2 className="h-4 w-4 text-blue-300 animate-spin shrink-0" />
        <div className="flex-1">
          <div className="text-sm text-blue-200">Building container image…</div>
          <div className="text-xs text-muted-foreground">
            Devcontainer features are installing. Agents in this crew will become runnable as soon as the image is ready (usually 30-90 s).
          </div>
        </div>
      </div>
    )
  }

  if (state.status === "failed") {
    return (
      <div className="rounded-xl border border-red-500/40 bg-red-500/5 px-4 py-3 flex items-start gap-3">
        <AlertTriangle className="h-4 w-4 text-red-300 shrink-0 mt-0.5" />
        <div className="flex-1 min-w-0">
          <div className="text-sm text-red-200">Last provision failed</div>
          {state.error && (
            <pre className="text-[11px] text-muted-foreground mt-1 whitespace-pre-wrap font-mono break-words max-h-24 overflow-y-auto">
              {state.error}
            </pre>
          )}
          <div className="text-xs text-muted-foreground mt-1.5">
            Fix the runtime config (Settings → Container image &amp; features) and try again.
          </div>
        </div>
        <button
          type="button"
          onClick={trigger}
          disabled={triggering}
          className="text-xs px-2.5 py-1.5 rounded bg-red-500/20 hover:bg-red-500/30 text-red-200 border border-red-500/40 shrink-0"
        >
          {triggering ? "Starting…" : "Retry"}
        </button>
      </div>
    )
  }

  // needs_provision (idle, hasConfig, no cached_image)
  return (
    <div className="rounded-xl border border-amber-500/40 bg-amber-500/5 px-4 py-3 flex items-center gap-3">
      <AlertTriangle className="h-4 w-4 text-amber-300 shrink-0" />
      <div className="flex-1 min-w-0">
        <div className="text-sm text-amber-200">Container image is out of date</div>
        <div className="text-xs text-muted-foreground">
          You changed the runtime config — agents in this crew can&apos;t start until the image is rebuilt. The image isn&apos;t auto-rebuilt on save (it&apos;s expensive); trigger it explicitly when you&apos;re ready.
        </div>
      </div>
      <button
        type="button"
        onClick={trigger}
        disabled={triggering}
        className="text-xs px-2.5 py-1.5 rounded bg-amber-500/25 hover:bg-amber-500/35 text-amber-200 border border-amber-500/40 shrink-0"
      >
        {triggering ? "Starting…" : "Provision now"}
      </button>
    </div>
  )
}

function Collapsible({ title, summary, children }: {
  title: string
  summary: string
  children: React.ReactNode
}) {
  return (
    <details className="rounded-xl border border-white/8 bg-card overflow-hidden group">
      <summary className="px-4 py-3 flex items-center gap-2 text-sm cursor-pointer hover:bg-white/[0.02] list-none">
        <ChevronDown className="h-3 w-3 text-muted-foreground transition-transform group-open:rotate-0 -rotate-90" />
        <span className="text-foreground font-medium">{title}</span>
        <span className="text-xs text-muted-foreground truncate">{summary}</span>
      </summary>
      <div className="px-4 py-3 border-t border-white/5">
        {children}
      </div>
    </details>
  )
}

function formatMemory(mb: number): string {
  if (!Number.isFinite(mb) || mb <= 0) return "—"
  if (mb < 1024) return `${mb} MB`
  const gb = mb / 1024
  return gb >= 10 ? `${gb.toFixed(0)} GB` : `${gb.toFixed(1)} GB`
}

function issueStatusColor(status: string | undefined): string {
  const s = (status ?? "").toLowerCase()
  if (s.includes("progress")) return "bg-blue-400"
  if (s.includes("review")) return "bg-amber-400"
  if (s.includes("done") || s.includes("closed") || s.includes("complete")) return "bg-emerald-400"
  if (s.includes("blocked") || s.includes("error") || s.includes("cancel")) return "bg-red-500"
  if (s.includes("todo")) return "bg-zinc-400"
  return "bg-zinc-600"
}

function Row({
  label,
  align = "center",
  children,
}: {
  label: string
  align?: "center" | "start"
  children: React.ReactNode
}) {
  return (
    <div className={cn(
      "grid grid-cols-[180px_1fr] gap-4 px-4 py-2.5",
      align === "center" ? "items-center" : "items-start",
    )}>
      <span className="text-xs text-muted-foreground">{label}</span>
      <div className="flex items-center gap-2 min-w-0">{children}</div>
    </div>
  )
}
