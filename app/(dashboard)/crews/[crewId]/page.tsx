"use client"

import { useEffect, useState, type FormEvent } from "react"
import { useParams, useRouter } from "next/navigation"
import { ArrowLeft, AlertTriangle } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Separator } from "@/components/ui/separator"
import { CrewHeader } from "@/components/features/crews/crew-header"
import { CrewStats } from "@/components/features/crews/crew-stats"
import { CrewEditForm } from "@/components/features/crews/crew-edit-form"
import { CrewAgents } from "@/components/features/crews/crew-agents"
import { CrewMembers } from "@/components/features/crews/crew-members"
import { CrewDangerZone } from "@/components/features/crews/crew-danger-zone"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { updateCrewSchema } from "@/lib/validations"
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
  container_ttl_hours: number | null
  container_memory_mb: number
  container_cpus: number
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
  const [credentialCount, setCredentialCount] = useState<number | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [editing, setEditing] = useState(false)
  const [formName, setFormName] = useState("")
  const [formDescription, setFormDescription] = useState("")
  const [formColor, setFormColor] = useState("#6b7280")
  const [formIcon, setFormIcon] = useState("")
  const [formMemory, setFormMemory] = useState("4096")
  const [formCpus, setFormCpus] = useState("2")
  const [formTtl, setFormTtl] = useState("")
  const [saving, setSaving] = useState(false)

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
        const [crewRes, membersRes, agentsRes, credsRes] = await Promise.all([
          fetch(`/api/v1/crews/${params.crewId}?workspace_id=${workspaceId}`),
          fetch(`/api/v1/crews/${params.crewId}/members?workspace_id=${workspaceId}`),
          fetch(`/api/v1/agents?workspace_id=${workspaceId}&crew_id=${params.crewId}`),
          fetch(`/api/v1/credentials?workspace_id=${workspaceId}`),
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
          setFormColor(crewData.color ?? "#6b7280")
          setFormIcon(crewData.icon ?? "")
          setFormMemory(String(crewData.container_memory_mb))
          setFormCpus(String(crewData.container_cpus))
          setFormTtl(crewData.container_ttl_hours ? String(crewData.container_ttl_hours) : "")
        }

        if (membersRes.ok) {
          const membersData = (await membersRes.json()) as CrewMember[]
          if (!cancelled) setMembers(membersData)
        }

        if (agentsRes.ok) {
          const agentsData = (await agentsRes.json()) as Agent[]
          if (!cancelled) setAgents(agentsData)
        }

        if (credsRes.ok) {
          const credsData = (await credsRes.json()) as unknown[]
          if (!cancelled) setCredentialCount(credsData.length)
        } else if (!cancelled) {
          setCredentialCount(null)
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

    const parsed = updateCrewSchema.safeParse({
      name: formName,
      description: formDescription || undefined,
      color: formColor,
      icon: formIcon || undefined,
      container_memory_mb: formMemory ? parseInt(formMemory) : undefined,
      container_cpus: formCpus ? parseFloat(formCpus) : undefined,
      container_ttl_hours: formTtl ? parseInt(formTtl) : null,
    })

    if (!parsed.success) {
      const msg = parsed.error.issues[0]?.message ?? "Invalid input"
      toast.error(msg)
      return
    }

    setSaving(true)

    try {
      const res = await fetch(`/api/v1/crews/${crew.id}?workspace_id=${workspaceId}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(parsed.data),
      })

      if (!res.ok) {
        const body = await res.json().catch(() => null)
        const msg = typeof body?.error === "string" ? body.error : "Failed to save"
        toast.error(msg)
        return
      }

      const updated = (await res.json()) as Crew
      setCrew(updated)
      setEditing(false)
      toast.success("Crew updated successfully")
    } catch {
      toast.error("Failed to save changes")
    } finally {
      setSaving(false)
    }
  }

  async function handleDelete() {
    if (!workspaceId || !crew) return

    try {
      const res = await fetch(`/api/v1/crews/${crew.id}?workspace_id=${workspaceId}`, {
        method: "DELETE",
      })
      if (res.ok) {
        toast.success(`"${crew.name}" deleted`)
        router.push("/crews")
      } else {
        toast.error("Failed to delete crew")
      }
    } catch {
      toast.error("Failed to delete crew")
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
      <Button variant="ghost" size="sm" className="mb-3" asChild>
        <Link href="/crews"><ArrowLeft className="mr-2 h-4 w-4" />Back to Crews</Link>
      </Button>

      <CrewHeader
        name={crew.name}
        slug={crew.slug}
        color={crew.color}
        icon={crew.icon}
        description={crew.description}
        editing={editing}
        canEdit={canEdit}
        onToggleEdit={() => setEditing(!editing)}
      />

      {editing && canEdit && (
        <CrewEditForm
          name={formName}
          description={formDescription}
          color={formColor}
          icon={formIcon}
          containerTtlHours={formTtl}
          containerMemoryMb={formMemory}
          containerCpus={formCpus}
          saving={saving}
          onNameChange={setFormName}
          onDescriptionChange={setFormDescription}
          onColorChange={setFormColor}
          onIconChange={setFormIcon}
          onTtlChange={setFormTtl}
          onMemoryChange={setFormMemory}
          onCpusChange={setFormCpus}
          onSubmit={handleSave}
        />
      )}

      <CrewStats
        agentCount={crew._count.agents}
        memberCount={crew._count.members}
        memoryMb={crew.container_memory_mb}
        cpus={crew.container_cpus}
        ttlHours={crew.container_ttl_hours}
      />

      <CrewAgents
        agents={agents}
        crewId={crew.id}
        canCreate={abilities.can("create", "Agent")}
      />

      {agents.length > 0 && credentialCount !== null && credentialCount === 0 && (
        <div className="flex items-center gap-3 rounded-lg border border-amber-200 bg-amber-50 p-3 dark:border-amber-900 dark:bg-amber-950/30">
          <AlertTriangle className="h-4 w-4 text-amber-600 shrink-0" />
          <p className="text-sm text-amber-800 dark:text-amber-200">
            No credentials configured. Agents need API keys to connect to LLM providers.{" "}
            <Link href="/credentials" className="font-medium underline underline-offset-2">
              Add credentials
            </Link>
          </p>
        </div>
      )}

      <Separator />

      <CrewMembers
        members={members}
        crewId={crew.id}
        workspaceId={workspaceId!}
        canEdit={canEdit}
        onMembersChange={setMembers}
      />

      {canDelete && (
        <>
          <Separator />
          <CrewDangerZone crewName={crew.name} onDelete={handleDelete} />
        </>
      )}

      <div className="text-xs text-muted-foreground">
        Created {new Date(crew.created_at).toLocaleDateString()}
      </div>
    </div>
  )
}
