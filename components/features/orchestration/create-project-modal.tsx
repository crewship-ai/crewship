"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import {
  X, Loader2, ChevronRight, Check, User, Bot, UserX,
  Tag, Calendar, Rocket, Code, Clipboard, Briefcase, Zap,
  Target, Shield, Database, Globe,
} from "lucide-react"
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
import { LabelBadge } from "@/components/features/issues/label-badge"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { TiptapEditor } from "@/components/features/issues/tiptap-editor"
import type { AssigneeOption } from "@/components/features/issues/assignee-picker"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import type { IssueLabel, IssuePriority, ProjectStatus } from "@/lib/types/mission"
import type { CrewSummary } from "@/lib/types/orchestration"

const PRIORITIES: IssuePriority[] = ["urgent", "high", "medium", "low", "none"]

const PROJECT_STATUSES: { value: ProjectStatus; label: string; color: string }[] = [
  { value: "backlog", label: "Backlog", color: "#8C8C8C" },
  { value: "planned", label: "Planned", color: "#8C8C8C" },
  { value: "in_progress", label: "In Progress", color: "#F2C94C" },
  { value: "paused", label: "Paused", color: "#95959F" },
  { value: "completed", label: "Completed", color: "#5E6AD2" },
  { value: "cancelled", label: "Cancelled", color: "#95959F" },
]

const PROJECT_ICONS = [
  { name: "rocket", Icon: Rocket },
  { name: "code", Icon: Code },
  { name: "clipboard", Icon: Clipboard },
  { name: "briefcase", Icon: Briefcase },
  { name: "zap", Icon: Zap },
  { name: "target", Icon: Target },
  { name: "shield", Icon: Shield },
  { name: "database", Icon: Database },
  { name: "globe", Icon: Globe },
]

const PROJECT_COLORS = [
  { name: "blue", value: "#3B82F6" },
  { name: "emerald", value: "#10B981" },
  { name: "violet", value: "#8B5CF6" },
  { name: "amber", value: "#F59E0B" },
  { name: "rose", value: "#F43F5E" },
  { name: "cyan", value: "#06B6D4" },
  { name: "lime", value: "#84CC16" },
  { name: "fuchsia", value: "#D946EF" },
]

interface CreateProjectModalProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  crews: CrewSummary[]
  labels: IssueLabel[]
  workspaceId: string
  onCreated: () => void
}

export function CreateProjectModal({
  open,
  onOpenChange,
  crews,
  labels,
  workspaceId,
  onCreated,
}: CreateProjectModalProps) {
  const [name, setName] = useState("")
  const [summary, setSummary] = useState("")
  const [description, setDescription] = useState("")
  const [icon, setIcon] = useState("rocket")
  const [color, setColor] = useState("blue")
  const [status, setStatus] = useState<ProjectStatus>("backlog")
  const [priority, setPriority] = useState<IssuePriority>("none")
  const [leadType, setLeadType] = useState<"user" | "agent" | null>(null)
  const [leadId, setLeadId] = useState<string | null>(null)
  const [startDate, setStartDate] = useState("")
  const [targetDate, setTargetDate] = useState("")
  const [selectedLabels, setSelectedLabels] = useState<string[]>([])
  const [agents, setAgents] = useState<AssigneeOption[]>([])
  const [saving, setSaving] = useState(false)

  // Popover states
  const [iconOpen, setIconOpen] = useState(false)
  const [statusOpen, setStatusOpen] = useState(false)
  const [priorityOpen, setPriorityOpen] = useState(false)
  const [leadOpen, setLeadOpen] = useState(false)
  const [startOpen, setStartOpen] = useState(false)
  const [targetOpen, setTargetOpen] = useState(false)
  const [labelsOpen, setLabelsOpen] = useState(false)

  const nameRef = useRef<HTMLInputElement>(null)

  // Focus name on open
  useEffect(() => {
    if (open) {
      setTimeout(() => nameRef.current?.focus(), 100)
    }
  }, [open])

  // Fetch all agents for lead picker
  useEffect(() => {
    if (!open || !workspaceId) return
    let cancelled = false
    async function fetchAgents() {
      try {
        const res = await fetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`)
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
  }, [open, workspaceId])

  function reset() {
    setName("")
    setSummary("")
    setDescription("")
    setIcon("rocket")
    setColor("blue")
    setStatus("backlog")
    setPriority("none")
    setLeadType(null)
    setLeadId(null)
    setStartDate("")
    setTargetDate("")
    setSelectedLabels([])
  }

  const statusInfo = PROJECT_STATUSES.find((s) => s.value === status) ?? PROJECT_STATUSES[0]
  const colorInfo = PROJECT_COLORS.find((c) => c.name === color) ?? PROJECT_COLORS[0]
  const IconComponent = PROJECT_ICONS.find((i) => i.name === icon)?.Icon ?? Rocket
  const leadName = (() => {
    if (!leadId) return null
    const found = agents.find((a) => a.id === leadId)
    return found?.name ?? null
  })()

  const handleSubmit = useCallback(async () => {
    if (!name.trim()) { toast.error("Project name is required"); return }

    setSaving(true)
    try {
      const res = await fetch(
        `/api/v1/projects?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            name: name.trim(),
            description: description.trim() || undefined,
            icon,
            color,
            status,
            priority,
            lead_type: leadType ?? undefined,
            lead_id: leadId ?? undefined,
            start_date: startDate || undefined,
            target_date: targetDate || undefined,
          }),
        },
      )

      if (!res.ok) {
        const body = await res.json().catch(() => null)
        toast.error(body?.detail ?? "Failed to create project")
        return
      }

      toast.success("Project created")
      reset()
      onOpenChange(false)
      onCreated()
    } catch {
      toast.error("Failed to create project")
    } finally {
      setSaving(false)
    }
  }, [name, description, icon, color, status, priority, leadType, leadId, startDate, targetDate, workspaceId, onCreated, onOpenChange])

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
        className="sm:max-w-[720px] p-0 gap-0 overflow-hidden flex flex-col max-h-[85vh]"
        onKeyDown={handleKeyDown}
        onOpenAutoFocus={(e) => e.preventDefault()}
      >
        <DialogTitle className="sr-only">New Project</DialogTitle>

        {/* ── Header ── */}
        <div className="flex items-center gap-1.5 px-4 py-3 border-b border-white/[0.06] shrink-0">
          <span className="text-xs font-medium text-muted-foreground">CRE</span>
          <ChevronRight className="h-3 w-3 text-muted-foreground/50" />
          <span className="text-xs text-muted-foreground">New project</span>
          <div className="flex-1" />
          <button
            onClick={() => onOpenChange(false)}
            className="h-6 w-6 rounded-md flex items-center justify-center text-muted-foreground hover:text-foreground hover:bg-white/[0.06] transition-colors"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>

        {/* ── Body (scrollable) ── */}
        <div className="flex-1 min-h-0 overflow-y-auto">
          <div className="px-5 pt-4 pb-2">
            {/* Icon + Name row */}
            <div className="flex items-start gap-3 mb-2">
              {/* Icon button */}
              <Popover open={iconOpen} onOpenChange={setIconOpen}>
                <PopoverTrigger asChild>
                  <button
                    className="h-10 w-10 rounded-lg flex items-center justify-center border border-white/[0.08] hover:border-white/[0.15] transition-colors shrink-0"
                    style={{ backgroundColor: `${colorInfo.value}15` }}
                  >
                    <IconComponent className="h-5 w-5" style={{ color: colorInfo.value }} />
                  </button>
                </PopoverTrigger>
                <PopoverContent className="w-[240px] p-3" align="start">
                  {/* Icons */}
                  <p className="text-xs text-muted-foreground mb-2">Icon</p>
                  <div className="grid grid-cols-5 gap-1 mb-3">
                    {PROJECT_ICONS.map((item) => (
                      <button
                        key={item.name}
                        onClick={() => setIcon(item.name)}
                        className={cn(
                          "h-8 w-8 rounded-md flex items-center justify-center transition-colors",
                          icon === item.name ? "bg-white/[0.12] text-foreground" : "text-muted-foreground hover:bg-white/[0.08]",
                        )}
                      >
                        <item.Icon className="h-4 w-4" />
                      </button>
                    ))}
                  </div>
                  {/* Colors */}
                  <p className="text-xs text-muted-foreground mb-2">Color</p>
                  <div className="flex gap-1.5">
                    {PROJECT_COLORS.map((c) => (
                      <button
                        key={c.name}
                        onClick={() => { setColor(c.name); setIconOpen(false) }}
                        className={cn(
                          "h-6 w-6 rounded-full transition-all",
                          color === c.name ? "ring-2 ring-offset-2 ring-offset-background" : "hover:scale-110",
                        )}
                        style={{ backgroundColor: c.value, "--tw-ring-color": c.value } as React.CSSProperties}
                      />
                    ))}
                  </div>
                </PopoverContent>
              </Popover>

              {/* Name + Summary */}
              <div className="flex-1 min-w-0">
                <input
                  ref={nameRef}
                  type="text"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="Project name"
                  className="w-full bg-transparent text-base font-medium text-foreground placeholder:text-muted-foreground/50 outline-none"
                />
                <input
                  type="text"
                  value={summary}
                  onChange={(e) => setSummary(e.target.value)}
                  placeholder="Add a short summary..."
                  className="w-full bg-transparent text-sm text-muted-foreground placeholder:text-muted-foreground/40 outline-none mt-1"
                />
              </div>
            </div>

            {/* Metadata pills */}
            <div className="flex items-center gap-1.5 flex-wrap py-2 border-t border-white/[0.04] mt-2">
              {/* Status */}
              <Popover open={statusOpen} onOpenChange={setStatusOpen}>
                <PopoverTrigger asChild>
                  <button className={cn(
                    "h-7 px-2.5 rounded-md text-xs bg-white/[0.04] hover:bg-white/[0.08] border border-white/[0.06] flex items-center gap-1.5 transition-colors",
                    status !== "backlog" ? "text-foreground/80" : "text-muted-foreground",
                  )}>
                    <span className="h-2 w-2 rounded-full" style={{ backgroundColor: statusInfo.color }} />
                    <span>{statusInfo.label}</span>
                  </button>
                </PopoverTrigger>
                <PopoverContent className="w-[180px] p-1" align="start">
                  {PROJECT_STATUSES.map((s) => (
                    <button
                      key={s.value}
                      onClick={() => { setStatus(s.value); setStatusOpen(false) }}
                      className={cn(
                        "w-full flex items-center gap-2 px-2 py-1.5 rounded-md text-xs hover:bg-white/[0.08] transition-colors",
                        status === s.value ? "text-foreground bg-white/[0.06]" : "text-muted-foreground",
                      )}
                    >
                      <span className="h-2 w-2 rounded-full" style={{ backgroundColor: s.color }} />
                      <span>{s.label}</span>
                      {status === s.value && <Check className="ml-auto h-3 w-3" />}
                    </button>
                  ))}
                </PopoverContent>
              </Popover>

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

              {/* Lead */}
              <Popover open={leadOpen} onOpenChange={setLeadOpen}>
                <PopoverTrigger asChild>
                  <button className={cn(
                    "h-7 px-2.5 rounded-md text-xs bg-white/[0.04] hover:bg-white/[0.08] border border-white/[0.06] flex items-center gap-1.5 transition-colors",
                    leadId ? "text-foreground/80" : "text-muted-foreground",
                  )}>
                    {leadType === "agent" ? <Bot className="h-3 w-3" /> : <User className="h-3 w-3" />}
                    <span>{leadName ?? "Lead"}</span>
                  </button>
                </PopoverTrigger>
                <PopoverContent className="w-[220px] p-0" align="start">
                  <Command>
                    <CommandInput placeholder="Search lead..." className="h-8 text-xs" />
                    <CommandList>
                      <CommandEmpty>No results found.</CommandEmpty>
                      <CommandGroup>
                        <CommandItem onSelect={() => { setLeadType(null); setLeadId(null); setLeadOpen(false) }}>
                          <UserX className="mr-2 h-3.5 w-3.5 text-muted-foreground" />
                          <span className="text-xs">No lead</span>
                          {!leadId && <Check className="ml-auto h-3.5 w-3.5" />}
                        </CommandItem>
                      </CommandGroup>
                      {agents.length > 0 && (
                        <CommandGroup heading="Agents">
                          {agents.map((agent) => (
                            <CommandItem
                              key={agent.id}
                              onSelect={() => { setLeadType("agent"); setLeadId(agent.id); setLeadOpen(false) }}
                            >
                              <Bot className="mr-2 h-3.5 w-3.5 text-muted-foreground" />
                              <span className="text-xs">{agent.name}</span>
                              {leadId === agent.id && <Check className="ml-auto h-3.5 w-3.5" />}
                            </CommandItem>
                          ))}
                        </CommandGroup>
                      )}
                    </CommandList>
                  </Command>
                </PopoverContent>
              </Popover>

              {/* Start date */}
              <Popover open={startOpen} onOpenChange={setStartOpen}>
                <PopoverTrigger asChild>
                  <button className={cn(
                    "h-7 px-2.5 rounded-md text-xs bg-white/[0.04] hover:bg-white/[0.08] border border-white/[0.06] flex items-center gap-1.5 transition-colors",
                    startDate ? "text-foreground/80" : "text-muted-foreground",
                  )}>
                    <Calendar className="h-3 w-3" />
                    <span>{startDate || "Start"}</span>
                  </button>
                </PopoverTrigger>
                <PopoverContent className="w-auto p-3" align="start">
                  <p className="text-xs text-muted-foreground mb-2">Start date</p>
                  <input
                    type="date"
                    value={startDate}
                    onChange={(e) => { setStartDate(e.target.value); setStartOpen(false) }}
                    className="bg-transparent text-sm text-foreground outline-none border border-white/[0.1] rounded-md px-2 py-1"
                  />
                  {startDate && (
                    <button
                      onClick={() => { setStartDate(""); setStartOpen(false) }}
                      className="block mt-2 text-xs text-muted-foreground hover:text-foreground"
                    >
                      Clear
                    </button>
                  )}
                </PopoverContent>
              </Popover>

              {/* Target date */}
              <Popover open={targetOpen} onOpenChange={setTargetOpen}>
                <PopoverTrigger asChild>
                  <button className={cn(
                    "h-7 px-2.5 rounded-md text-xs bg-white/[0.04] hover:bg-white/[0.08] border border-white/[0.06] flex items-center gap-1.5 transition-colors",
                    targetDate ? "text-foreground/80" : "text-muted-foreground",
                  )}>
                    <Calendar className="h-3 w-3" />
                    <span>{targetDate || "Target"}</span>
                  </button>
                </PopoverTrigger>
                <PopoverContent className="w-auto p-3" align="start">
                  <p className="text-xs text-muted-foreground mb-2">Target date</p>
                  <input
                    type="date"
                    value={targetDate}
                    onChange={(e) => { setTargetDate(e.target.value); setTargetOpen(false) }}
                    className="bg-transparent text-sm text-foreground outline-none border border-white/[0.1] rounded-md px-2 py-1"
                  />
                  {targetDate && (
                    <button
                      onClick={() => { setTargetDate(""); setTargetOpen(false) }}
                      className="block mt-2 text-xs text-muted-foreground hover:text-foreground"
                    >
                      Clear
                    </button>
                  )}
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
          </div>

          {/* Description */}
          <div className="px-5 pb-4 border-t border-white/[0.04]">
            <TiptapEditor
              content={description}
              onChange={setDescription}
              placeholder="Write a description, a project brief, or collect ideas..."
              compact
              className="min-h-[120px]"
            />
          </div>

          {/* Milestones section (future) */}
          <div className="px-5 py-2.5 border-t border-white/[0.06] flex items-center justify-between">
            <span className="text-xs font-medium text-muted-foreground">Milestones</span>
            <button className="h-5 w-5 rounded flex items-center justify-center text-muted-foreground/50 hover:text-muted-foreground hover:bg-white/[0.06] transition-colors text-xs">
              +
            </button>
          </div>
        </div>

        {/* ── Footer ── */}
        <div className="flex items-center justify-end gap-2 px-5 py-3 border-t border-white/[0.06] shrink-0">
          <button
            onClick={() => onOpenChange(false)}
            disabled={saving}
            className="h-7 px-3 rounded-md text-xs font-medium text-muted-foreground hover:text-foreground hover:bg-white/[0.06] transition-colors disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            onClick={handleSubmit}
            disabled={saving || !name.trim()}
            className="h-7 px-3 rounded-md text-xs font-medium bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50 disabled:pointer-events-none flex items-center gap-1.5 transition-colors"
          >
            {saving && <Loader2 className="h-3 w-3 animate-spin" />}
            Create project
          </button>
        </div>
      </DialogContent>
    </Dialog>
  )
}
