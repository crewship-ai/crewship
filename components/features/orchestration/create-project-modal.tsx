"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import {
  AlertTriangle,
  Check,
  User,
  UserX,
  Calendar,
  FolderKanban,
} from "lucide-react"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceNotice,
  CreateSurfacePicker,
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
import { AgentAvatar } from "@/components/ui/agent-avatar"
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

/** What GET /api/v1/agents actually returns, narrowed to what is used here. */
interface RawAgent {
  id: string
  name: string
  slug?: string
  avatar_seed?: string | null
  avatar_style?: string | null
  avatar_url?: string | null
  crew?: { avatar_style?: string | null } | null
}

/**
 * An agent, with the face it wears everywhere else.
 *
 * `AssigneeOption` (assignee-picker.tsx) carries id/name/type/slug only. Rather
 * than widen a type three other surfaces import, this narrows the widening to
 * the one modal that needs it.
 */
type LeadOption = AssigneeOption & {
  avatar_seed?: string | null
  avatar_style?: string | null
  avatar_url?: string | null
  crew_avatar_style?: string | null
}

/**
 * Where the agent list is between "not asked yet" and "here it is".
 *
 * Mirrors `AgentLoad` in create-issue-modal.tsx: an empty list renders
 * identically whether the workspace has nobody, the request 500'd, or the
 * request is still in flight, which is the same defect that picker's fix
 * addressed. Applying the same shape here rather than inventing a second one.
 */
type AgentLoad = "idle" | "loading" | "ready" | "error"

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
  const [agents, setAgents] = useState<LeadOption[]>([])
  const [agentLoad, setAgentLoad] = useState<AgentLoad>("idle")
  /** Bumped by the error state's Try again, purely to re-run the fetch effect. */
  const [agentReload, setAgentReload] = useState(0)
  const [saving, setSaving] = useState(false)
  // What the server said when it said no. Shown in the shell's refusal band,
  // which sits outside the scrollport — the toast below it is kept because it
  // is the notification the rest of the app uses, but the toast is what fades.
  const [refusal, setRefusal] = useState<string | null>(null)

  // Icon picker is a panel inside the surface (CreateSurfacePicker), not a
  // second floating overlay — see the header/body/footer swap below.
  const [panel, setPanel] = useState<null | "icon">(null)
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

  // Fetch all agents for lead picker.
  //
  // The failure paths used to be `if (!res.ok || cancelled) return` and a bare
  // `catch {}`, which left `agents` at `[]` — identical to what a workspace
  // with no agents renders. Same defect the assignee picker in
  // create-issue-modal.tsx had, fixed there with a three-state load; this is
  // that same shape rather than a second one.
  useEffect(() => {
    if (!open || !workspaceId) return
    let cancelled = false
    setAgents([])
    setAgentLoad("loading")
    async function fetchAgents() {
      try {
        const res = await apiFetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`)
        if (cancelled) return
        if (!res.ok) { setAgentLoad("error"); return }
        const data = await res.json()
        if (cancelled) return
        const list = Array.isArray(data) ? data : data.agents ?? []
        // The avatar fields were dropped in this map, which is why the lead
        // picker drew a row of identical grey bots while the same agents
        // wear faces on the board behind it. GET /api/v1/agents returns all
        // four; the seed falls back to the name, which is the only reason
        // an avatar appears anywhere (every agent here has a NULL seed).
        setAgents(
          list.map((a: RawAgent) => ({
            id: a.id,
            name: a.name,
            type: "agent" as const,
            slug: a.slug,
            avatar_seed: a.avatar_seed,
            avatar_style: a.avatar_style,
            avatar_url: a.avatar_url,
            crew_avatar_style: a.crew?.avatar_style ?? null,
          })),
        )
        setAgentLoad("ready")
      } catch {
        if (!cancelled) setAgentLoad("error")
      }
    }
    fetchAgents()
    return () => { cancelled = true }
  }, [open, workspaceId, agentReload])

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
  const selectedLead = leadId ? (agents.find((a) => a.id === leadId) ?? null) : null
  const leadName = selectedLead?.name ?? null

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
        // Not a key prefix. "CRE" was copied across from the issue modal,
        // where it is the fallback for `crew.slug.slice(0, 3)` — a project is
        // workspace-scoped and has no crew, so the literal froze into a string
        // that reads as truncated text. Every other door names its page here.
        context="Projects"
        title={panel === "icon" ? "Icon — new project" : "New project"}
        description={
          panel === "icon" ? "Pick a colour, then an icon. Browse by category, or search." : undefined
        }
        onBack={panel ? () => setPanel(null) : undefined}
        onClose={() => onOpenChange(false)}
      />

      <CreateSurfaceBody className="flex flex-col gap-4">
        {/* Icon panel — CreateSurfacePicker, a PANEL INSIDE the surface rather
            than a nested Popover. A create dialog is already one overlay; a
            popover full of its own grid on top of it was a second, and on a
            phone that is a sheet stacked on a sheet where the back gesture
            dismisses the wrong one. */}
        {panel === "icon" && (
          <CreateSurfacePicker
            preview={<CrewIcon icon={icon} color={color} size="xl" />}
            previewHint={`${getCrewIconDef(icon).label} · ${color}`}
            palette={{
              value: color,
              onChange: setColor,
              options: GRADIENT_PALETTES.map((p) => ({ id: p.id, dot: p.dot })),
            }}
            categories={{
              value: iconCategory,
              options: CREW_ICON_CATEGORIES,
              onChange: (c) => { setIconCategory(c); setIconQuery("") },
            }}
            search={{
              value: iconQuery,
              onChange: (v) => { setIconQuery(v); setIconCategory(null) },
              placeholder: "Search icons…",
            }}
            options={iconResults.map((iconName) => {
              const def = getCrewIconDef(iconName)
              return { id: iconName, label: def.label, render: <def.icon className="h-4 w-4 text-foreground/70" /> }
            })}
            value={icon}
            onChange={setIcon}
            columns={8}
          />
        )}

        {!panel && (
          <>
        {/* Icon + Name row */}
        <div className="flex items-start gap-3">
          <button
            type="button"
            aria-label="Change project icon"
            onClick={() => setPanel("icon")}
            className="shrink-0 rounded-xl transition-opacity hover:opacity-80"
          >
            <CrewIcon icon={icon} color={color} size="lg" />
          </button>

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
            // A project brief is prose with a full toolbar above it — headings,
            // lists, tables, code. 120px is four lines, so the toolbar was
            // taller than the thing it formats and every paragraph pushed the
            // caret against the bottom edge. This is the one field on the
            // surface people actually write IN rather than fill.
            className="min-h-[280px]"
          />
        </div>

        {/* Milestones cannot be created here: POST
            /api/v1/projects/{projectId}/milestones 404s until the project
            already exists (milestone_handler.go's projectExists check), and
            there is no post-create screen that can add one either. This used
            to be a code comment only — the user was told nothing. Said
            out loud instead, matching the read-only reference at
            components/features/design/surfaces/issues.tsx:341-353. */}
        <CreateSurfaceNotice tone="warn" icon={AlertTriangle}>
          Milestones cannot be created here — the endpoint refuses until the project exists — and there is
          no screen anywhere in the web UI that can create one. The CLI can:{" "}
          <code className="font-mono">crewship milestone create</code>.
        </CreateSurfaceNotice>
          </>
        )}
      </CreateSurfaceBody>

      {/* Metadata pills. Same five controls in the same order as before; they
          moved out of the scrollport into the shell's pill row, which is the
          slot for exactly this and keeps them on one scrolling line on a
          phone instead of wrapping onto three. Hidden on the icon panel —
          the pills describe the project, not the icon, and there is no room
          for both above a 44px footer. */}
      {!panel && <CreateSurfacePills>
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
            <CreateSurfacePill
              icon={leadType === "agent" ? undefined : User}
              leading={
                leadType === "agent" && selectedLead ? (
                  <AgentAvatar
                    seed={selectedLead.avatar_seed || selectedLead.name}
                    style={selectedLead.avatar_style || selectedLead.crew_avatar_style}
                    agentId={selectedLead.id}
                    avatarUrl={selectedLead.avatar_url}
                    alt=""
                    className="h-3.5 w-3.5 shrink-0"
                  />
                ) : undefined
              }
              set={leadId !== null}
            >
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
                {/* Three ways to have no agents to list, and they are not the
                    same thing — see the fetch effect above. Plain nodes rather
                    than CommandItems: not selectable, and cmdk must not filter
                    them away when the search box has text in it. */}
                {agentLoad === "loading" && (
                  <p className="px-3 py-2 text-[11px] text-muted-foreground">Loading agents…</p>
                )}
                {agentLoad === "error" && (
                  <div role="status" className="flex flex-col items-start gap-1 px-3 py-2">
                    <span className="text-[11px] leading-relaxed text-destructive">
                      Agents could not be loaded.
                    </span>
                    <button
                      type="button"
                      onClick={() => setAgentReload((n) => n + 1)}
                      className="text-[11px] font-medium text-primary underline-offset-2 hover:underline"
                    >
                      Try again
                    </button>
                  </div>
                )}
                {agentLoad === "ready" && agents.length === 0 && (
                  <p className="px-3 py-2 text-[11px] leading-relaxed text-muted-foreground">
                    No agents in this workspace to lead the project.
                  </p>
                )}
                {agents.length > 0 && (
                  <CommandGroup heading="Agents">
                    {agents.map((agent) => (
                      <CommandItem
                        key={agent.id}
                        onSelect={() => { setLeadType("agent"); setLeadId(agent.id); setLeadOpen(false) }}
                      >
                        <AgentAvatar
                          seed={agent.avatar_seed || agent.name}
                          style={agent.avatar_style || agent.crew_avatar_style}
                          agentId={agent.id}
                          avatarUrl={agent.avatar_url}
                          alt=""
                          className="mr-2 h-4 w-4 shrink-0"
                        />
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
      </CreateSurfacePills>}

      {!panel && <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />}

      <CreateSurfaceFooter
        onCancel={panel ? () => setPanel(null) : () => onOpenChange(false)}
        cancelLabel={panel ? "Back" : "Cancel"}
        primaryLabel={panel ? "Use this icon" : "Create project"}
        onPrimary={panel ? () => setPanel(null) : handleSubmit}
        primaryDisabled={panel ? false : !name.trim()}
        busy={panel ? false : saving}
      />
    </CreateSurface>
  )
}
