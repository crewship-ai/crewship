"use client"

import { useCallback, useEffect, useState } from "react"
import { useParams, useRouter } from "next/navigation"
import {
  ArrowLeft, AlertTriangle, Paintbrush, RefreshCw, Loader2, ChevronDown, Settings2, FolderOpen,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import { Skeleton } from "@/components/ui/skeleton"
import { CrewIconPopover } from "@/components/crew-icon-popover"
import { CrewStats } from "@/components/features/crews/crew-stats"
import { CrewAgents } from "@/components/features/crews/crew-agents"
import { CrewMembers } from "@/components/features/crews/crew-members"
import { CrewMissions } from "@/components/features/crews/crew-missions"
import { CrewAssignments } from "@/components/features/crews/crew-assignments"
import { CrewPeerConversations } from "@/components/features/crews/crew-peer-conversations"
import { CrewEscalations } from "@/components/features/crews/crew-escalations"
import { CrewStandup } from "@/components/features/crews/crew-standup"
import { CrewDangerZone } from "@/components/features/crews/crew-danger-zone"
import { CrewNetworkPolicy } from "@/components/features/crews/crew-network-policy"
import { CrewContainerConfig } from "@/components/features/crews/crew-container-config"
import { AvatarPicker } from "@/components/avatar-picker"
import { AVATAR_STYLES } from "@/lib/agent-avatar"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import type { CrewMember } from "@/lib/types/crew"
import { toast } from "sonner"
import Link from "next/link"

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

export default function CrewDetailPage() {
  const params = useParams<{ crewId: string }>()
  const router = useRouter()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { abilities } = useAbilities()

  const [crew, setCrew] = useState<Crew | null>(null)
  const [members, setMembers] = useState<CrewMember[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [avatarStyle, setAvatarStyle] = useState("")
  const [applying, setApplying] = useState(false)

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
  useRealtimeEvent("run.completed", useCallback(() => { fetchData(true) }, [fetchData]))

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
      <div className="p-4 sm:p-6">
        <Button variant="ghost" size="sm" asChild>
          <Link href="/crews"><ArrowLeft className="mr-2 h-4 w-4" />Back to Crews</Link>
        </Button>
        <p className="text-sm text-destructive mt-4">{error}</p>
      </div>
    )
  }

  if (wsLoading || loading) {
    return (
      <div className="p-4 sm:p-6 space-y-4">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-16 w-full rounded-xl" />
        <div className="grid grid-cols-4 gap-3"><Skeleton className="h-20" /><Skeleton className="h-20" /><Skeleton className="h-20" /><Skeleton className="h-20" /></div>
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

  return (
    <div className="p-4 sm:p-6 space-y-6">
      <Button variant="ghost" size="sm" asChild>
        <Link href="/crews"><ArrowLeft className="mr-2 h-4 w-4" />Back to Crews</Link>
      </Button>

      {/* Hero */}
      <div className="flex items-start gap-4">
        <CrewIconPopover
          icon={crew.icon || crew.name}
          color={crew.color || "90caf9"}
          onIconChange={(icon) => patchCrew({ icon }).catch(() => {})}
          onColorChange={(color) => patchCrew({ color }).catch(() => {})}
        />
        <div className="flex-1 min-w-0 pt-0.5">
          <div className="flex items-center gap-3">
            <h1 className="text-xl font-semibold">{crew.name}</h1>
            <span className="text-xs font-mono text-muted-foreground">{crew.slug}</span>
          </div>
          {crew.description && (
            <p className="text-sm text-muted-foreground mt-1">{crew.description}</p>
          )}
          <p className="text-xs text-muted-foreground mt-1">
            Created {new Date(crew.created_at).toLocaleDateString()}
          </p>
        </div>
      </div>

      {/* Stats */}
      <CrewStats
        agentCount={crew._count.agents}
        memberCount={crew._count.members}
      />

      <div className="flex items-center gap-2">
        <Button variant="outline" size="sm" asChild>
          <Link href={`/crews/${crew.id}/files`}>
            <FolderOpen className="mr-2 h-4 w-4" />
            Crew Files
          </Link>
        </Button>
      </div>

      {/* Network Policy */}
      <CrewNetworkPolicy
        networkMode={crew.network_mode || "free"}
        allowedDomains={crew.allowed_domains || []}
        canEdit={canEdit}
        onSave={handleNetworkSave}
      />

      {/* Advanced — Container Config */}
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
                  <span className="text-sm font-medium">Advanced Container Settings</span>
                  <span className="text-xs text-muted-foreground">
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

      {/* Agents */}
      <CrewAgents
        agents={agents}
        crewId={crew.id}
        canCreate={abilities.can("create", "Agent")}
      />

      {/* Appearance — collapsible */}
      {canEdit && (
        <Collapsible>
          <Card>
            <CollapsibleTrigger asChild>
              <button
                type="button"
                className="flex w-full items-center justify-between p-4 text-left hover:bg-muted/50 transition-colors rounded-xl"
              >
                <div className="flex items-center gap-2">
                  <Paintbrush className="h-4 w-4 text-muted-foreground" />
                  <span className="text-sm font-medium">Agent Avatar Style</span>
                  {avatarStyle && (
                    <span className="text-xs text-muted-foreground">
                      ({AVATAR_STYLES[avatarStyle]?.label ?? avatarStyle})
                    </span>
                  )}
                </div>
                <ChevronDown className="h-4 w-4 text-muted-foreground transition-transform duration-200 [[data-state=open]_&]:rotate-180" />
              </button>
            </CollapsibleTrigger>
            <CollapsibleContent>
              <CardContent className="px-4 pb-4 pt-0 space-y-3">
                <p className="text-xs text-muted-foreground">Choose a style for agent avatars in this crew.</p>
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
                {avatarStyle && agents.length > 0 && (
                  <button
                    type="button"
                    onClick={handleApplyToAll}
                    disabled={applying}
                    className="text-[11px] font-medium text-amber-600 hover:text-amber-700 dark:text-amber-400 inline-flex items-center gap-1 disabled:opacity-50"
                  >
                    {applying ? (
                      <Loader2 className="h-3 w-3 animate-spin" />
                    ) : (
                      <RefreshCw className="h-3 w-3" />
                    )}
                    Apply to all {agents.length} agents
                  </button>
                )}
              </CardContent>
            </CollapsibleContent>
          </Card>
        </Collapsible>
      )}

      {/* Missions */}
      <CrewMissions
        crewId={crew.id}
        workspaceId={workspaceId}
        canCreate={abilities.can("create", "Crew")}
        leadAgents={agents
          .filter((a) => a.agent_role === "LEAD")
          .map((a) => ({ id: a.id, name: a.name, slug: a.slug }))}
      />

      {/* Assignments */}
      <CrewAssignments crewId={crew.id} workspaceId={workspaceId} />

      {/* Peer Conversations */}
      <CrewPeerConversations crewId={crew.id} workspaceId={workspaceId} />

      {/* Escalations */}
      <CrewEscalations crewId={crew.id} workspaceId={workspaceId} />

      {/* Standup */}
      <CrewStandup crewId={crew.id} workspaceId={workspaceId} />

      {/* Credentials reminder */}
      {agents.length > 0 && (
        <div className="flex items-center gap-3 rounded-lg border border-amber-200 bg-amber-50 p-3 dark:border-amber-900 dark:bg-amber-950/30">
          <AlertTriangle className="h-4 w-4 text-amber-600 shrink-0" />
          <p className="text-sm text-amber-800 dark:text-amber-200">
            Agents need API keys to connect to LLM providers.{" "}
            <Link href="/credentials" className="font-medium underline underline-offset-2">
              Add credentials
            </Link>
          </p>
        </div>
      )}

      {/* Members */}
      <CrewMembers
        members={members}
        crewId={crew.id}
        workspaceId={workspaceId}
        canEdit={canEdit}
        onMembersChange={setMembers}
      />

      {/* Danger Zone */}
      {canDelete && <CrewDangerZone crewName={crew.name} onDelete={handleDelete} />}
    </div>
  )
}
