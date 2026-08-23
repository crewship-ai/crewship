"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import {
  Check,
  User,
  Bot,
  UserX,
  Calendar,
  FolderKanban,
  Search,
} from "lucide-react"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfacePill,
  CreateSurfacePills,
  CreateSurfaceRefusal,
  CreateSurfaceTitleInput,
} from "@/components/layout/create-surface"
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
import { Input } from "@/components/ui/input"
import dynamic from "next/dynamic"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { CrewIcon } from "@/components/ui/crew-icon"

const TiptapEditor = dynamic(
  () => import("@/components/features/issues/tiptap-editor").then(m => m.TiptapEditor),
  { ssr: false },
)
import {
  searchCrewIcons, getCrewIconDef,
  CREW_ICON_CATEGORIES, GRADIENT_PALETTES,
} from "@/lib/entities"
import type { AssigneeOption } from "@/components/features/issues/assignee-picker"
import { cn } from "@/lib/utils"
import { ISSUE_ICON_COLORS } from "@/lib/colors"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "sonner"
import type { IssueLabel, IssuePriority, ProjectStatus } from "@/lib/types/mission"
import type { CrewSummary } from "@/lib/types/orchestration"

const PRIORITIES: IssuePriority[] = ["urgent", "high", "medium", "low", "none"]

const PROJECT_STATUSES: { value: ProjectStatus; label: string; color: string }[] = [
  { value: "backlog", label: "Backlog", color: ISSUE_ICON_COLORS.BACKLOG },
  { value: "planned", label: "Planned", color: ISSUE_ICON_COLORS.BACKLOG },
  { value: "in_progress", label: "In Progress", color: ISSUE_ICON_COLORS.IN_PROGRESS },
  { value: "paused", label: "Paused", color: ISSUE_ICON_COLORS.CANCELLED },
  { value: "completed", label: "Completed", color: ISSUE_ICON_COLORS.COMPLETED },
  { value: "cancelled", label: "Cancelled", color: ISSUE_ICON_COLORS.CANCELLED },
]

interface CreateProjectModalProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  crews: CrewSummary[]
  /**
   * Workspace issue labels. Unused: labels are an *issue* concept
   * (labels/mission_labels) — there is no project_labels table and
   * POST /api/v1/projects binds no labels field, so a picker here could only
   * discard what the user chose. Kept on the props so the caller does not have
   * to change; wire it up if project labels ever ship.
   */
  labels: IssueLabel[]
  workspaceId: string
  onCreated: () => void
}

export function CreateProjectModal({
  open,
  onOpenChange,
  crews: _crews,
  labels: _labels,
  workspaceId,
  onCreated,
}: CreateProjectModalProps) {
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [icon, setIcon] = useState("rocket")
  const [color, setColor] = useState("blue")
  const [status, setStatus] = useState<ProjectStatus>("backlog")
  const [priority, setPriority] = useState<IssuePriority>("none")
  const [leadType, setLeadType] = useState<"user" | "agent" | null>(null)
  const [leadId, setLeadId] = useState<string | null>(null)
  const [startDate, setStartDate] = useState("")
  const [targetDate, setTargetDate] = useState("")
  const [agents, setAgents] = useState<AssigneeOption[]>([])
  const [saving, setSaving] = useState(false)
  // What the server said when it said no. Shown in the shell's refusal band,
  // which sits outside the scrollport — the toast below it is kept because it
  // is the notification the rest of the app uses, but the toast is what fades.
  const [refusal, setRefusal] = useState<string | null>(null)

  // Popover states
  const [iconOpen, setIconOpen] = useState(false)
  const [iconQuery, setIconQuery] = useState("")
  const [iconCategory, setIconCategory] = useState<string | null>(null)
  const [statusOpen, setStatusOpen] = useState(false)
  const [priorityOpen, setPriorityOpen] = useState(false)
  const [leadOpen, setLeadOpen] = useState(false)
  const [startOpen, setStartOpen] = useState(false)
  const [targetOpen, setTargetOpen] = useState(false)

  const nameRef = useRef<HTMLInputElement>(null)

  // Icon search results
  const iconResults = useMemo(() => {
    if (iconCategory) return searchCrewIcons(iconCategory)
    return searchCrewIcons(iconQuery)
  }, [iconQuery, iconCategory])

  // Focus name on open. The shell suppresses Radix's own autofocus so the
  // surface opens on the first real field rather than on the close button.
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
        const res = await apiFetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`)
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
    setDescription("")
    setIcon("rocket")
    setColor("blue")
    setStatus("backlog")
    setPriority("none")
    setLeadType(null)
    setLeadId(null)
    setStartDate("")
    setTargetDate("")
    setRefusal(null)
  }

  const statusInfo = PROJECT_STATUSES.find((s) => s.value === status) ?? PROJECT_STATUSES[0]
  const leadName = (() => {
    if (!leadId) return null
    const found = agents.find((a) => a.id === leadId)
    return found?.name ?? null
  })()

  // Anything the person has typed or picked. Drives the shell's discard guard,
  // which covers Esc, the overlay click and the header's × — the three routes
  // out that this modal previously took without asking.
  const dirty =
    name.trim() !== "" ||
    description.trim() !== "" ||
    icon !== "rocket" ||
    color !== "blue" ||
    status !== "backlog" ||
    priority !== "none" ||
    leadId !== null ||
    startDate !== "" ||
    targetDate !== ""

  const handleSubmit = useCallback(async () => {
    if (!name.trim()) { toast.error("Project name is required"); return }

    setSaving(true)
    setRefusal(null)
    try {
      const res = await apiFetch(
        `/api/v1/projects?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          // Every key here is bound by the create request struct in
          // internal/api/project_handler.go. readJSON() does not reject unknown
          // fields, so anything extra would be accepted with a 201 and dropped.
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
        const detail = body?.detail ?? "Failed to create project"
        setRefusal(detail)
        toast.error(detail)
        return
      }

      toast.success("Project created")
      reset()
      onOpenChange(false)
      onCreated()
    } catch {
      setRefusal("Failed to create project")
      toast.error("Failed to create project")
    } finally {
      setSaving(false)
    }
  }, [name, description, icon, color, status, priority, leadType, leadId, startDate, targetDate, workspaceId, onCreated, onOpenChange])

  return (
    <CreateSurface
      open={open}
      onOpenChange={onOpenChange}
      size="md"
      dirty={dirty}
      discardLabel="this project"
      // ⌘↵ comes from the shell now. handleSubmit keeps its own empty-name
      // guard because the keyboard route reaches it while the footer's primary
      // is still disabled.
      onSubmit={handleSubmit}
    >
      <CreateSurfaceHeader
        icon={FolderKanban}
        accent="blue"
        context="CRE"
        title="New project"
        onClose={() => onOpenChange(false)}
      />

      <CreateSurfaceBody className="flex flex-col gap-4">
        {/* Icon + Name row */}
        <div className="flex items-start gap-3">
          {/* Icon button — uses crew icon system */}
          <Popover open={iconOpen} onOpenChange={(v) => { setIconOpen(v); if (!v) { setIconQuery(""); setIconCategory(null) } }}>
            <PopoverTrigger asChild>
              <button type="button" aria-label="Change project icon" className="shrink-0 relative group cursor-pointer">
                <CrewIcon icon={icon} color={color} size="lg" />
              </button>
            </PopoverTrigger>
            <PopoverContent className="w-[340px] sm:w-[400px] p-0 rounded-2xl" align="start" sideOffset={8}>
              {/* Color picker row */}
              <div className="px-4 pt-4 pb-3 border-b">
                <div className="flex items-center justify-between mb-3">
                  {GRADIENT_PALETTES.map((p) => (
                    <button
                      key={p.id}
                      type="button"
                      onClick={() => setColor(p.id)}
                      className="transition-all hover:scale-105 shrink-0"
                    >
                      <CrewIcon
                        icon={icon}
                        color={p.id}
                        size="sm"
                        className={cn(
                          "transition-all",
                          color === p.id ? "ring-2 ring-primary ring-offset-2 ring-offset-background scale-110" : "opacity-50 hover:opacity-100",
                        )}
                      />
                    </button>
                  ))}
                </div>
                <p className="text-micro text-muted-foreground">Pick a color, then choose an icon below</p>
              </div>

              {/* Search */}
              <div className="px-4 pt-3 pb-2">
                <div className="relative">
                  <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
                  <Input
                    value={iconQuery}
                    onChange={(e) => { setIconQuery(e.target.value); setIconCategory(null) }}
                    placeholder="Search icons..."
                    className="pl-9 h-8 text-xs"
                  />
                </div>
              </div>

              {/* Category chips */}
              <div className="px-4 pb-2">
                <div className="flex flex-wrap gap-1">
                  {CREW_ICON_CATEGORIES.map((cat) => (
                    <button
                      key={cat}
                      type="button"
                      onClick={() => {
                        if (iconCategory === cat) { setIconCategory(null); setIconQuery("") }
                        else { setIconCategory(cat); setIconQuery("") }
                      }}
                      className={cn(
                        "px-2 py-0.5 text-micro rounded-full capitalize transition-colors",
                        iconCategory === cat
                          ? "bg-primary text-primary-foreground font-medium"
                          : "bg-muted/60 text-muted-foreground hover:bg-muted hover:text-foreground",
                      )}
                    >
                      {cat}
                    </button>
                  ))}
                </div>
              </div>

              {/* Icon grid */}
              <div className="px-4 pb-4">
                <div className="grid grid-cols-8 gap-1 max-h-[240px] overflow-y-auto rounded-lg border bg-muted/20 p-2">
                  {iconResults.map((iconName) => {
                    const def = getCrewIconDef(iconName)
                    const IconComp = def.icon
                    const isSelected = icon === iconName
                    return (
                      <button
                        key={iconName}
                        type="button"
                        title={def.label}
                        onClick={() => { setIcon(iconName); setIconOpen(false) }}
                        className={cn(
                          "aspect-square rounded-lg flex items-center justify-center transition-all",
                          isSelected
                            ? "bg-primary text-primary-foreground shadow-sm scale-110"
                            : "text-muted-foreground hover:bg-accent hover:text-foreground",
                        )}
                      >
                        <IconComp className="h-4 w-4" />
                      </button>
                    )
                  })}
                  {iconResults.length === 0 && (
                    <p className="col-span-8 text-center text-xs text-muted-foreground py-8">No icons found</p>
                  )}
                </div>
                <p className="text-micro text-muted-foreground mt-2 text-center">
                  {iconResults.length} icons {iconCategory ? `in ${iconCategory}` : "available"}
                </p>
              </div>
            </PopoverContent>
          </Popover>

          {/* Name */}
          <div className="flex-1 min-w-0">
            <label htmlFor="project-name" className="sr-only">Project name</label>
            <CreateSurfaceTitleInput
              id="project-name"
              ref={nameRef}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Project name"
            />
          </div>
        </div>

        {/* Description */}
        <div className="border-t border-hairline pt-3">
          <TiptapEditor
            content={description}
            onChange={setDescription}
            placeholder="Write a description, a project brief, or collect ideas..."
            compact
            className="min-h-[120px]"
          />
        </div>

        {/* No milestones section: POST /api/v1/projects/{projectId}/milestones
            404s unless the project already exists, so nothing here can create
            one. Milestones are added after the project is created. */}
      </CreateSurfaceBody>

      {/* Metadata pills. Same five controls in the same order as before; they
          moved out of the scrollport into the shell's pill row, which is the
          slot for exactly this and keeps them on one scrolling line on a
          phone instead of wrapping onto three. */}
      <CreateSurfacePills>
        {/* Status */}
        <Popover open={statusOpen} onOpenChange={setStatusOpen}>
          <PopoverTrigger asChild>
            <CreateSurfacePill set={status !== "backlog"}>
              <span className="h-2 w-2 rounded-full" style={{ backgroundColor: statusInfo.color }} />
              <span>{statusInfo.label}</span>
            </CreateSurfacePill>
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
            <CreateSurfacePill set={priority !== "none"}>
              <PriorityIcon priority={priority} className="h-3.5 w-3.5" />
              <span>{priorityLabel[priority]}</span>
            </CreateSurfacePill>
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
            <CreateSurfacePill icon={leadType === "agent" ? Bot : User} set={leadId !== null}>
              <span>{leadName ?? "Lead"}</span>
            </CreateSurfacePill>
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
            <CreateSurfacePill icon={Calendar} set={startDate !== ""}>
              <span>{startDate || "Start"}</span>
            </CreateSurfacePill>
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
            <CreateSurfacePill icon={Calendar} set={targetDate !== ""}>
              <span>{targetDate || "Target"}</span>
            </CreateSurfacePill>
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

        {/* No labels pill: labels are an issue concept. There is no
            project_labels table and no labels field on the create
            request, so a picker here would discard the selection. */}
      </CreateSurfacePills>

      <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />

      <CreateSurfaceFooter
        onCancel={() => onOpenChange(false)}
        primaryLabel="Create project"
        onPrimary={handleSubmit}
        primaryDisabled={!name.trim()}
        busy={saving}
      />
    </CreateSurface>
  )
}
