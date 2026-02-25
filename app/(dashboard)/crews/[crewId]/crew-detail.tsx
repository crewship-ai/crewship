"use client"

import { useEffect, useState, type FormEvent } from "react"
import { useParams, useRouter } from "next/navigation"
import { Bot, Users, Pencil, Trash2, ArrowLeft, Clock, Cpu, HardDrive, RefreshCw, AlertTriangle } from "lucide-react"
import { AVATAR_STYLES, getAgentAvatarUrl } from "@/lib/agent-avatar"
import { getCrewIconUrl } from "@/lib/crew-icon"
import { AvatarPicker } from "@/components/avatar-picker"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { Skeleton } from "@/components/ui/skeleton"
import { Separator } from "@/components/ui/separator"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { AgentCard } from "@/components/features/agents/agent-card"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
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
  created_at: string
  _count_agents: number
  _count_members: number
}

interface CrewMember {
  id: string
  user_id: string
  created_at: string
  user: { id: string; email: string; full_name: string | null; avatar_url: string | null }
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

export function CrewDetailClient() {
  const params = useParams<{ crewId: string }>()
  const router = useRouter()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { abilities } = useAbilities()

  const [crew, setCrew] = useState<Crew | null>(null)
  const [members, setMembers] = useState<CrewMember[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [editing, setEditing] = useState(false)
  const [formName, setFormName] = useState("")
  const [formDescription, setFormDescription] = useState("")
  const [formAvatarSeed, setFormAvatarSeed] = useState("")
  const [formAvatarStyle, setFormAvatarStyle] = useState("")
  const [applyingToAgents, setApplyingToAgents] = useState(false)
  const [saveStatus, setSaveStatus] = useState<"idle" | "saving" | "success" | "error">("idle")
  const [saveError, setSaveError] = useState<string | null>(null)

  useEffect(() => {
    if (!workspaceId) {
      if (!wsLoading) setLoading(false)
      return
    }

    let cancelled = false

    async function fetchData() {
      setLoading(true)
      setError(null)
      try {
        const [crewRes, membersRes, agentsRes] = await Promise.all([
          fetch(`/api/v1/crews/${params.crewId}?workspace_id=${workspaceId}`),
          fetch(`/api/v1/crews/${params.crewId}/members?workspace_id=${workspaceId}`),
          fetch(`/api/v1/agents?workspace_id=${workspaceId}&crew_id=${params.crewId}`),
        ])

        if (!crewRes.ok) {
          setError("Crew not found")
          return
        }

        const crewData = (await crewRes.json()) as Crew
        if (!cancelled) {
          setCrew(crewData)
          setFormName(crewData.name)
          setFormDescription(crewData.description ?? "")
          setFormAvatarSeed(crewData.icon ?? "")
          setFormAvatarStyle(crewData.avatar_style ?? "")
        }

        if (membersRes.ok) {
          const membersData = (await membersRes.json()) as CrewMember[]
          if (!cancelled) setMembers(membersData)
        }

        if (agentsRes.ok) {
          const agentsData = (await agentsRes.json()) as Agent[]
          if (!cancelled) setAgents(agentsData)
        }
      } catch {
        if (!cancelled) setError("Failed to load crew")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchData()
    return () => { cancelled = true }
  }, [workspaceId, wsLoading, params.crewId])

  async function handleSave(e: FormEvent) {
    e.preventDefault()
    if (!workspaceId || !crew) return

    setSaveStatus("saving")
    setSaveError(null)

    try {
      const res = await fetch(`/api/v1/crews/${crew.id}?workspace_id=${workspaceId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: formName,
          description: formDescription || undefined,
          icon: formAvatarSeed || undefined,
          avatar_style: formAvatarStyle || undefined,
        }),
      })

      if (!res.ok) {
        const body = await res.json().catch(() => null)
        const msg = typeof body?.error === "string" ? body.error : "Failed to save"
        setSaveStatus("error")
        setSaveError(msg)
        return
      }

      const updated = (await res.json()) as Crew
      setCrew(updated)
      setEditing(false)
      setSaveStatus("success")
      setTimeout(() => setSaveStatus("idle"), 3000)
    } catch {
      setSaveStatus("error")
      setSaveError("Failed to save changes")
    }
  }

  async function handleDelete() {
    if (!workspaceId || !crew) return

    const confirmed = window.confirm(
      `Are you sure you want to delete "${crew.name}"? This action cannot be undone.`
    )
    if (!confirmed) return

    try {
      const res = await fetch(`/api/v1/crews/${crew.id}?workspace_id=${workspaceId}`, {
        method: "DELETE",
      })
      if (res.ok) {
        router.push("/crews")
      } else {
        setSaveError("Failed to delete crew")
      }
    } catch {
      setSaveError("Failed to delete crew")
    }
  }

  async function handleRemoveMember(memberId: string) {
    if (!workspaceId || !crew) return

    const confirmed = window.confirm("Remove this member from the crew?")
    if (!confirmed) return

    try {
      const res = await fetch(
        `/api/v1/crews/${crew.id}/members/${memberId}?workspace_id=${workspaceId}`,
        { method: "DELETE" }
      )
      if (res.ok) {
        setMembers((prev) => prev.filter((m) => m.id !== memberId))
      }
    } catch {
      // silently fail
    }
  }

  const isLoading = wsLoading || loading

  if (error) {
    return (
      <div className="p-4 sm:p-6 space-y-4 max-w-4xl">
        <Button variant="ghost" size="sm" asChild>
          <Link href="/crews"><ArrowLeft className="mr-2 h-4 w-4" />Back to Crews</Link>
        </Button>
        <p className="text-sm text-destructive">{error}</p>
      </div>
    )
  }

  if (isLoading) {
    return (
      <div className="p-4 sm:p-6 space-y-4 max-w-4xl">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-[200px] rounded-xl" />
        <Skeleton className="h-[200px] rounded-xl" />
      </div>
    )
  }

  if (!crew) return null

  const canEdit = abilities.can("update", "Crew")
  const canDelete = abilities.can("delete", "Crew")

  return (
    <div className="p-4 sm:p-6 space-y-6 max-w-4xl">
      {/* Header */}
      <div>
        <Button variant="ghost" size="sm" className="mb-3" asChild>
          <Link href="/crews"><ArrowLeft className="mr-2 h-4 w-4" />Back to Crews</Link>
        </Button>
        <div className="flex items-center gap-4">
          <img
            src={getCrewIconUrl(crew.icon || crew.name)}
            alt={crew.name}
            className="h-12 w-12 rounded-lg shrink-0"
          />
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-3">
              <h1 className="text-xl font-semibold truncate">{crew.name}</h1>
              <span
                className="h-3 w-3 rounded-full shrink-0"
                style={{ backgroundColor: crew.color ?? "#6b7280" }}
              />
            </div>
            <p className="text-sm text-muted-foreground font-mono">{crew.slug}</p>
          </div>
          {canEdit && (
            <Button variant="outline" size="sm" onClick={() => setEditing(!editing)}>
              <Pencil className="mr-2 h-3.5 w-3.5" />
              {editing ? "Cancel" : "Edit"}
            </Button>
          )}
        </div>
        {crew.description && !editing && (
          <p className="text-sm text-muted-foreground mt-2">{crew.description}</p>
        )}
      </div>

      {/* Edit form */}
      {editing && canEdit && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Edit Crew</CardTitle>
          </CardHeader>
          <CardContent>
            <form onSubmit={handleSave} className="space-y-6">
              <div className="space-y-2">
                <Label htmlFor="team-name">Name</Label>
                <Input id="team-name" value={formName} onChange={(e) => setFormName(e.target.value)} />
              </div>
              <div className="space-y-2">
                <Label htmlFor="team-desc">Description</Label>
                <Textarea id="team-desc" value={formDescription} onChange={(e) => setFormDescription(e.target.value)} rows={3} />
              </div>

              <Separator />

              {/* Crew Icon — DiceBear icons style */}
              <div className="space-y-2">
                <Label>Crew Icon</Label>
                <div className="flex items-start gap-4">
                  <img
                    src={getCrewIconUrl(formAvatarSeed || formName || "preview")}
                    alt="Crew icon"
                    className="h-16 w-16 rounded-xl border shrink-0"
                  />
                  <div className="space-y-2 flex-1">
                    <Label className="text-xs text-muted-foreground font-normal">Icon Seed</Label>
                    <div className="flex gap-2">
                      <Input
                        value={formAvatarSeed}
                        onChange={(e) => setFormAvatarSeed(e.target.value)}
                        placeholder="Leave empty to use crew name"
                        className="font-mono text-xs"
                      />
                      <Button
                        type="button"
                        variant="outline"
                        size="icon"
                        onClick={() => setFormAvatarSeed(Math.random().toString(36).substring(2, 10))}
                        title="Randomize"
                      >
                        <RefreshCw className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                  </div>
                </div>
                <div className="grid grid-cols-8 gap-2 mt-2">
                  {["alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"].map((s) => (
                    <button
                      key={s}
                      type="button"
                      onClick={() => setFormAvatarSeed(s)}
                      className={`rounded-lg border p-1.5 transition-colors hover:bg-muted ${
                        formAvatarSeed === s ? "border-primary bg-primary/5" : "border-border"
                      }`}
                    >
                      <img src={getCrewIconUrl(s)} alt={s} className="h-8 w-8 rounded" />
                    </button>
                  ))}
                </div>
              </div>

              <Separator />

              {/* Agent Avatar Style — same picker as agent settings */}
              <div className="space-y-3">
                <Label>Agent Avatar Style</Label>
                <p className="text-xs text-muted-foreground">
                  New style for agents in this crew. Agents with custom styles keep theirs unless you apply below.
                </p>
                <AvatarPicker
                  seed={crew?.name || "preview"}
                  style={formAvatarStyle}
                  onSeedChange={() => {}}
                  onStyleChange={setFormAvatarStyle}
                />
              </div>

              <Separator />

              {/* Apply to all agents — destructive */}
              <div className="rounded-lg border border-amber-200 bg-amber-50/50 dark:border-amber-900 dark:bg-amber-950/20 p-4 space-y-3">
                <div className="flex items-start gap-2">
                  <AlertTriangle className="h-4 w-4 text-amber-600 shrink-0 mt-0.5" />
                  <div>
                    <p className="text-sm font-medium text-amber-800 dark:text-amber-200">Apply style to all agents</p>
                    <p className="text-xs text-amber-700 dark:text-amber-300 mt-1">
                      This will overwrite avatar style on all {agents.length} agent{agents.length !== 1 ? "s" : ""} in this crew,
                      including any custom styles they may have set individually. This cannot be undone.
                    </p>
                  </div>
                </div>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="border-amber-300 text-amber-700 hover:bg-amber-100 dark:border-amber-800 dark:text-amber-300"
                  disabled={applyingToAgents || !formAvatarStyle}
                  onClick={async () => {
                    const style = AVATAR_STYLES[formAvatarStyle]?.label ?? formAvatarStyle
                    if (!confirm(
                      `Apply "${style}" avatar style to all ${agents.length} agent${agents.length !== 1 ? "s" : ""} in ${crew?.name}?\n\nThis will overwrite any individually set avatar styles. This cannot be undone.`
                    )) return

                    setApplyingToAgents(true)
                    try {
                      const res = await fetch(
                        `/api/v1/crews/${crew?.id}/apply-avatar-style?workspace_id=${workspaceId}`,
                        {
                          method: "POST",
                          headers: { "Content-Type": "application/json" },
                          body: JSON.stringify({ avatar_style: formAvatarStyle }),
                        }
                      )
                      if (!res.ok) {
                        const data = await res.json().catch(() => ({ error: "Failed" }))
                        setSaveError(typeof data.error === "string" ? data.error : "Failed to apply")
                        setSaveStatus("error")
                      } else {
                        const data = await res.json()
                        setSaveStatus("success")
                        setSaveError(null)
                        // Refresh agents list
                        const agentsRes = await fetch(`/api/v1/agents?workspace_id=${workspaceId}&crew_id=${crew?.id}`)
                        if (agentsRes.ok) setAgents(await agentsRes.json())
                      }
                    } catch {
                      setSaveError("Network error")
                      setSaveStatus("error")
                    } finally {
                      setApplyingToAgents(false)
                    }
                  }}
                >
                  {applyingToAgents ? "Applying..." : `Apply to all ${agents.length} agents`}
                </Button>
              </div>

              {saveStatus === "success" && <p className="text-sm text-emerald-600">Changes saved.</p>}
              {saveStatus === "error" && saveError && <p className="text-sm text-destructive">{saveError}</p>}

              <Button type="submit" disabled={saveStatus === "saving"}>
                {saveStatus === "saving" ? "Saving..." : "Save Changes"}
              </Button>
            </form>
          </CardContent>
        </Card>
      )}

      {/* Stats */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
        <Card>
          <CardContent className="p-4">
            <div className="flex items-center gap-2 text-muted-foreground">
              <Bot className="h-4 w-4" />
              <span className="text-xs">Agents</span>
            </div>
            <p className="text-2xl font-bold mt-1">{crew._count_agents ?? 0}</p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="p-4">
            <div className="flex items-center gap-2 text-muted-foreground">
              <Users className="h-4 w-4" />
              <span className="text-xs">Members</span>
            </div>
            <p className="text-2xl font-bold mt-1">{crew._count_members ?? 0}</p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="p-4">
            <div className="flex items-center gap-2 text-muted-foreground">
              <HardDrive className="h-4 w-4" />
              <span className="text-xs">Memory</span>
            </div>
            <p className="text-2xl font-bold mt-1">{crew.container_memory_mb} MB</p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="p-4">
            <div className="flex items-center gap-2 text-muted-foreground">
              <Cpu className="h-4 w-4" />
              <span className="text-xs">CPUs</span>
            </div>
            <p className="text-2xl font-bold mt-1">{crew.container_cpus}</p>
          </CardContent>
        </Card>
      </div>

      {/* Container config */}
      {crew.container_ttl_hours && (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Clock className="h-4 w-4" />
          <span>Container TTL: {crew.container_ttl_hours}h</span>
        </div>
      )}

      {/* Agents */}
      <div>
        <div className="flex items-center justify-between mb-3">
          <h2 className="text-base font-semibold">Agents</h2>
          {abilities.can("create", "Agent") && (
            <Button size="sm" asChild>
              <Link href={`/agents/new?crew_id=${crew.id}`}>New Agent</Link>
            </Button>
          )}
        </div>
        {agents.length === 0 ? (
          <p className="text-sm text-muted-foreground">No agents in this crew yet.</p>
        ) : (
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            {agents.map((agent) => (
              <AgentCard key={agent.id} agent={agent} />
            ))}
          </div>
        )}
      </div>

      <Separator />

      {/* Members */}
      <div>
        <div className="flex items-center justify-between mb-3">
          <h2 className="text-base font-semibold">Members</h2>
        </div>
        {members.length === 0 ? (
          <p className="text-sm text-muted-foreground">No crew members yet.</p>
        ) : (
          <Card>
            <CardContent className="p-0">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Name</TableHead>
                    <TableHead>Email</TableHead>
                    <TableHead>Joined</TableHead>
                    {canEdit && <TableHead className="w-20" />}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {members.map((member) => (
                    <TableRow key={member.id}>
                      <TableCell className="text-sm font-medium">
                        {member.user.full_name ?? "—"}
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground">
                        {member.user.email}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {new Date(member.created_at).toLocaleDateString()}
                      </TableCell>
                      {canEdit && (
                        <TableCell>
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => handleRemoveMember(member.id)}
                            className="text-destructive hover:text-destructive"
                          >
                            Remove
                          </Button>
                        </TableCell>
                      )}
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </CardContent>
          </Card>
        )}
      </div>

      {/* Danger zone */}
      {canDelete && (
        <>
          <Separator />
          <Card>
            <CardHeader>
              <CardTitle className="text-base">Danger Zone</CardTitle>
              <CardDescription>Irreversible actions for this crew</CardDescription>
            </CardHeader>
            <CardContent>
              <Button variant="destructive" onClick={handleDelete}>
                <Trash2 className="mr-2 h-4 w-4" />
                Delete Crew
              </Button>
            </CardContent>
          </Card>
        </>
      )}

      <div className="text-xs text-muted-foreground">
        Created {new Date(crew.created_at).toLocaleDateString()}
      </div>
    </div>
  )
}
