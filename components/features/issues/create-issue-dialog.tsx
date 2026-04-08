"use client"

import { useEffect, useState } from "react"
import { Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Checkbox } from "@/components/ui/checkbox"
import { LabelBadge } from "./label-badge"
import { AssigneePicker } from "./assignee-picker"
import type { AssigneeOption } from "./assignee-picker"
import { PriorityIcon, priorityLabel } from "./priority-icon"
import { toast } from "sonner"
import type { IssueLabel, IssuePriority } from "@/lib/types/mission"

interface Crew {
  id: string
  name: string
  slug: string
}

interface CreateIssueDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  crews: Crew[]
  labels: IssueLabel[]
  onCreated: () => void
  workspaceId: string
}

const PRIORITIES: IssuePriority[] = ["urgent", "high", "medium", "low", "none"]

export function CreateIssueDialog({
  open,
  onOpenChange,
  crews,
  labels,
  onCreated,
  workspaceId,
}: CreateIssueDialogProps) {
  const [crewId, setCrewId] = useState("")
  const [title, setTitle] = useState("")
  const [description, setDescription] = useState("")
  const [priority, setPriority] = useState<IssuePriority>("none")
  const [selectedLabels, setSelectedLabels] = useState<string[]>([])
  const [assigneeType, setAssigneeType] = useState<"user" | "agent" | null>(null)
  const [assigneeId, setAssigneeId] = useState<string | null>(null)
  const [agents, setAgents] = useState<AssigneeOption[]>([])
  const [saving, setSaving] = useState(false)

  // Fetch agents when crew changes
  useEffect(() => {
    if (!crewId) {
      setAgents([])
      return
    }
    let cancelled = false
    async function fetchAgents() {
      try {
        const res = await fetch(
          `/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}&crew_id=${encodeURIComponent(crewId)}`,
        )
        if (!res.ok || cancelled) return
        const data = await res.json()
        const list = Array.isArray(data) ? data : data.agents ?? []
        if (!cancelled) {
          setAgents(
            list.map((a: { id: string; name: string; slug?: string }) => ({
              id: a.id,
              name: a.name,
              type: "agent" as const,
              slug: a.slug,
            })),
          )
        }
      } catch {
        // ignore
      }
    }
    fetchAgents()
    return () => {
      cancelled = true
    }
  }, [crewId, workspaceId])

  function reset() {
    setCrewId("")
    setTitle("")
    setDescription("")
    setPriority("none")
    setSelectedLabels([])
    setAssigneeType(null)
    setAssigneeId(null)
  }

  function toggleLabel(labelId: string) {
    setSelectedLabels((prev) =>
      prev.includes(labelId)
        ? prev.filter((id) => id !== labelId)
        : [...prev, labelId],
    )
  }

  async function handleSubmit() {
    if (!crewId) {
      toast.error("Please select a crew")
      return
    }
    if (!title.trim()) {
      toast.error("Title is required")
      return
    }

    setSaving(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/issues?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            title: title.trim(),
            description: description.trim() || undefined,
            priority,
            labels: selectedLabels.length > 0 ? selectedLabels : undefined,
            assignee_type: assigneeType ?? undefined,
            assignee_id: assigneeId ?? undefined,
          }),
        },
      )

      if (!res.ok) {
        const body = await res.json().catch(() => null)
        toast.error(body?.detail ?? "Failed to create issue")
        return
      }

      toast.success("Issue created")
      reset()
      onOpenChange(false)
      onCreated()
    } catch {
      toast.error("Failed to create issue")
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[520px]">
        <DialogHeader>
          <DialogTitle>New Issue</DialogTitle>
        </DialogHeader>

        <div className="space-y-4 py-2">
          {/* Crew */}
          <div className="space-y-2">
            <Label htmlFor="issue-crew">Crew</Label>
            <Select value={crewId} onValueChange={setCrewId}>
              <SelectTrigger id="issue-crew">
                <SelectValue placeholder="Select a crew" />
              </SelectTrigger>
              <SelectContent>
                {crews.map((crew) => (
                  <SelectItem key={crew.id} value={crew.id}>
                    {crew.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {crews.length === 0 && (
              <p className="text-xs text-muted-foreground">
                No crews available. Create a crew first.
              </p>
            )}
          </div>

          {/* Title */}
          <div className="space-y-2">
            <Label htmlFor="issue-title">Title</Label>
            <Input
              id="issue-title"
              placeholder="Issue title"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
            />
          </div>

          {/* Description */}
          <div className="space-y-2">
            <Label htmlFor="issue-description">Description</Label>
            <Textarea
              id="issue-description"
              placeholder="Add a description..."
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={3}
            />
          </div>

          {/* Priority */}
          <div className="space-y-2">
            <Label>Priority</Label>
            <Select
              value={priority}
              onValueChange={(v) => setPriority(v as IssuePriority)}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {PRIORITIES.map((p) => (
                  <SelectItem key={p} value={p}>
                    <div className="flex items-center gap-2">
                      <PriorityIcon priority={p} className="h-3.5 w-3.5" />
                      <span>{priorityLabel[p]}</span>
                    </div>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {/* Assignee */}
          {crewId && (
            <div className="space-y-2">
              <Label>Assignee</Label>
              <AssigneePicker
                value={{ type: assigneeType, id: assigneeId }}
                onChange={(type, id) => {
                  setAssigneeType(type)
                  setAssigneeId(id)
                }}
                agents={agents}
                users={[]}
                className="w-full"
              />
            </div>
          )}

          {/* Labels */}
          {labels.length > 0 && (
            <div className="space-y-2">
              <Label>Labels</Label>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button
                    variant="outline"
                    className="w-full justify-start text-sm h-9 font-normal"
                  >
                    {selectedLabels.length === 0 ? (
                      <span className="text-muted-foreground">Select labels...</span>
                    ) : (
                      <div className="flex items-center gap-1 flex-wrap">
                        {selectedLabels.map((id) => {
                          const label = labels.find((l) => l.id === id)
                          return label ? (
                            <LabelBadge key={id} label={label} />
                          ) : null
                        })}
                      </div>
                    )}
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start" className="w-64">
                  {labels.map((label) => (
                    <DropdownMenuItem
                      key={label.id}
                      onSelect={(e) => e.preventDefault()}
                      onClick={() => toggleLabel(label.id)}
                      className="gap-2"
                    >
                      <Checkbox
                        checked={selectedLabels.includes(label.id)}
                        className="pointer-events-none"
                      />
                      <LabelBadge label={label} />
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          )}
        </div>

        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={saving}
          >
            Cancel
          </Button>
          <Button
            onClick={handleSubmit}
            disabled={saving || !title.trim() || !crewId}
          >
            {saving && <Loader2 className="mr-2 h-3.5 w-3.5 animate-spin" />}
            {saving ? "Creating..." : "Create Issue"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
