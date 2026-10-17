"use client"

import { useEffect, useState } from "react"
import { Plus } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Badge } from "@/components/ui/badge"
import { toast } from "@/lib/toast"
import { apiFetch } from "@/lib/api-fetch"
import type { WorkflowTemplate } from "@/lib/types/template"

interface LeadAgent {
  id: string
  name: string
  slug: string
}

interface CreateMissionDialogProps {
  crewId: string
  workspaceId: string
  leadAgents: LeadAgent[]
  onCreated: () => void
}

export function CreateMissionDialog({
  crewId,
  workspaceId,
  leadAgents,
  onCreated,
}: CreateMissionDialogProps) {
  const [open, setOpen] = useState(false)
  const [title, setTitle] = useState("")
  const [description, setDescription] = useState("")
  const [leadAgentId, setLeadAgentId] = useState("")
  const [templateId, setTemplateId] = useState<string>("none")
  const [templates, setTemplates] = useState<WorkflowTemplate[]>([])
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!open) return
    apiFetch(`/api/v1/templates?workspace_id=${workspaceId}`)
      .then((res) => (res.ok ? res.json() : []))
      .then(setTemplates)
      .catch(() => {})
  }, [open, workspaceId])

  async function handleSubmit() {
    if (!title.trim()) {
      toast.error("Title is required")
      return
    }
    if (!leadAgentId) {
      toast.error("Lead agent is required")
      return
    }

    setSaving(true)
    try {
      const res = await apiFetch(
        `/api/v1/crews/${crewId}/missions?workspace_id=${workspaceId}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            title: title.trim(),
            description: description.trim() || undefined,
            lead_agent_id: leadAgentId,
            workflow_template: templateId !== "none" ? templateId : undefined,
          }),
        }
      )

      if (!res.ok) {
        const body = await res.json().catch(() => null)
        toast.error(body?.detail ?? "Failed to create mission")
        return
      }

      toast.success("Mission created")
      setOpen(false)
      setTitle("")
      setDescription("")
      setLeadAgentId("")
      setTemplateId("none")
      onCreated()
    } catch {
      toast.error("Failed to create mission")
    } finally {
      setSaving(false)
    }
  }

  const canSubmit = !saving && !!title.trim() && !!leadAgentId

  return (
    <>
      <Button size="sm" className="gap-1" onClick={() => setOpen(true)} aria-haspopup="dialog">
        <Plus className="h-3.5 w-3.5" />
        Create Mission
      </Button>
      <CreateSurface
        open={open}
        onOpenChange={setOpen}
        size="md"
        restoreOpenerFocus
        dirty={!!title.trim() || !!description.trim()}
        discardLabel="this mission"
        onSubmit={() => { if (canSubmit) void handleSubmit() }}
      >
        <CreateSurfaceHeader
          concept="missions"
          title="Create Mission"
          description="Create a new mission for the lead agent to plan and execute."
          onClose={() => setOpen(false)}
        />
        <CreateSurfaceBody className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="mission-title">Title</Label>
            <Input
              id="mission-title"
              placeholder="e.g. Build authentication system"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="mission-description">Description (optional)</Label>
            <Textarea
              id="mission-description"
              placeholder="Describe what this mission should accomplish..."
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={3}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="mission-lead">Lead Agent</Label>
            <Select value={leadAgentId} onValueChange={setLeadAgentId}>
              <SelectTrigger>
                <SelectValue placeholder="Select a lead agent" />
              </SelectTrigger>
              <SelectContent>
                {leadAgents.map((agent) => (
                  <SelectItem key={agent.id} value={agent.id}>
                    @{agent.slug} — {agent.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {leadAgents.length === 0 && (
              <p className="text-label text-muted-foreground">
                No lead agents in this crew. Promote an agent to LEAD role first.
              </p>
            )}
          </div>
          {templates.length > 0 && (
            <div className="space-y-2">
              <Label>Workflow Template (optional)</Label>
              <Select value={templateId} onValueChange={setTemplateId}>
                <SelectTrigger>
                  <SelectValue placeholder="No template" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">No template (lead plans freely)</SelectItem>
                  {templates.map((tmpl) => (
                    <SelectItem key={tmpl.id} value={tmpl.name}>
                      <div className="flex items-center gap-2">
                        <span>{tmpl.name}</span>
                        {tmpl.is_builtin && (
                          <Badge variant="outline" className="text-[9px] px-1 py-0">builtin</Badge>
                        )}
                      </div>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className="text-label text-muted-foreground">
                Templates pre-define task structure. The lead agent can still modify the plan.
              </p>
            </div>
          )}
        </CreateSurfaceBody>
        <CreateSurfaceFooter
          onCancel={() => setOpen(false)}
          primaryLabel={saving ? "Creating..." : "Create Mission"}
          onPrimary={() => void handleSubmit()}
          primaryDisabled={!title.trim() || !leadAgentId}
          busy={saving}
        />
      </CreateSurface>
    </>
  )
}
