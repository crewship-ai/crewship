"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import {
  X, Loader2, Paperclip, ChevronRight, User, Bot, UserX, Check,
  Tag, FolderKanban, ScrollText,
} from "lucide-react"
import type { Pipeline } from "@/hooks/use-pipelines"
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"
import { Checkbox } from "@/components/ui/checkbox"
import { Switch } from "@/components/ui/switch"
import { LabelBadge } from "@/components/features/issues/label-badge"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { StatusIcon } from "@/components/features/issues/status-icon"
import type { AssigneeOption } from "@/components/features/issues/assignee-picker"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import type { IssueLabel, IssuePriority, Project } from "@/lib/types/mission"
import type { CrewSummary } from "@/lib/types/orchestration"

const PRIORITIES: IssuePriority[] = ["urgent", "high", "medium", "low", "none"]

interface CreateIssueModalProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  crews: CrewSummary[]
  labels: IssueLabel[]
  projects: Project[]
  // Routines available to bind to the new issue. Optional — if the
  // host page hasn't loaded pipelines yet, the picker simply renders
  // an empty Command list.
  routines?: Pipeline[]
  workspaceId: string
  onCreated: () => void
}

export function CreateIssueModal({
  open,
  onOpenChange,
  crews,
  labels,
  projects,
  routines = [],
  workspaceId,
  onCreated,
}: CreateIssueModalProps) {
  const [crewId, setCrewId] = useState("")
  const [title, setTitle] = useState("")
  const [description, setDescription] = useState("")
  const [priority, setPriority] = useState<IssuePriority>("none")
  const [assigneeType, setAssigneeType] = useState<"user" | "agent" | null>(null)
  const [assigneeId, setAssigneeId] = useState<string | null>(null)
  const [projectId, setProjectId] = useState<string | null>(null)
  const [selectedLabels, setSelectedLabels] = useState<string[]>([])
  const [routineId, setRoutineId] = useState<string | null>(null)
  const [agents, setAgents] = useState<AssigneeOption[]>([])
  const [createMore, setCreateMore] = useState(false)
  const [saving, setSaving] = useState(false)

  // Popover states
  const [crewOpen, setCrewOpen] = useState(false)
  const [priorityOpen, setPriorityOpen] = useState(false)
  const [assigneeOpen, setAssigneeOpen] = useState(false)
  const [projectOpen, setProjectOpen] = useState(false)
  const [labelsOpen, setLabelsOpen] = useState(false)
  const [routineOpen, setRoutineOpen] = useState(false)

  const titleRef = useRef<HTMLInputElement>(null)

  // Auto-select first crew when opening
  useEffect(() => {
    if (open && !crewId && crews.length > 0) {
      setCrewId(crews[0].id)
    }
  }, [open, crewId, crews])

  // Focus title on open
  useEffect(() => {
    if (open) {
      setTimeout(() => titleRef.current?.focus(), 100)
    }
  }, [open])

  // Fetch agents when crew changes
  useEffect(() => {
    setAssigneeType(null)
    setAssigneeId(null)
    if (!crewId) { setAgents([]); return }
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
              id: a.id, name: a.name, type: "agent" as const, slug: a.slug,
            })),
          )
        }
      } catch { /* ignore */ }
    }
    fetchAgents()
    return () => { cancelled = true }
  }, [crewId, workspaceId])

  function reset() {
    setTitle("")
    setDescription("")
    setPriority("none")
    setAssigneeType(null)
    setAssigneeId(null)
    setProjectId(null)
    setSelectedLabels([])
    setRoutineId(null)
  }

  const selectedCrew = crews.find((c) => c.id === crewId)
  const crewPrefix = selectedCrew?.slug?.toUpperCase().slice(0, 3) ?? "CRE"
  const selectedProject = projects.find((p) => p.id === projectId)
  const selectedRoutine = routines.find((r) => r.id === routineId)
  const assigneeName = (() => {
    if (!assigneeId) return null
    const found = agents.find((a) => a.id === assigneeId)
    return found?.name ?? null
  })()

  const handleSubmit = useCallback(async () => {
    if (!crewId) { toast.error("Please select a crew"); return }
    if (!title.trim()) { toast.error("Title is required"); return }

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
            project_id: projectId ?? undefined,
            routine_id: routineId ?? undefined,
          }),
        },
      )

      if (!res.ok) {
        const body = await res.json().catch(() => null)
        toast.error(body?.detail ?? "Failed to create issue")
        return
      }

      toast.success("Issue created")
      onCreated()

      if (createMore) {
        reset()
        setTimeout(() => titleRef.current?.focus(), 50)
      } else {
        reset()
        onOpenChange(false)
      }
    } catch {
      toast.error("Failed to create issue")
    } finally {
      setSaving(false)
    }
  }, [crewId, title, description, priority, selectedLabels, assigneeType, assigneeId, projectId, routineId, workspaceId, onCreated, createMore, onOpenChange])

  // Cmd+Enter to submit
  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
      e.preventDefault()
      handleSubmit()
    }
  }, [handleSubmit])

  function toggleLabel(labelId: string) {
    setSelectedLabels((prev) =>
      prev.includes(labelId) ? prev.filter((id) => id !== labelId) : [...prev, labelId],
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        className="sm:max-w-[640px] p-0 gap-0 overflow-hidden"
        onKeyDown={handleKeyDown}
        onOpenAutoFocus={(e) => e.preventDefault()}
      >
        <DialogTitle className="sr-only">New Issue</DialogTitle>

        {/* ── Header ── */}
        <div className="flex items-center gap-1.5 px-4 py-3 border-b border-white/[0.06]">
          {/* Crew selector */}
          <Popover open={crewOpen} onOpenChange={setCrewOpen}>
            <PopoverTrigger asChild>
              <button className="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors">
                <span className="font-medium">{crewPrefix}</span>
              </button>
            </PopoverTrigger>
            <PopoverContent className="w-[200px] p-0" align="start">
              <Command>
                <CommandInput placeholder="Select crew..." className="h-8 text-xs" />
                <CommandList>
                  <CommandEmpty>No crews found.</CommandEmpty>
                  <CommandGroup>
                    {crews.map((crew) => (
                      <CommandItem
                        key={crew.id}
                        onSelect={() => { setCrewId(crew.id); setCrewOpen(false) }}
                      >
                        <span className="text-xs">{crew.name}</span>
                        {crewId === crew.id && <Check className="ml-auto h-3.5 w-3.5" />}
                      </CommandItem>
                    ))}
                  </CommandGroup>
                </CommandList>
              </Command>
            </PopoverContent>
          </Popover>

          <ChevronRight className="h-3 w-3 text-muted-foreground/50" />
          <span className="text-xs text-muted-foreground">New issue</span>

          <div className="flex-1" />

          <button
            onClick={() => onOpenChange(false)}
            className="h-6 w-6 rounded-md flex items-center justify-center text-muted-foreground hover:text-foreground hover:bg-white/[0.06] transition-colors"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>

        {/* ── Body ── */}
        <div className="px-5 pt-4 pb-2 space-y-1">
          {/* Title */}
          <input
            ref={titleRef}
            type="text"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="Issue title"
            className="w-full bg-transparent text-base font-medium text-foreground placeholder:text-muted-foreground/50 outline-none"
          />

          {/* Description */}
          <textarea
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="Add description..."
            rows={3}
            className="w-full bg-transparent text-sm text-muted-foreground placeholder:text-muted-foreground/40 outline-none resize-none mt-1"
          />
        </div>

        {/* ── Metadata pills ── */}
        <div className="px-5 py-2 flex items-center gap-1.5 flex-wrap border-t border-white/[0.04]">
          {/* Status (read-only) */}
          <div className="h-7 px-2.5 rounded-md text-xs text-muted-foreground bg-white/[0.04] border border-white/[0.06] flex items-center gap-1.5">
            <StatusIcon status="BACKLOG" className="h-3.5 w-3.5" />
            <span>Backlog</span>
          </div>

          {/* Priority */}
          <Popover open={priorityOpen} onOpenChange={setPriorityOpen}>
            <PopoverTrigger asChild>
              <button className={cn(
                "h-7 px-2.5 rounded-md text-xs bg-white/[0.04] hover:bg-white/[0.08] border border-white/[0.06] flex items-center gap-1.5 transition-colors",
                priority !== "none" ? "text-foreground/80" : "text-muted-foreground",
              )}>
                <PriorityIcon priority={priority} className="h-3.5 w-3.5" />
                <span>{priorityLabel[priority]}</span>
              </button>
            </PopoverTrigger>
            <PopoverContent className="w-[180px] p-1" align="start">
              {PRIORITIES.map((p) => (
                <button
                  key={p}
                  onClick={() => { setPriority(p); setPriorityOpen(false) }}
                  className={cn(
                    "w-full flex items-center gap-2 px-2 py-1.5 rounded-md text-xs hover:bg-white/[0.08] transition-colors",
                    priority === p ? "text-foreground bg-white/[0.06]" : "text-muted-foreground",
                  )}
                >
                  <PriorityIcon priority={p} className="h-3.5 w-3.5" />
                  <span>{priorityLabel[p]}</span>
                  {priority === p && <Check className="ml-auto h-3 w-3" />}
                </button>
              ))}
            </PopoverContent>
          </Popover>

          {/* Assignee */}
          {crewId && (
            <Popover open={assigneeOpen} onOpenChange={setAssigneeOpen}>
              <PopoverTrigger asChild>
                <button className={cn(
                  "h-7 px-2.5 rounded-md text-xs bg-white/[0.04] hover:bg-white/[0.08] border border-white/[0.06] flex items-center gap-1.5 transition-colors",
                  assigneeId ? "text-foreground/80" : "text-muted-foreground",
                )}>
                  {assigneeType === "agent" && <Bot className="h-3 w-3" />}
                  {assigneeType === "user" && <User className="h-3 w-3" />}
                  {!assigneeType && <User className="h-3 w-3" />}
                  <span>{assigneeName ?? "Assignee"}</span>
                </button>
              </PopoverTrigger>
              <PopoverContent className="w-[220px] p-0" align="start">
                <Command>
                  <CommandInput placeholder="Search assignee..." className="h-8 text-xs" />
                  <CommandList>
                    <CommandEmpty>No results found.</CommandEmpty>
                    <CommandGroup>
                      <CommandItem onSelect={() => { setAssigneeType(null); setAssigneeId(null); setAssigneeOpen(false) }}>
                        <UserX className="mr-2 h-3.5 w-3.5 text-muted-foreground" />
                        <span className="text-xs">Unassigned</span>
                        {!assigneeId && <Check className="ml-auto h-3.5 w-3.5" />}
                      </CommandItem>
                    </CommandGroup>
                    {agents.length > 0 && (
                      <CommandGroup heading="Agents">
                        {agents.map((agent) => (
                          <CommandItem
                            key={agent.id}
                            onSelect={() => { setAssigneeType("agent"); setAssigneeId(agent.id); setAssigneeOpen(false) }}
                          >
                            <Bot className="mr-2 h-3.5 w-3.5 text-muted-foreground" />
                            <span className="text-xs">{agent.name}</span>
                            {assigneeId === agent.id && <Check className="ml-auto h-3.5 w-3.5" />}
                          </CommandItem>
                        ))}
                      </CommandGroup>
                    )}
                  </CommandList>
                </Command>
              </PopoverContent>
            </Popover>
          )}

          {/* Project */}
          <Popover open={projectOpen} onOpenChange={setProjectOpen}>
            <PopoverTrigger asChild>
              <button className={cn(
                "h-7 px-2.5 rounded-md text-xs bg-white/[0.04] hover:bg-white/[0.08] border border-white/[0.06] flex items-center gap-1.5 transition-colors",
                projectId ? "text-foreground/80" : "text-muted-foreground",
              )}>
                <FolderKanban className="h-3 w-3" />
                <span>{selectedProject?.name ?? "Project"}</span>
              </button>
            </PopoverTrigger>
            <PopoverContent className="w-[220px] p-0" align="start">
              <Command>
                <CommandInput placeholder="Search project..." className="h-8 text-xs" />
                <CommandList>
                  <CommandEmpty>No projects found.</CommandEmpty>
                  <CommandGroup>
                    <CommandItem onSelect={() => { setProjectId(null); setProjectOpen(false) }}>
                      <span className="text-xs text-muted-foreground">No project</span>
                      {!projectId && <Check className="ml-auto h-3.5 w-3.5" />}
                    </CommandItem>
                    {projects.map((p) => (
                      <CommandItem
                        key={p.id}
                        onSelect={() => { setProjectId(p.id); setProjectOpen(false) }}
                      >
                        <FolderKanban className="mr-2 h-3.5 w-3.5 text-muted-foreground" />
                        <span className="text-xs">{p.name}</span>
                        {projectId === p.id && <Check className="ml-auto h-3.5 w-3.5" />}
                      </CommandItem>
                    ))}
                  </CommandGroup>
                </CommandList>
              </Command>
            </PopoverContent>
          </Popover>

          {/* Routine — bind a saved routine to handle this issue */}
          <Popover open={routineOpen} onOpenChange={setRoutineOpen}>
            <PopoverTrigger asChild>
              <button className={cn(
                "h-7 px-2.5 rounded-md text-xs bg-white/[0.04] hover:bg-white/[0.08] border border-white/[0.06] flex items-center gap-1.5 transition-colors",
                routineId ? "text-foreground/80" : "text-muted-foreground",
              )}>
                <ScrollText className="h-3 w-3" />
                <span>{selectedRoutine?.name ?? "Routine"}</span>
              </button>
            </PopoverTrigger>
            <PopoverContent className="w-[280px] p-0" align="start">
              <Command>
                <CommandInput placeholder="Search routines..." className="h-8 text-xs" />
                <CommandList>
                  <CommandEmpty>No routines yet — create one in /routines.</CommandEmpty>
                  <CommandGroup>
                    <CommandItem onSelect={() => { setRoutineId(null); setRoutineOpen(false) }}>
                      <span className="text-xs text-muted-foreground">No routine</span>
                      {!routineId && <Check className="ml-auto h-3.5 w-3.5" />}
                    </CommandItem>
                    {routines.map((r) => (
                      <CommandItem
                        key={r.id}
                        onSelect={() => { setRoutineId(r.id); setRoutineOpen(false) }}
                      >
                        <ScrollText className="mr-2 h-3.5 w-3.5 text-muted-foreground" />
                        <div className="min-w-0 flex-1">
                          <div className="text-xs font-medium truncate">{r.name}</div>
                          <div className="text-[10px] text-muted-foreground truncate">{r.slug}</div>
                        </div>
                        {routineId === r.id && <Check className="ml-2 h-3.5 w-3.5 shrink-0" />}
                      </CommandItem>
                    ))}
                  </CommandGroup>
                </CommandList>
              </Command>
            </PopoverContent>
          </Popover>

          {/* Labels */}
          {labels.length > 0 && (
            <Popover open={labelsOpen} onOpenChange={setLabelsOpen}>
              <PopoverTrigger asChild>
                <button className={cn(
                  "h-7 px-2.5 rounded-md text-xs bg-white/[0.04] hover:bg-white/[0.08] border border-white/[0.06] flex items-center gap-1.5 transition-colors",
                  selectedLabels.length > 0 ? "text-foreground/80" : "text-muted-foreground",
                )}>
                  <Tag className="h-3 w-3" />
                  <span>{selectedLabels.length > 0 ? `${selectedLabels.length} label${selectedLabels.length > 1 ? "s" : ""}` : "Labels"}</span>
                </button>
              </PopoverTrigger>
              <PopoverContent className="w-[240px] p-1" align="start">
                <div className="max-h-[200px] overflow-y-auto">
                  {labels.map((label) => (
                    <button
                      key={label.id}
                      onClick={() => toggleLabel(label.id)}
                      className="w-full flex items-center gap-2 px-2 py-1.5 rounded-md text-xs hover:bg-white/[0.08] transition-colors"
                    >
                      <Checkbox
                        checked={selectedLabels.includes(label.id)}
                        className="pointer-events-none h-3.5 w-3.5"
                      />
                      <LabelBadge label={label} />
                    </button>
                  ))}
                </div>
              </PopoverContent>
            </Popover>
          )}
        </div>

        {/* ── Footer ── */}
        <div className="flex items-center justify-between px-5 py-3 border-t border-white/[0.06]">
          <div className="flex items-center gap-3">
            <Paperclip className="h-3.5 w-3.5 text-muted-foreground/40" />
          </div>

          <div className="flex items-center gap-3">
            <label className="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer">
              <Switch
                size="sm"
                checked={createMore}
                onCheckedChange={setCreateMore}
              />
              Create more
            </label>
            <button
              onClick={handleSubmit}
              disabled={saving || !title.trim() || !crewId}
              className="h-7 px-3 rounded-md text-xs font-medium bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50 disabled:pointer-events-none flex items-center gap-1.5 transition-colors"
            >
              {saving && <Loader2 className="h-3 w-3 animate-spin" />}
              Create issue
            </button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
