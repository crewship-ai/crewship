"use client"

import { useCallback, useEffect, useState } from "react"
import {
  Plus, Trash2, GripVertical, Loader2, Link2,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Dialog, DialogContent, DialogDescription, DialogFooter,
  DialogHeader, DialogTitle, DialogTrigger,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { Badge } from "@/components/ui/badge"
import { Separator } from "@/components/ui/separator"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Checkbox } from "@/components/ui/checkbox"
import { toast } from "sonner"

interface Crew {
  id: string
  name: string
  slug: string
}

interface Agent {
  id: string
  name: string
  slug: string
  agent_role: string
  crew_id: string
}

interface TaskDraft {
  key: string
  title: string
  description: string
  agentId: string
  dependsOnKeys: string[]
}

interface CreateMissionWizardProps {
  workspaceId: string
  onCreated: () => void
}

let nextKey = 1
function genKey() { return `task-${nextKey++}` }

export function CreateMissionWizard({ workspaceId, onCreated }: CreateMissionWizardProps) {
  const [open, setOpen] = useState(false)
  const [crews, setCrews] = useState<Crew[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [crewId, setCrewId] = useState("")
  const [title, setTitle] = useState("")
  const [description, setDescription] = useState("")
  const [tasks, setTasks] = useState<TaskDraft[]>([])
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!open) return
    Promise.all([
      fetch(`/api/v1/crews?workspace_id=${workspaceId}`).then((r) => r.ok ? r.json() : []),
      fetch(`/api/v1/agents?workspace_id=${workspaceId}`).then((r) => r.ok ? r.json() : []),
    ]).then(([c, a]) => {
      setCrews(c)
      setAgents(a)
    }).catch(() => {})
  }, [open, workspaceId])

  const leadAgent = agents.find((a) => a.crew_id === crewId && a.agent_role === "LEAD")
  const availableAgents = agents.filter((a) => a.crew_id === crewId || true) // all agents (cross-crew possible)

  const addTask = useCallback(() => {
    setTasks((prev) => [...prev, { key: genKey(), title: "", description: "", agentId: "", dependsOnKeys: [] }])
  }, [])

  const removeTask = useCallback((key: string) => {
    setTasks((prev) => prev
      .filter((t) => t.key !== key)
      .map((t) => ({ ...t, dependsOnKeys: t.dependsOnKeys.filter((k) => k !== key) }))
    )
  }, [])

  const updateTask = useCallback((key: string, updates: Partial<TaskDraft>) => {
    setTasks((prev) => prev.map((t) => t.key === key ? { ...t, ...updates } : t))
  }, [])

  const toggleDep = useCallback((taskKey: string, depKey: string) => {
    setTasks((prev) => prev.map((t) => {
      if (t.key !== taskKey) return t
      const has = t.dependsOnKeys.includes(depKey)
      return { ...t, dependsOnKeys: has ? t.dependsOnKeys.filter((k) => k !== depKey) : [...t.dependsOnKeys, depKey] }
    }))
  }, [])

  async function handleSubmit() {
    if (!title.trim()) { toast.error("Mission title is required"); return }
    if (!crewId) { toast.error("Select a crew"); return }
    if (!leadAgent) { toast.error("Selected crew has no LEAD agent"); return }

    const invalidTasks = tasks.filter((t) => !t.title.trim())
    if (invalidTasks.length > 0) { toast.error("All tasks need a title"); return }

    setSaving(true)
    try {
      // Create mission
      const missionRes = await fetch(`/api/v1/crews/${crewId}/missions?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          title: title.trim(),
          description: description.trim() || undefined,
          lead_agent_id: leadAgent.id,
        }),
      })
      if (!missionRes.ok) {
        const body = await missionRes.json().catch(() => null)
        toast.error(body?.detail ?? "Failed to create mission")
        return
      }
      const mission = await missionRes.json()

      // Create tasks sequentially (need IDs for dependencies)
      const keyToId = new Map<string, string>()
      for (let i = 0; i < tasks.length; i++) {
        const t = tasks[i]
        const depIds = t.dependsOnKeys.map((k) => keyToId.get(k)).filter(Boolean) as string[]
        const taskBody: Record<string, unknown> = {
          title: t.title.trim(),
          task_order: i + 1,
        }
        if (t.description.trim()) taskBody.description = t.description.trim()
        if (t.agentId) taskBody.assigned_agent_id = t.agentId
        if (depIds.length > 0) taskBody.depends_on = depIds

        const taskRes = await fetch(
          `/api/v1/crews/${crewId}/missions/${mission.id}/tasks?workspace_id=${workspaceId}`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(taskBody),
          }
        )
        if (taskRes.ok) {
          const created = await taskRes.json()
          keyToId.set(t.key, created.id)
        }
      }

      toast.success(`Mission created with ${tasks.length} tasks`)
      setOpen(false)
      resetForm()
      onCreated()
    } catch {
      toast.error("Failed to create mission")
    } finally {
      setSaving(false)
    }
  }

  function resetForm() {
    setTitle("")
    setDescription("")
    setCrewId("")
    setTasks([])
  }

  return (
    <Dialog open={open} onOpenChange={(v) => { setOpen(v); if (!v) resetForm() }}>
      <DialogTrigger asChild>
        <Button size="sm" className="gap-1.5">
          <Plus className="h-3.5 w-3.5" />
          New Mission
        </Button>
      </DialogTrigger>
      <DialogContent className="max-w-2xl max-h-[85vh] flex flex-col">
        <DialogHeader>
          <DialogTitle>Create Mission</DialogTitle>
          <DialogDescription>
            Define a mission with tasks, assign agents, and set dependencies.
          </DialogDescription>
        </DialogHeader>

        <ScrollArea className="flex-1 -mx-6 px-6">
          <div className="space-y-5 py-2">
            {/* Mission basics */}
            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label>Crew</Label>
                <Select value={crewId} onValueChange={setCrewId}>
                  <SelectTrigger>
                    <SelectValue placeholder="Select crew" />
                  </SelectTrigger>
                  <SelectContent>
                    {crews.map((c) => (
                      <SelectItem key={c.id} value={c.id}>{c.name} ({c.slug})</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label>Lead Agent</Label>
                <div className="h-9 px-3 flex items-center rounded-md border border-input bg-muted/50 text-sm">
                  {leadAgent ? (
                    <span>@{leadAgent.slug} — {leadAgent.name}</span>
                  ) : (
                    <span className="text-muted-foreground">
                      {crewId ? "No LEAD agent in crew" : "Select a crew first"}
                    </span>
                  )}
                </div>
              </div>
            </div>

            <div className="space-y-2">
              <Label>Mission Title</Label>
              <Input
                placeholder="e.g. Build authentication system"
                value={title}
                onChange={(e) => setTitle(e.target.value)}
              />
            </div>

            <div className="space-y-2">
              <Label>Description (optional)</Label>
              <Textarea
                placeholder="Describe what this mission should accomplish..."
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                rows={2}
              />
            </div>

            <Separator />

            {/* Task builder */}
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <Label className="text-base font-semibold">Tasks ({tasks.length})</Label>
                <Button size="sm" variant="outline" onClick={addTask} className="gap-1.5 h-7 text-xs">
                  <Plus className="h-3 w-3" /> Add Task
                </Button>
              </div>

              {tasks.length === 0 && (
                <div className="text-center py-8 text-sm text-muted-foreground border border-dashed rounded-lg">
                  No tasks yet. Click &quot;Add Task&quot; to build the workflow.
                </div>
              )}

              {tasks.map((task, idx) => (
                <div key={task.key} className="rounded-lg border bg-card p-4 space-y-3">
                  <div className="flex items-center gap-2">
                    <GripVertical className="h-4 w-4 text-muted-foreground shrink-0 cursor-grab" />
                    <Badge variant="outline" className="shrink-0 text-[10px]">#{idx + 1}</Badge>
                    <Input
                      placeholder="Task title"
                      value={task.title}
                      onChange={(e) => updateTask(task.key, { title: e.target.value })}
                      className="h-8 text-sm"
                    />
                    <Button
                      size="icon"
                      variant="ghost"
                      className="h-7 w-7 shrink-0 text-muted-foreground hover:text-red-400"
                      onClick={() => removeTask(task.key)}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </div>

                  <div className="grid grid-cols-2 gap-3 pl-6">
                    <div className="space-y-1">
                      <Label className="text-xs text-muted-foreground">Assign to</Label>
                      <Select value={task.agentId || "none"} onValueChange={(v) => updateTask(task.key, { agentId: v === "none" ? "" : v })}>
                        <SelectTrigger className="h-8 text-xs">
                          <SelectValue placeholder="Unassigned" />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="none">Unassigned</SelectItem>
                          {availableAgents.map((a) => {
                            const crewName = crews.find((c) => c.id === a.crew_id)?.slug
                            return (
                              <SelectItem key={a.id} value={a.id}>
                                @{a.slug}
                                {crewName && a.crew_id !== crewId && (
                                  <span className="text-muted-foreground ml-1">({crewName})</span>
                                )}
                              </SelectItem>
                            )
                          })}
                        </SelectContent>
                      </Select>
                    </div>

                    {idx > 0 && (
                      <div className="space-y-1">
                        <Label className="text-xs text-muted-foreground flex items-center gap-1">
                          <Link2 className="h-3 w-3" /> Depends on
                        </Label>
                        <div className="space-y-1">
                          {tasks.slice(0, idx).map((prev) => (
                            <label key={prev.key} className="flex items-center gap-2 text-xs cursor-pointer">
                              <Checkbox
                                checked={task.dependsOnKeys.includes(prev.key)}
                                onCheckedChange={() => toggleDep(task.key, prev.key)}
                                className="h-3.5 w-3.5"
                              />
                              <span className="truncate text-muted-foreground">
                                #{tasks.indexOf(prev) + 1} {prev.title || "Untitled"}
                              </span>
                            </label>
                          ))}
                        </div>
                      </div>
                    )}
                  </div>
                </div>
              ))}
            </div>
          </div>
        </ScrollArea>

        <DialogFooter className="pt-4 border-t">
          <Button variant="outline" onClick={() => setOpen(false)}>Cancel</Button>
          <Button
            onClick={handleSubmit}
            disabled={saving || !title.trim() || !crewId || !leadAgent}
          >
            {saving ? (
              <><Loader2 className="h-3.5 w-3.5 animate-spin mr-1.5" /> Creating...</>
            ) : (
              <>Create Mission{tasks.length > 0 && ` with ${tasks.length} tasks`}</>
            )}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
