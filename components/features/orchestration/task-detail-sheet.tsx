"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  Clock, User, AlertCircle, CheckCircle, Repeat, Copy,
  Play, SkipForward, Loader2, ArrowRight, Pencil, Save, X,
} from "lucide-react"
import {
  Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription,
} from "@/components/ui/sheet"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Separator } from "@/components/ui/separator"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Collapsible, CollapsibleContent, CollapsibleTrigger,
} from "@/components/ui/collapsible"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import type { Mission, MissionTask } from "@/lib/types/mission"

interface TaskDetailSheetProps {
  task: MissionTask | null
  mission: Mission | null
  allTasks: MissionTask[]
  workspaceId: string
  onClose: () => void
  onTaskChanged: () => void
}

const statusStyles: Record<string, { color: string; bg: string; label: string }> = {
  COMPLETED: { color: "text-green-400", bg: "bg-green-500/10 border-green-500/30", label: "Completed" },
  IN_PROGRESS: { color: "text-blue-400", bg: "bg-blue-500/10 border-blue-500/30", label: "Running" },
  FAILED: { color: "text-red-400", bg: "bg-red-500/10 border-red-500/30", label: "Failed" },
  BLOCKED: { color: "text-amber-400", bg: "bg-amber-500/10 border-amber-500/30", label: "Blocked" },
  PENDING: { color: "text-slate-400", bg: "bg-slate-500/10 border-slate-500/30", label: "Pending" },
  SKIPPED: { color: "text-gray-400", bg: "bg-gray-500/10 border-gray-500/30", label: "Skipped" },
}

function LiveDuration({ startedAt }: { startedAt: string }) {
  const [elapsed, setElapsed] = useState("")
  useEffect(() => {
    function update() {
      const diff = Math.floor((Date.now() - new Date(startedAt).getTime()) / 1000)
      if (diff < 60) setElapsed(`${diff}s`)
      else if (diff < 3600) setElapsed(`${Math.floor(diff / 60)}m ${diff % 60}s`)
      else setElapsed(`${Math.floor(diff / 3600)}h ${Math.floor((diff % 3600) / 60)}m`)
    }
    update()
    const interval = setInterval(update, 1000)
    return () => clearInterval(interval)
  }, [startedAt])
  return <span className="font-mono tabular-nums">{elapsed}</span>
}

function formatDuration(ms: number | null): string {
  if (ms == null) return "--"
  if (ms < 1000) return `${ms}ms`
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  return `${Math.floor(s / 60)}m ${s % 60}s`
}

interface Agent {
  id: string
  name: string
  slug: string
}

export function TaskDetailSheet({ task, mission, allTasks, workspaceId, onClose, onTaskChanged }: TaskDetailSheetProps) {
  const [loading, setLoading] = useState<string | null>(null)
  const [editing, setEditing] = useState(false)
  const [editTitle, setEditTitle] = useState("")
  const [editDesc, setEditDesc] = useState("")
  const [editAgentId, setEditAgentId] = useState("")
  const [editDeps, setEditDeps] = useState<string[]>([])
  const [agents, setAgents] = useState<Agent[]>([])

  const isEditable = task?.status === "PENDING" || task?.status === "BLOCKED"

  // Load agents when editing starts
  useEffect(() => {
    if (!editing || agents.length > 0) return
    fetch(`/api/v1/agents?workspace_id=${workspaceId}`)
      .then((r) => r.ok ? r.json() : [])
      .then(setAgents)
      .catch(() => {})
  }, [editing, workspaceId, agents.length])

  const startEditing = useCallback(() => {
    if (!task) return
    setEditTitle(task.title)
    setEditDesc(task.description || "")
    setEditAgentId(task.assigned_agent_id || "")
    try { setEditDeps(JSON.parse(task.depends_on || "[]")) }
    catch { setEditDeps([]) }
    setEditing(true)
  }, [task])

  const cancelEditing = useCallback(() => {
    setEditing(false)
  }, [])

  const saveEdit = useCallback(async () => {
    if (!task || !mission) return
    setLoading("save")
    try {
      const body: Record<string, unknown> = {}
      if (editTitle !== task.title) body.title = editTitle
      if (editDesc !== (task.description || "")) body.description = editDesc
      if (editAgentId !== (task.assigned_agent_id || "")) body.assigned_agent_id = editAgentId || null

      const origDeps: string[] = (() => { try { return JSON.parse(task.depends_on || "[]") } catch { return [] } })()
      if (JSON.stringify(editDeps.sort()) !== JSON.stringify(origDeps.sort())) {
        body.depends_on = JSON.stringify(editDeps)
      }

      if (Object.keys(body).length === 0) {
        setEditing(false)
        return
      }

      const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`
      const res = await fetch(
        `/api/v1/crews/${mission.crew_id}/missions/${mission.id}/tasks/${task.id}${qs}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        }
      )
      if (!res.ok) {
        const b = await res.json().catch(() => null)
        toast.error(b?.detail ?? "Failed to save task")
        return
      }
      toast.success("Task updated")
      setEditing(false)
      onTaskChanged()
    } catch {
      toast.error("Failed to save task")
    } finally {
      setLoading(null)
    }
  }, [task, mission, workspaceId, editTitle, editDesc, editAgentId, editDeps, onTaskChanged])

  const deps = (() => {
    if (!task) return []
    try { return JSON.parse(task.depends_on || "[]") as string[] }
    catch { return [] }
  })()
  const depTasks = allTasks.filter((t) => deps.includes(t.id))
  const dependents = allTasks.filter((t) => {
    try { return (JSON.parse(t.depends_on || "[]") as string[]).includes(task?.id || "") }
    catch { return false }
  })

  const otherTasks = useMemo(
    () => allTasks.filter((t) => t.id !== task?.id),
    [allTasks, task?.id]
  )

  const handleRetry = useCallback(async () => {
    if (!task || !mission) return
    setLoading("retry")
    try {
      const res = await fetch(
        `/api/v1/crews/${mission.crew_id}/missions/${mission.id}/tasks/${task.id}?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ status: "PENDING" }),
        }
      )
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        toast.error(body?.detail ?? "Failed to retry task")
        return
      }
      toast.success("Task queued for retry")
      onTaskChanged()
    } catch {
      toast.error("Failed to retry task")
    } finally {
      setLoading(null)
    }
  }, [task, mission, workspaceId, onTaskChanged])

  const handleSkip = useCallback(async () => {
    if (!task || !mission) return
    setLoading("skip")
    try {
      const res = await fetch(
        `/api/v1/crews/${mission.crew_id}/missions/${mission.id}/tasks/${task.id}?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ status: "SKIPPED" }),
        }
      )
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        toast.error(body?.detail ?? "Failed to skip task")
        return
      }
      toast.success("Task skipped")
      onTaskChanged()
    } catch {
      toast.error("Failed to skip task")
    } finally {
      setLoading(null)
    }
  }, [task, mission, workspaceId, onTaskChanged])

  const style = task ? (statusStyles[task.status] || statusStyles.PENDING) : statusStyles.PENDING

  // Reset editing when task changes
  useEffect(() => { setEditing(false) }, [task?.id])

  return (
    <Sheet open={!!task} onOpenChange={(open) => { if (!open) { setEditing(false); onClose() } }}>
      <SheetContent className="w-[420px] sm:w-[480px] p-0 bg-card border-border">
        {task && (
          <>
            <SheetHeader className="px-6 pt-6 pb-4">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0 flex-1">
                  {editing ? (
                    <Input
                      value={editTitle}
                      onChange={(e) => setEditTitle(e.target.value)}
                      className="text-base font-semibold bg-accent border-border h-9"
                      placeholder="Task title"
                    />
                  ) : (
                    <SheetTitle className="text-base font-semibold text-foreground leading-tight">
                      {task.title}
                    </SheetTitle>
                  )}
                  <SheetDescription className="mt-1">
                    Task #{task.task_order} · {mission?.title}
                  </SheetDescription>
                </div>
                {isEditable && !editing && (
                  <Button variant="ghost" size="icon" className="h-7 w-7 shrink-0 text-muted-foreground/70 hover:text-foreground/70" onClick={startEditing}>
                    <Pencil className="h-3.5 w-3.5" />
                  </Button>
                )}
              </div>
            </SheetHeader>

            <ScrollArea className="h-[calc(100vh-100px)]">
              <div className="px-6 pb-6 space-y-5">
                {/* Status + Controls */}
                <div className="flex items-center gap-2 flex-wrap">
                  <Badge variant="outline" className={cn("gap-1.5", style.bg, style.color)}>
                    {task.status === "IN_PROGRESS" && <Loader2 className="h-3 w-3 animate-spin" />}
                    {style.label}
                  </Badge>
                  {task.max_iterations && task.max_iterations > 1 && (
                    <Badge variant="outline" className="text-xs gap-1 text-muted-foreground">
                      <Repeat className="h-3 w-3" />
                      {task.iteration || 1}/{task.max_iterations}
                    </Badge>
                  )}

                  <div className="flex-1" />

                  {editing ? (
                    <>
                      <Button size="sm" variant="outline" onClick={cancelEditing} disabled={loading !== null}
                        className="gap-1 h-7 text-xs border-border text-muted-foreground">
                        <X className="h-3 w-3" /> Cancel
                      </Button>
                      <Button size="sm" onClick={saveEdit} disabled={loading !== null || !editTitle.trim()}
                        className="gap-1 h-7 text-xs bg-blue-600 hover:bg-blue-700">
                        {loading === "save" ? <Loader2 className="h-3 w-3 animate-spin" /> : <Save className="h-3 w-3" />}
                        Save
                      </Button>
                    </>
                  ) : (
                    <>
                      {task.status === "FAILED" && (
                        <Button size="sm" variant="outline" onClick={handleRetry} disabled={loading !== null}
                          className="gap-1.5 h-7 text-xs border-blue-500/30 text-blue-400 hover:bg-blue-500/10">
                          {loading === "retry" ? <Loader2 className="h-3 w-3 animate-spin" /> : <Play className="h-3 w-3" />}
                          Retry
                        </Button>
                      )}
                      {(task.status === "BLOCKED" || task.status === "PENDING") && (
                        <Button size="sm" variant="outline" onClick={handleSkip} disabled={loading !== null}
                          className="gap-1.5 h-7 text-xs border-gray-500/30 text-gray-400 hover:bg-gray-500/10">
                          {loading === "skip" ? <Loader2 className="h-3 w-3 animate-spin" /> : <SkipForward className="h-3 w-3" />}
                          Skip
                        </Button>
                      )}
                    </>
                  )}
                </div>

                {/* Description — editable or static */}
                {editing ? (
                  <>
                    <Separator className="bg-border" />
                    <div className="space-y-1.5">
                      <label className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Description</label>
                      <Textarea
                        value={editDesc}
                        onChange={(e) => setEditDesc(e.target.value)}
                        placeholder="Task description..."
                        className="min-h-[80px] bg-accent border-border text-sm"
                      />
                    </div>
                  </>
                ) : task.description ? (
                  <>
                    <Separator className="bg-border" />
                    <p className="text-sm text-muted-foreground leading-relaxed">{task.description}</p>
                  </>
                ) : null}

                <Separator className="bg-border" />

                {/* Agent — editable or static */}
                {editing ? (
                  <div className="space-y-1.5">
                    <label className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Assigned Agent</label>
                    <Select value={editAgentId || "unassigned"} onValueChange={(v) => setEditAgentId(v === "unassigned" ? "" : v)}>
                      <SelectTrigger className="h-9 bg-accent border-border text-sm">
                        <SelectValue placeholder="Select agent..." />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="unassigned">Unassigned</SelectItem>
                        {agents.map((a) => (
                          <SelectItem key={a.id} value={a.id}>
                            {a.name} <span className="text-muted-foreground/70 ml-1">@{a.slug}</span>
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                ) : (
                  <div className="space-y-3">
                    <div className="flex items-center gap-2 text-sm">
                      <User className="h-4 w-4 text-muted-foreground/70 shrink-0" />
                      <span className="text-foreground/80">{task.agent_name || "Unassigned"}</span>
                      {task.agent_slug && (
                        <span className="text-xs text-muted-foreground/70 font-mono">@{task.agent_slug}</span>
                      )}
                    </div>

                    <div className="flex items-center gap-2 text-sm">
                      <Clock className="h-4 w-4 text-muted-foreground/70 shrink-0" />
                      {task.status === "IN_PROGRESS" && task.started_at ? (
                        <LiveDuration startedAt={task.started_at} />
                      ) : (
                        <span className="text-foreground/80 font-mono">
                          {task.duration_ms != null ? formatDuration(task.duration_ms) : "--"}
                        </span>
                      )}
                    </div>

                    {(task.token_count != null && task.token_count > 0) && (
                      <div className="flex items-center gap-4 text-xs text-muted-foreground">
                        <span className="font-mono">{task.token_count.toLocaleString()} tokens</span>
                        {task.estimated_cost != null && task.estimated_cost > 0 && (
                          <span className="font-mono">${task.estimated_cost.toFixed(4)}</span>
                        )}
                      </div>
                    )}
                  </div>
                )}

                {/* Dependencies — editable or static */}
                {editing ? (
                  <>
                    <Separator className="bg-border" />
                    <div className="space-y-2">
                      <label className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
                        Dependencies
                      </label>
                      {otherTasks.length === 0 ? (
                        <p className="text-xs text-muted-foreground/70">No other tasks to depend on</p>
                      ) : (
                        <div className="space-y-1.5">
                          {otherTasks.map((t) => (
                            <label key={t.id} className="flex items-center gap-2 px-3 py-2 rounded-lg bg-accent/50 border border-border cursor-pointer hover:bg-accent/50 transition-colors">
                              <Checkbox
                                checked={editDeps.includes(t.id)}
                                onCheckedChange={(checked) => {
                                  setEditDeps((prev) =>
                                    checked ? [...prev, t.id] : prev.filter((d) => d !== t.id)
                                  )
                                }}
                              />
                              <span className="text-xs text-foreground/80 truncate flex-1">{t.title}</span>
                              <span className={cn("text-[10px] font-medium", (statusStyles[t.status] || statusStyles.PENDING).color)}>
                                {(statusStyles[t.status] || statusStyles.PENDING).label}
                              </span>
                            </label>
                          ))}
                        </div>
                      )}
                    </div>
                  </>
                ) : (
                  <>
                    {depTasks.length > 0 && (
                      <>
                        <Separator className="bg-border" />
                        <div className="space-y-2">
                          <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                            Depends on ({depTasks.length})
                          </h4>
                          {depTasks.map((dep) => {
                            const ds = statusStyles[dep.status] || statusStyles.PENDING
                            return (
                              <div key={dep.id} className="flex items-center gap-2 px-3 py-2 rounded-lg bg-accent/50 border border-border">
                                <div className={cn("w-2 h-2 rounded-full shrink-0",
                                  dep.status === "COMPLETED" ? "bg-green-500" :
                                  dep.status === "IN_PROGRESS" ? "bg-blue-500 animate-pulse" :
                                  dep.status === "FAILED" ? "bg-red-500" : "bg-slate-500"
                                )} />
                                <span className="text-xs text-foreground/80 truncate flex-1">{dep.title}</span>
                                <span className={cn("text-[10px] font-medium", ds.color)}>{ds.label}</span>
                              </div>
                            )
                          })}
                        </div>
                      </>
                    )}

                    {dependents.length > 0 && (
                      <>
                        <Separator className="bg-border" />
                        <div className="space-y-2">
                          <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                            Blocks ({dependents.length})
                          </h4>
                          {dependents.map((dep) => {
                            const ds = statusStyles[dep.status] || statusStyles.PENDING
                            return (
                              <div key={dep.id} className="flex items-center gap-2 px-3 py-2 rounded-lg bg-accent/50 border border-border">
                                <ArrowRight className="h-3 w-3 text-muted-foreground/50 shrink-0" />
                                <span className="text-xs text-foreground/80 truncate flex-1">{dep.title}</span>
                                <span className={cn("text-[10px] font-medium", ds.color)}>{ds.label}</span>
                              </div>
                            )
                          })}
                        </div>
                      </>
                    )}
                  </>
                )}

                {/* Output / Result */}
                {!editing && task.result_summary && (
                  <>
                    <Separator className="bg-border" />
                    <Collapsible defaultOpen>
                      <CollapsibleTrigger className="flex items-center gap-2 w-full text-left group">
                        <CheckCircle className="h-4 w-4 text-green-500 shrink-0" />
                        <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider flex-1">
                          Output
                        </h4>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-6 w-6 opacity-0 group-hover:opacity-100"
                          onClick={(e) => {
                            e.stopPropagation()
                            navigator.clipboard.writeText(task.result_summary || "")
                            toast.success("Copied to clipboard")
                          }}
                        >
                          <Copy className="h-3 w-3" />
                        </Button>
                      </CollapsibleTrigger>
                      <CollapsibleContent>
                        <div className="mt-2 p-3 rounded-lg bg-accent/50 border border-border">
                          <pre className="text-xs text-muted-foreground whitespace-pre-wrap font-mono leading-relaxed max-h-[300px] overflow-y-auto">
                            {task.result_summary}
                          </pre>
                        </div>
                      </CollapsibleContent>
                    </Collapsible>
                  </>
                )}

                {/* Error */}
                {!editing && task.error_message && (
                  <>
                    <Separator className="bg-border" />
                    <Collapsible defaultOpen>
                      <CollapsibleTrigger className="flex items-center gap-2 w-full text-left group">
                        <AlertCircle className="h-4 w-4 text-red-500 shrink-0" />
                        <h4 className="text-xs font-semibold text-red-400/70 uppercase tracking-wider flex-1">
                          Error
                        </h4>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-6 w-6 opacity-0 group-hover:opacity-100"
                          onClick={(e) => {
                            e.stopPropagation()
                            navigator.clipboard.writeText(task.error_message || "")
                            toast.success("Copied to clipboard")
                          }}
                        >
                          <Copy className="h-3 w-3" />
                        </Button>
                      </CollapsibleTrigger>
                      <CollapsibleContent>
                        <div className="mt-2 p-3 rounded-lg bg-red-500/5 border border-red-500/10">
                          <pre className="text-xs text-red-400/80 whitespace-pre-wrap font-mono leading-relaxed max-h-[200px] overflow-y-auto">
                            {task.error_message}
                          </pre>
                        </div>
                      </CollapsibleContent>
                    </Collapsible>
                  </>
                )}

                {/* Meta */}
                {!editing && (
                  <>
                    <Separator className="bg-border" />
                    <div className="text-[11px] text-muted-foreground/50 space-y-1 font-mono">
                      <div>ID: {task.id}</div>
                      {task.assignment_id && <div>Assignment: {task.assignment_id}</div>}
                      <div>Created: {new Date(task.created_at).toLocaleString()}</div>
                      {task.started_at && <div>Started: {new Date(task.started_at).toLocaleString()}</div>}
                      {task.completed_at && <div>Completed: {new Date(task.completed_at).toLocaleString()}</div>}
                    </div>
                  </>
                )}
              </div>
            </ScrollArea>
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}
