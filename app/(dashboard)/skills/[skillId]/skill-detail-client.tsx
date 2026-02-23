"use client"

import { useEffect, useState } from "react"
import { useParams } from "next/navigation"
import { ArrowLeft } from "lucide-react"
import Link from "next/link"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { SkillDetailView } from "@/components/skills/skill-detail"
import { InstallSkillDialog } from "@/components/skills/install-skill-dialog"
import { useWorkspace } from "@/hooks/use-workspace"

interface SkillDetail {
  id: string
  name: string
  slug: string
  display_name: string | null
  description: string | null
  version: string | null
  author: string | null
  category: string
  source: string
  icon: string | null
  verification: string | null
  content: string | null
  credential_requirements: string | null
  mcp_server_command: string | null
  mcp_transport: string | null
  license: string | null
  tags: string | null
  tool_count: number | null
  agent_count: number
  created_at: string
  updated_at: string
}

export function SkillDetailPageClient() {
  const params = useParams<{ skillId: string }>()
  const { workspaceId, loading: wsLoading } = useWorkspace()

  const [skill, setSkill] = useState<SkillDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!workspaceId) {
      if (!wsLoading) setLoading(false)
      return
    }

    let cancelled = false

    async function fetchSkill() {
      setLoading(true)
      setError(null)
      try {
        const res = await fetch(
          `/api/v1/skills/${params.skillId}?workspace_id=${workspaceId}`
        )
        if (!res.ok) {
          setError("Skill not found")
          return
        }
        const data = (await res.json()) as SkillDetail
        if (!cancelled) setSkill(data)
      } catch {
        if (!cancelled) setError("Failed to load skill")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchSkill()
    return () => {
      cancelled = true
    }
  }, [workspaceId, wsLoading, params.skillId])

  const isLoading = wsLoading || loading

  if (error) {
    return (
      <div className="p-4 sm:p-6 space-y-4 max-w-3xl">
        <Button variant="ghost" size="sm" asChild>
          <Link href="/skills">
            <ArrowLeft className="mr-2 h-4 w-4" />
            Back to Skills
          </Link>
        </Button>
        <p className="text-sm text-destructive">{error}</p>
      </div>
    )
  }

  if (isLoading) {
    return (
      <div className="p-4 sm:p-6 space-y-4 max-w-3xl">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-[80px] rounded-xl" />
        <Skeleton className="h-[200px] rounded-xl" />
      </div>
    )
  }

  if (!skill || !workspaceId) return null

  return (
    <div className="p-4 sm:p-6 space-y-6 max-w-3xl">
      <div className="flex items-center justify-between">
        <Button variant="ghost" size="sm" asChild>
          <Link href="/skills">
            <ArrowLeft className="mr-2 h-4 w-4" />
            Back to Skills
          </Link>
        </Button>
        <InstallSkillDialog skillId={skill.id} workspaceId={workspaceId} />
      </div>

      <SkillDetailView skill={skill} />
    </div>
  )
}
