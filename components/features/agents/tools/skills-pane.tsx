"use client"

import { useAgentId } from "@/hooks/use-agent-id"
import { useState, useEffect, useCallback } from "react"
import {
  Puzzle, AlertCircle, Plus, Trash2, Loader2,
  Blocks, Code, Search, Hammer, Server, MessageCircle, Settings,
} from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { EmptyState } from "@/components/layout/empty-state"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { useWorkspace } from "@/hooks/use-workspace"
import { z } from "zod"

const SkillDataSchema = z.object({
  id: z.string(),
  name: z.string(),
  slug: z.string(),
  display_name: z.string().nullable(),
  description: z.string().nullable(),
  category: z.string().nullable(),
  source: z.string(),
  icon: z.string().nullable(),
  version: z.string().nullable(),
})

const AgentSkillSchema = z.object({
  id: z.string(),
  agent_id: z.string(),
  skill_id: z.string(),
  enabled: z.boolean(),
  config: z.record(z.string(), z.unknown()).nullable(),
  skill: SkillDataSchema,
})

const AgentSkillListSchema = z.array(AgentSkillSchema)
const SkillDataListSchema = z.array(SkillDataSchema)

interface SkillData {
  id: string
  name: string
  slug: string
  display_name: string | null
  description: string | null
  category: string | null
  source: string
  icon: string | null
  version: string | null
}

interface AgentSkill {
  id: string
  agent_id: string
  skill_id: string
  enabled: boolean
  config: Record<string, unknown> | null
  skill: SkillData
}

// Source labels only — visual tone comes from the shared outline Badge variant.
const SOURCE_LABELS: Record<string, string> = {
  BUILTIN: "Built-in",
  BUNDLED: "Bundled",
  CUSTOM: "Custom",
  MARKETPLACE: "Marketplace",
}

const CATEGORY_ICONS: Record<string, React.ElementType> = {
  CODING: Code,
  RESEARCH: Search,
  DEVELOPMENT: Hammer,
  DEVOPS: Server,
  COMMUNICATION: MessageCircle,
  CUSTOM: Settings,
}

function SkillIcon({ category }: { category: string | null }) {
  const Icon = (category && CATEGORY_ICONS[category]) || Blocks
  return (
    <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10">
      <Icon className="h-5 w-5 text-primary" />
    </div>
  )
}

export function SkillsPageClient() {
  const agentId = useAgentId()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [skills, setSkills] = useState<AgentSkill[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [removingId, setRemovingId] = useState<string | null>(null)

  const fetchSkills = useCallback(async () => {
    if (!workspaceId || !agentId) return
    try {
      const res = await fetch(`/api/v1/agents/${agentId}/skills?workspace_id=${workspaceId}`)
      if (!res.ok) {
        setError("Failed to load skills")
        return
      }
      const parsed = AgentSkillListSchema.safeParse(await res.json())
      if (!parsed.success) {
        setError("Invalid skills response")
        setSkills([])
        return
      }
      setSkills(parsed.data)
      setError(null)
    } catch {
      setError("Network error. Please try again.")
    } finally {
      setLoading(false)
    }
  }, [agentId, workspaceId])

  useEffect(() => {
    if (!workspaceId || !agentId) {
      // Flush the previous agent's skills so switching agents (or
      // closing the panel) doesn't leave the old roster visible
      // while the new fetch resolves.
      setSkills([])
      setLoading(false)
      return
    }
    fetchSkills()
  }, [workspaceId, agentId, fetchSkills])

  const handleRemove = async (skillId: string) => {
    if (!workspaceId || !agentId) return
    setRemovingId(skillId)
    try {
      const res = await fetch(
        `/api/v1/agents/${agentId}/skills/${skillId}?workspace_id=${workspaceId}`,
        { method: "DELETE" }
      )
      if (res.ok || res.status === 204) {
        await fetchSkills()
      } else {
        setError("Failed to remove skill")
      }
    } catch {
      setError("Network error removing skill")
    } finally {
      setRemovingId(null)
    }
  }

  if (wsLoading || loading) return <SkillsSkeleton />

  if (error) {
    return (
      <div className="p-4 sm:p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-body">{error}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="p-4 sm:p-6 space-y-6">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h2 className="text-title font-semibold">Skills</h2>
          <p className="text-body text-muted-foreground">
            {skills.length} skill{skills.length !== 1 ? "s" : ""} assigned
          </p>
        </div>
        <Button size="sm" variant="outline" onClick={() => setDialogOpen(true)} disabled={!workspaceId || !agentId}>
          <Plus className="h-4 w-4 mr-1" />
          Add Skill
        </Button>
      </div>

      {skills.length === 0 ? (
        <EmptyState
          icon={Puzzle}
          title="No skills assigned"
          description='Click "Add Skill" to enable agent capabilities.'
        />
      ) : (
        <div className="grid gap-3">
          {skills.map((agentSkill) => {
            const sourceLabel = SOURCE_LABELS[agentSkill.skill.source] ?? agentSkill.skill.source
            return (
              <Card key={agentSkill.id} className="py-0">
                <CardContent className="p-4 sm:p-5">
                  <div className="flex items-start gap-3">
                    <SkillIcon category={agentSkill.skill.category} />
                    <div className="space-y-1 min-w-0 flex-1">
                      <div className="flex items-center gap-2 flex-wrap">
                        <h3 className="text-body font-medium">
                          {agentSkill.skill.display_name ?? agentSkill.skill.name}
                        </h3>
                        {agentSkill.skill.category && (
                          <Badge variant="outline" className="text-micro">{agentSkill.skill.category.charAt(0) + agentSkill.skill.category.slice(1).toLowerCase()}</Badge>
                        )}
                        <Badge variant="outline" className="text-micro border-border bg-muted/40 text-muted-foreground">
                          {sourceLabel}
                        </Badge>
                        {!agentSkill.enabled && (
                          <Badge variant="secondary" className="text-micro">Disabled</Badge>
                        )}
                      </div>
                      {agentSkill.skill.description && (
                        <p className="text-label text-muted-foreground leading-relaxed">{agentSkill.skill.description}</p>
                      )}
                    </div>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="shrink-0 text-muted-foreground hover:text-destructive"
                      onClick={() => handleRemove(agentSkill.skill_id)}
                      disabled={removingId === agentSkill.skill_id}
                    >
                      {removingId === agentSkill.skill_id ? (
                        <Loader2 className="h-4 w-4 animate-spin" />
                      ) : (
                        <Trash2 className="h-4 w-4" />
                      )}
                    </Button>
                  </div>
                </CardContent>
              </Card>
            )
          })}
        </div>
      )}

      {workspaceId && agentId && (
        <AddSkillDialog
          open={dialogOpen}
          onOpenChange={setDialogOpen}
          agentId={agentId}
          workspaceId={workspaceId}
          assignedSkillIds={skills.map((s) => s.skill_id)}
          onAdded={fetchSkills}
        />
      )}
    </div>
  )
}

interface AddSkillDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  agentId: string
  workspaceId: string
  assignedSkillIds: string[]
  onAdded: () => void
}

function AddSkillDialog({ open, onOpenChange, agentId, workspaceId, assignedSkillIds, onAdded }: AddSkillDialogProps) {
  const [available, setAvailable] = useState<SkillData[]>([])
  const [loading, setLoading] = useState(false)
  const [adding, setAdding] = useState<string | null>(null)

  useEffect(() => {
    if (!open || !workspaceId) return
    setLoading(true)
    fetch(`/api/v1/skills?workspace_id=${workspaceId}`)
      .then((res) => (res.ok ? res.json() : []))
      .then((data) => {
        const parsed = SkillDataListSchema.safeParse(data)
        setAvailable(parsed.success ? parsed.data : [])
      })
      .catch(() => setAvailable([]))
      .finally(() => setLoading(false))
  }, [open, workspaceId])

  const unassigned = available.filter((s) => !assignedSkillIds.includes(s.id))

  const [addError, setAddError] = useState<string | null>(null)

  const handleAdd = async (skillId: string) => {
    // Dialog only mounts when both workspaceId and agentId are present,
    // but guard again here to keep the contract on the handler itself
    // so a future caller can't POST to /agents//skills.
    if (!workspaceId || !agentId) return
    setAdding(skillId)
    setAddError(null)
    try {
      const res = await fetch(`/api/v1/agents/${agentId}/skills?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ skill_id: skillId }),
      })
      if (res.ok) {
        onAdded()
        const remaining = available.filter((s) => !assignedSkillIds.includes(s.id) && s.id !== skillId)
        if (remaining.length === 0) onOpenChange(false)
      } else {
        setAddError("Failed to add skill")
      }
    } catch {
      setAddError("Network error adding skill")
    } finally {
      setAdding(null)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="md:max-w-md">
        <DialogHeader>
          <DialogTitle>Add Skill</DialogTitle>
          <DialogDescription>Select a skill to assign to this agent.</DialogDescription>
        </DialogHeader>

        {addError && (
          <div className="flex items-center gap-2 text-destructive text-body">
            <AlertCircle className="h-4 w-4" />
            <span>{addError}</span>
          </div>
        )}

        {loading ? (
          <div className="space-y-3 py-2">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-16 rounded-lg" />
            ))}
          </div>
        ) : unassigned.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-8 text-center">
            <Puzzle className="h-8 w-8 text-muted-foreground/50 mb-2" />
            <p className="text-body text-muted-foreground">
              {available.length === 0 ? "No skills available" : "All skills are already assigned"}
            </p>
          </div>
        ) : (
          <div className="space-y-2 max-h-80 overflow-y-auto py-2">
            {unassigned.map((skill) => (
              <div
                key={skill.id}
                className="flex items-center gap-3 p-3 rounded-lg border hover:bg-muted/50 transition-colors"
              >
                <SkillIcon category={skill.category} />
                <div className="flex-1 min-w-0">
                  <p className="text-body font-medium truncate">{skill.display_name ?? skill.name}</p>
                  {skill.description && (
                    <p className="text-label text-muted-foreground line-clamp-1">{skill.description}</p>
                  )}
                </div>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => handleAdd(skill.id)}
                  disabled={adding === skill.id}
                >
                  {adding === skill.id ? (
                    <Loader2 className="h-3.5 w-3.5 animate-spin" />
                  ) : (
                    <Plus className="h-3.5 w-3.5" />
                  )}
                </Button>
              </div>
            ))}
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>Close</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function SkillsSkeleton() {
  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <div className="flex items-center justify-between">
        <Skeleton className="h-5 w-32" />
        <Skeleton className="h-9 w-24" />
      </div>
      <div className="grid gap-3">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-20 rounded-[var(--radius)]" />
        ))}
      </div>
    </div>
  )
}
