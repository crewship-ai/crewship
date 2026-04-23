"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { useParams, useRouter, useSearchParams } from "next/navigation"
import {
  ArrowLeft, AlertTriangle, Paintbrush, RefreshCw, RotateCcw, Loader2, ChevronDown, Settings2, FolderOpen, TerminalSquare, Blocks,
  LayoutDashboard, Users, Share2, Boxes, ScrollText, Settings as SettingsIcon,
} from "lucide-react"
import { ToolbarStrip, type ToolbarTab } from "@/components/layout/toolbar-strip"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent } from "@/components/ui/card"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import { Skeleton } from "@/components/ui/skeleton"
import { STATUS_BG_LIGHT } from "@/lib/colors"
import { CrewIconPopover } from "@/components/crew-icon-popover"
import { CrewStats } from "@/components/features/crews/crew-stats"
import { CrewAgents } from "@/components/features/crews/crew-agents"
import { CrewMembers } from "@/components/features/crews/crew-members"
import { CrewMissions } from "@/components/features/crews/crew-missions"
import { CrewAssignments } from "@/components/features/crews/crew-assignments"
import { CrewPeerConversations } from "@/components/features/crews/crew-peer-conversations"
import { CrewEscalations } from "@/components/features/crews/crew-escalations"
import { CrewJournal } from "@/components/features/crews/crew-journal"
import { CrewDangerZone } from "@/components/features/crews/crew-danger-zone"
import { CrewNetworkPolicy } from "@/components/features/crews/crew-network-policy"
import { CrewContainerConfig } from "@/components/features/crews/crew-container-config"
import { CrewRuntimeConfig } from "@/components/features/crews/crew-runtime-config"
import { CrewMCPConfig } from "@/components/features/crews/crew-mcp-config"
import { AvatarPicker } from "@/components/avatar-picker"
import { AVATAR_STYLES } from "@/lib/agent-avatar"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import type { CrewMember } from "@/lib/types/crew"
import { toast } from "sonner"
import dynamic from "next/dynamic"
import Link from "next/link"

const WebTerminal = dynamic(
  () => import("@/components/features/terminal/web-terminal").then((m) => m.WebTerminal),
  { ssr: false }
)

interface Crew {
  id: string
  name: string
  slug: string
  description: string | null
  color: string | null
  icon: string | null
  avatar_style: string | null
  container_ttl_hours: number | null
  container_memory_mb: number
  container_cpus: number
  network_mode: string
  allowed_domains: string[]
  runtime_image: string | null
  devcontainer_config: string | null
  mise_config: string | null
  cached_image: string | null
  issue_prefix: string | null
  created_at: string
  _count: { agents: number; members: number }
}

interface Agent {
  id: string
  name: string
  slug: string
  description: string | null
  role_title: string | null
  agent_role: string
  status: string
  cli_adapter: string
  llm_provider: string
  llm_model: string
  crew: { name: string; slug: string; color: string | null } | null
  _count: { skills: number; credentials: number; chats: number }
}

type CrewTab = "overview" | "members" | "network" | "runtime" | "journal" | "settings"

const CREW_TABS: ToolbarTab<CrewTab>[] = [
  { id: "overview", label: "Overview", icon: LayoutDashboard },
  { id: "members", label: "Members", icon: Users },
  { id: "network", label: "Network", icon: Share2 },
  { id: "runtime", label: "Runtime", icon: Boxes },
  { id: "journal", label: "Journal", icon: ScrollText },
  { id: "settings", label: "Settings", icon: SettingsIcon },
]

function parseCrewTab(value: string | null): CrewTab {
  switch (value) {
    case "members":
    case "network":
    case "runtime":
    case "journal":
    case "settings":
      return value
    default:
      return "overview"
  }
}

export default function CrewDetailPage() {
  const params = useParams<{ crewId: string }>()
  const router = useRouter()
  const searchParams = useSearchParams()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { abilities } = useAbilities()

  const activeTab = useMemo(() => parseCrewTab(searchParams.get("tab")), [searchParams])

  const handleTabChange = useCallback(
    (tab: CrewTab) => {
      const next = new URLSearchParams(searchParams.toString())
      if (tab === "overview") next.delete("tab")
      else next.set("tab", tab)
      const qs = next.toString()
      router.replace(qs ? `?${qs}` : "?", { scroll: false })
    },
    [router, searchParams],
  )

  const [crew, setCrew] = useState<Crew | null>(null)
  const [members, setMembers] = useState<CrewMember[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [avatarStyle, setAvatarStyle] = useState("")
  const [applying, setApplying] = useState(false)
  const [resetting, setResetting] = useState(false)

  const fetchData = useCallback(async (silent = false) => {
    if (!workspaceId) return
    if (!silent) {
      setLoading(true)
      setError(null)
    }
    try {
      const [crewRes, membersRes, agentsRes] = await Promise.all([
        fetch(`/api/v1/crews/${params.crewId}?workspace_id=${workspaceId}`),
        fetch(`/api/v1/crews/${params.crewId}/members?workspace_id=${workspaceId}`),
        fetch(`/api/v1/agents?workspace_id=${workspaceId}&crew_id=${params.crewId}`),
      ])

      if (!crewRes.ok) { if (!silent) setError("Crew not found"); return }

      const crewData = (await crewRes.json()) as Crew
      setCrew(crewData)
      setAvatarStyle(crewData.avatar_style ?? "")
      if (membersRes.ok) setMembers(await membersRes.json())
      if (agentsRes.ok) setAgents(await agentsRes.json())
    } catch {
      if (!silent) setError("Failed to load crew")
    } finally {
      if (!silent) setLoading(false)
    }
  }, [workspaceId, params.crewId])

  useEffect(() => {
    if (!workspaceId) {
      if (!wsLoading) setLoading(false)
      return
    }
    fetchData()
  }, [workspaceId, wsLoading, fetchData])

  // Real-time: refetch crew data when agent/mission/crew changes occur
  useRealtimeEvent("agent.status", useCallback(() => { fetchData(true) }, [fetchData]))
  useRealtimeEvent("agent.created", useCallback(() => { fetchData(true) }, [fetchData]))
  useRealtimeEvent("agent.deleted", useCallback(() => { fetchData(true) }, [fetchData]))
  useRealtimeEvent("mission.updated", useCallback(() => { fetchData(true) }, [fetchData]))

  async function patchCrew(body: Record<string, unknown>) {
    if (!workspaceId || !crew) return
    const res = await fetch(`/api/v1/crews/${crew.id}?workspace_id=${workspaceId}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    })
    if (!res.ok) {
      const err = await res.json().catch(() => ({ error: "Request failed" }))
      throw new Error(err.error || `HTTP ${res.status}`)
    }
    const updated = (await res.json()) as Crew
    setCrew(updated)
  }

  async function handleApplyToAll() {
    if (!avatarStyle || !crew || !workspaceId) return
    const label = AVATAR_STYLES[avatarStyle]?.label ?? avatarStyle
    if (!confirm(`Apply "${label}" to all ${agents.length} agents? This overrides individual styles.`)) return

    setApplying(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${crew.id}/apply-avatar-style?workspace_id=${workspaceId}`,
        { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ avatar_style: avatarStyle }) },
      )
      if (res.ok) {
        const data = await res.json()
        toast.success(`Applied to ${data.updated} agents`)
        const agentsRes = await fetch(`/api/v1/agents?workspace_id=${workspaceId}&crew_id=${crew.id}`)
        if (agentsRes.ok) setAgents(await agentsRes.json())
      } else {
        toast.error("Failed to apply style")
      }
    } catch {
      toast.error("Network error")
    } finally {
      setApplying(false)
    }
  }

  async function handleResetOverrides() {
    if (!crew || !workspaceId) return
    if (!confirm(`Clear per-agent avatar overrides for all ${agents.length} agents in "${crew.name}"?\n\nAgents will fall back to the crew-level style. This cannot be undone automatically.`)) return

    setResetting(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${crew.id}/apply-avatar-style?workspace_id=${workspaceId}`,
        { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ reset_overrides: true }) },
      )
      if (res.ok) {
        const data = await res.json()
        toast.success(`Reset avatar override on ${data.updated} agents`)
        const agentsRes = await fetch(`/api/v1/agents?workspace_id=${workspaceId}&crew_id=${crew.id}`)
        if (agentsRes.ok) setAgents(await agentsRes.json())
      } else {
        toast.error("Failed to reset overrides")
      }
    } catch {
      toast.error("Network error")
    } finally {
      setResetting(false)
    }
  }

  async function handleDelete() {
    if (!workspaceId || !crew) return
    try {
      const res = await fetch(`/api/v1/crews/${crew.id}?workspace_id=${workspaceId}`, { method: "DELETE" })
      if (res.ok) { toast.success(`"${crew.name}" deleted`); router.push("/crews") }
      else toast.error("Failed to delete crew")
    } catch { toast.error("Failed to delete crew") }
  }

  if (error) {
    return (
      <div className="p-6 space-y-4">
        <Button variant="ghost" size="sm" asChild>
          <Link href="/crews"><ArrowLeft className="mr-2 h-4 w-4" />Back to Crews</Link>
        </Button>
        <p className="text-body text-destructive">{error}</p>
      </div>
    )
  }

  if (wsLoading || loading) {
    return (
      <div className="p-6 space-y-6">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-16 w-full rounded-xl" />
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Skeleton className="h-20" />
          <Skeleton className="h-20" />
          <Skeleton className="h-20" />
          <Skeleton className="h-20" />
        </div>
      </div>
    )
  }

  if (!crew || !workspaceId) return null

  const canEdit = abilities.can("update", "Crew")
  const canDelete = abilities.can("delete", "Crew")

  async function handleNetworkSave(mode: string, domains: string[]) {
    await patchCrew({ network_mode: mode, allowed_domains: domains })
  }

  async function handleContainerSave(config: { container_memory_mb: number; container_cpus: number; container_ttl_hours: number | null }) {
    await patchCrew(config)
  }

  async function handleRuntimeSave(config: { runtime_image: string | null; devcontainer_config: string | null; mise_config: string | null }) {
    await patchCrew(config)
  }

  const overrideCount = agents.filter((a) => (a as Agent & { avatar_style?: string | null }).avatar_style).length

  return (
    <div className="flex flex-col h-full min-h-0">
      <ToolbarStrip
        tabs={CREW_TABS}
        activeTab={activeTab}
        onTabChange={handleTabChange}
        leading={
          <Button variant="ghost" size="sm" asChild className="h-7 px-2 gap-1">
            <Link href="/crews"><ArrowLeft className="h-3.5 w-3.5" /><span className="text-micro">Back</span></Link>
          </Button>
        }
        ariaLabel="Crew tabs"
      />
      <div className="flex-1 overflow-y-auto">
      {activeTab === "overview" && (
      <div className="p-6 space-y-6">
        <div className="flex items-start gap-4">
          <CrewIconPopover
            icon={crew.icon || crew.name}
            color={crew.color || "90caf9"}
            onIconChange={(icon) => patchCrew({ icon }).catch(() => {})}
            onColorChange={(color) => patchCrew({ color }).catch(() => {})}
          />
          <div className="flex-1 min-w-0 pt-0.5">
            <div className="flex items-center gap-3">
              <h1 className="text-title font-semibold">{crew.name}</h1>
              <span className="text-label font-mono text-muted-foreground">{crew.slug}</span>
            </div>
            {crew.description && (
              <p className="text-body text-muted-foreground mt-1">{crew.description}</p>
            )}
            <p className="text-micro text-muted-foreground mt-1">
              Created {new Date(crew.created_at).toLocaleDateString()}
            </p>
          </div>
        </div>

        <CrewStats agentCount={crew._count.agents} memberCount={crew._count.members} />

        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" asChild>
            <Link href={`/crews/${crew.id}/files`}>
              <FolderOpen className="mr-2 h-4 w-4" />
              Crew Files
            </Link>
          </Button>
        </div>

        <CrewAgents
          agents={agents}
          crewId={crew.id}
          canCreate={abilities.can("create", "Agent")}
        />

        {canEdit && (
          <Card>
            <CardContent className="p-4 space-y-3">
              <div className="flex items-center gap-2">
                <Paintbrush className="h-4 w-4 text-muted-foreground" />
                <span className="text-body font-medium">Agent avatar style</span>
                {avatarStyle && (
                  <span className="text-label text-muted-foreground">
                    ({AVATAR_STYLES[avatarStyle]?.label ?? avatarStyle})
                  </span>
                )}
              </div>
              <p className="text-label text-muted-foreground">
                Choose a style for agent avatars in this crew. Individual agents can override via their own Settings.
              </p>
              <AvatarPicker
                seed={crew.name || "preview"}
                style={avatarStyle}
                onSeedChange={() => {}}
                onStyleChange={(s) => {
                  setAvatarStyle(s)
                  patchCrew({ avatar_style: s }).catch(() => {})
                }}
                styleOnly
              />
              <div className="flex items-center gap-3 flex-wrap">
                {avatarStyle && agents.length > 0 && (
                  <button
                    type="button"
                    onClick={handleApplyToAll}
                    disabled={applying}
                    className="text-micro font-medium text-primary hover:text-primary/80 inline-flex items-center gap-1 disabled:opacity-50"
                  >
                    {applying ? <Loader2 className="h-3 w-3 animate-spin" /> : <RefreshCw className="h-3 w-3" />}
                    Apply to all {agents.length} agents
                  </button>
                )}
                {overrideCount > 0 && (
                  <button
                    type="button"
                    onClick={handleResetOverrides}
                    disabled={resetting}
                    className="text-micro font-medium text-muted-foreground hover:text-foreground inline-flex items-center gap-1 disabled:opacity-50"
                  >
                    {resetting ? <Loader2 className="h-3 w-3 animate-spin" /> : <RotateCcw className="h-3 w-3" />}
                    Reset {overrideCount} override{overrideCount === 1 ? "" : "s"}
                  </button>
                )}
              </div>
            </CardContent>
          </Card>
        )}

        <CrewMissions
          crewId={crew.id}
          workspaceId={workspaceId}
          canCreate={abilities.can("create", "Crew")}
          leadAgents={agents
            .filter((a) => a.agent_role === "LEAD")
            .map((a) => ({ id: a.id, name: a.name, slug: a.slug }))}
        />

        <CrewAssignments crewId={crew.id} workspaceId={workspaceId} />

        <CrewPeerConversations crewId={crew.id} workspaceId={workspaceId} />

        {agents.length > 0 && (
          <div className={`flex items-center gap-3 rounded-lg border border-border p-3 ${STATUS_BG_LIGHT.BLOCKED}`}>
            <AlertTriangle className="h-4 w-4 shrink-0" />
            <p className="text-body">
              Agents need API keys to connect to LLM providers.{" "}
              <Link href="/credentials" className="font-medium underline underline-offset-2">
                Add credentials
              </Link>
            </p>
          </div>
        )}
      </div>
      )}

      {activeTab === "members" && (
        <div className="p-6">
          <CrewMembers
            members={members}
            crewId={crew.id}
            workspaceId={workspaceId}
            canEdit={canEdit}
            onMembersChange={setMembers}
          />
        </div>
      )}

      {activeTab === "network" && (
        <div className="p-6">
          <CrewNetworkPolicy
            networkMode={crew.network_mode || "free"}
            allowedDomains={crew.allowed_domains || []}
            canEdit={canEdit}
            onSave={handleNetworkSave}
          />
        </div>
      )}

      {activeTab === "runtime" && (
        <div className="p-6 space-y-6">
          {canEdit && (
            <Card>
              <CardContent className="flex items-center justify-between gap-4 p-4">
                <div className="min-w-0">
                  <div className="flex items-center gap-2 mb-1">
                    <span className="text-body font-medium">Issue Prefix</span>
                  </div>
                  <p className="text-label text-muted-foreground">
                    Used for issue identifiers (e.g. ENG-42). Defaults to first 3 letters of slug.
                  </p>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  <input
                    type="text"
                    defaultValue={crew.issue_prefix || ""}
                    placeholder={crew.slug.toUpperCase().slice(0, 3)}
                    className="w-20 h-8 text-label text-center font-mono uppercase border border-border rounded-md bg-background px-2"
                    maxLength={5}
                    onBlur={(e) => {
                      const val = e.target.value.trim().toUpperCase()
                      if (val !== (crew.issue_prefix || "")) {
                        void patchCrew({ issue_prefix: val || null }).catch((err) => {
                          toast.error(err instanceof Error ? err.message : "Failed to update issue prefix")
                        })
                      }
                    }}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") (e.target as HTMLInputElement).blur()
                    }}
                  />
                </div>
              </CardContent>
            </Card>
          )}

          {canEdit && workspaceId && (
            <Card>
              <div className="flex w-full items-center justify-between p-4">
                <div className="flex items-center gap-2">
                  <Blocks className="h-4 w-4 text-muted-foreground" />
                  <span className="text-body font-medium">Runtime Configuration</span>
                  {crew.devcontainer_config || crew.mise_config ? (
                    crew.cached_image ? (
                      <Badge variant="outline" className="text-xs text-green-600">Provisioned</Badge>
                    ) : (
                      <Badge variant="outline" className="text-xs text-amber-600">Configured</Badge>
                    )
                  ) : (
                    <Badge variant="outline" className="text-xs text-muted-foreground">Not configured</Badge>
                  )}
                </div>
              </div>
              <CardContent className="px-4 pb-4 pt-0">
                <CrewRuntimeConfig
                  crewId={crew.id}
                  workspaceId={workspaceId}
                  runtimeImage={crew.runtime_image}
                  devcontainerConfig={crew.devcontainer_config}
                  miseConfig={crew.mise_config}
                  cachedImage={crew.cached_image}
                  canEdit={canEdit}
                  onSave={handleRuntimeSave}
                />
              </CardContent>
            </Card>
          )}

          {canEdit && (
            <Collapsible>
              <Card>
                <CollapsibleTrigger asChild>
                  <button
                    type="button"
                    className="flex w-full items-center justify-between p-4 text-left hover:bg-muted/50 transition-colors rounded-xl"
                  >
                    <div className="flex items-center gap-2">
                      <Settings2 className="h-4 w-4 text-muted-foreground" />
                      <span className="text-body font-medium">Advanced Container Settings</span>
                      <span className="text-label text-muted-foreground">
                        {crew.container_memory_mb === 4096 && crew.container_cpus === 2 && !crew.container_ttl_hours
                          ? "(using defaults)"
                          : `(${crew.container_memory_mb} MB, ${crew.container_cpus} CPU)`}
                      </span>
                    </div>
                    <ChevronDown className="h-4 w-4 text-muted-foreground transition-transform duration-200 [[data-state=open]_&]:rotate-180" />
                  </button>
                </CollapsibleTrigger>
                <CollapsibleContent>
                  <CardContent className="px-4 pb-4 pt-0">
                    <CrewContainerConfig
                      memoryMb={crew.container_memory_mb}
                      cpus={crew.container_cpus}
                      ttlHours={crew.container_ttl_hours}
                      canEdit={canEdit}
                      onSave={handleContainerSave}
                    />
                  </CardContent>
                </CollapsibleContent>
              </Card>
            </Collapsible>
          )}

          {canEdit && (
            <Collapsible>
              <Card>
                <CollapsibleTrigger asChild>
                  <button
                    type="button"
                    className="flex w-full items-center justify-between p-4 text-left hover:bg-muted/50 transition-colors rounded-xl"
                  >
                    <div className="flex items-center gap-2">
                      <TerminalSquare className="h-4 w-4 text-muted-foreground" />
                      <span className="text-body font-medium">Terminal</span>
                      <span className="text-label text-muted-foreground">Connect to crew container</span>
                    </div>
                    <ChevronDown className="h-4 w-4 text-muted-foreground transition-transform duration-200 [[data-state=open]_&]:rotate-180" />
                  </button>
                </CollapsibleTrigger>
                <CollapsibleContent>
                  <CardContent className="px-4 pb-4 pt-0">
                    <WebTerminal
                      crewId={crew.id}
                      crewSlug={crew.slug}
                      agents={agents.map((a) => ({ id: a.id, slug: a.slug, name: a.name }))}
                    />
                  </CardContent>
                </CollapsibleContent>
              </Card>
            </Collapsible>
          )}

          {canEdit && workspaceId && (
            <CrewMCPConfig crewId={crew.id} workspaceId={workspaceId} />
          )}
        </div>
      )}

      {activeTab === "journal" && (
        <div className="p-6 space-y-6">
          <CrewEscalations crewId={crew.id} workspaceId={workspaceId} />
          <CrewJournal crewId={crew.id} workspaceId={workspaceId} />
        </div>
      )}

      {activeTab === "settings" && (
        <div className="p-6 space-y-6">
          <Card>
            <CardContent className="p-4 space-y-3">
              <div className="flex items-start gap-4">
                <CrewIconPopover
                  icon={crew.icon || crew.name}
                  color={crew.color || "90caf9"}
                  onIconChange={(icon) => patchCrew({ icon }).catch(() => {})}
                  onColorChange={(color) => patchCrew({ color }).catch(() => {})}
                />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="text-body font-medium">{crew.name}</span>
                    <span className="text-label font-mono text-muted-foreground">{crew.slug}</span>
                  </div>
                  {crew.description && (
                    <p className="text-label text-muted-foreground mt-1">{crew.description}</p>
                  )}
                </div>
              </div>
            </CardContent>
          </Card>
          {canDelete && <CrewDangerZone crewName={crew.name} onDelete={handleDelete} />}
        </div>
      )}
      </div>
    </div>
  )
}
