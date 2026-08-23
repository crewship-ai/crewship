"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import {
  Paperclip,
  User,
  Bot,
  UserX,
  Check,
  Tag,
  FolderKanban,
} from "lucide-react"
import type { Pipeline } from "@/hooks/use-pipelines"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceDescriptionInput,
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
import { Checkbox } from "@/components/ui/checkbox"
import { Switch } from "@/components/ui/switch"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import { resolveRoutineColor, resolveRoutineIcon } from "@/lib/routine-identity"
import { LabelBadge } from "@/components/features/issues/label-badge"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { StatusIcon } from "@/components/features/issues/status-icon"
import type { AssigneeOption } from "@/components/features/issues/assignee-picker"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "sonner"
import type { IssueLabel, IssuePriority, Project } from "@/lib/types/mission"
import type { CrewSummary } from "@/lib/types/orchestration"

const PRIORITIES: IssuePriority[] = ["urgent", "high", "medium", "low", "none"]

/**
 * An assignee row plus the three fields a face is drawn from.
 *
 * The shared `AssigneeOption` carries id/name/type/slug only, and widening it
 * would change a type five other surfaces read. The roster draws an agent from
 * `avatar_seed || name` and `avatar_style || crew.avatar_style` (see
 * crews-explorer.tsx and agent-canvas.tsx); carrying the same three fields here
 * is what makes the same agent wear the same face in both places.
 */
type AgentAssigneeOption = AssigneeOption & {
  avatar_seed?: string | null
  avatar_style?: string | null
  avatar_url?: string | null
  /** The crew's default style, which an agent with none of its own inherits. */
  crew_avatar_style?: string | null
}

/** The subset of GET /api/v1/agents this modal reads. */
interface AgentRow {
  id: string
  name: string
  slug?: string
  avatar_seed?: string | null
  avatar_style?: string | null
  avatar_url?: string | null
  crew?: { avatar_style?: string | null } | null
}

/**
 * Where the agent list is between "not asked yet" and "here it is".
 *
 * An empty `<CommandGroup>` renders identically whether the crew has nobody in
 * it, the request 500'd, or the request is still in flight — which is the
 * actual defect behind "the Assignee picker offers nobody". Three states, three
 * sentences.
 */
type AgentLoad = "idle" | "loading" | "ready" | "error"

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
  const [agents, setAgents] = useState<AgentAssigneeOption[]>([])
  const [agentLoad, setAgentLoad] = useState<AgentLoad>("idle")
  const [createMore, setCreateMore] = useState(false)
  const [saving, setSaving] = useState(false)
  // What the server said when it said no. The toast stays — this is the copy
  // that is still on screen when you look back up.
  const [refusal, setRefusal] = useState<string | null>(null)

  // Popover states
  const [crewOpen, setCrewOpen] = useState(false)
  const [priorityOpen, setPriorityOpen] = useState(false)
  const [assigneeOpen, setAssigneeOpen] = useState(false)
  const [projectOpen, setProjectOpen] = useState(false)
  const [labelsOpen, setLabelsOpen] = useState(false)
  const [routineOpen, setRoutineOpen] = useState(false)

  const titleRef = useRef<HTMLInputElement>(null)

  // Auto-select a crew when opening.
  //
  // This used to be `crews[0]` — whichever crew the API happened to sort
  // first. In a workspace that has ever run the e2e or smoke suites that is an
  // `e2e-empty-…` / `smoke-…` crew with zero agents, so New issue opened onto a
  // crew whose Assignee picker could only ever offer "Unassigned", on a board
  // full of issues assigned to real agents. The surface looked broken and was
  // in fact reporting the truth about a crew nobody meant to pick.
  //
  // The crew list already carries `_count.agents` (crews.go's crewCountResponse,
  // passed through verbatim by orchestration-page-shell), so preferring a crew
  // somebody is actually in costs no extra fetch and no new prop. It is a
  // DEFAULT, not a restriction: the header still names the crew and still lets
  // you change it to an empty one on purpose.
  //
  // `crews[0]` stays as the fallback, so a workspace whose crews are all empty
  // — or a payload without `_count` — still lands somewhere rather than nowhere.
  useEffect(() => {
    if (!open || crewId || crews.length === 0) return
    const staffed = crews.find((c) => (c._count?.agents ?? 0) > 0)
    setCrewId((staffed ?? crews[0]).id)
  }, [open, crewId, crews])

  // Focus title on open
  useEffect(() => {
    if (open) {
      setTimeout(() => titleRef.current?.focus(), 100)
    }
  }, [open])

  // Fetch agents when crew changes.
  //
  // The failure paths used to be `if (!res.ok || cancelled) return` and a bare
  // `catch {}`, which left `agents` holding whatever it held before and the
  // picker showing the same empty list a genuinely empty crew shows. A refused
  // or dropped request now says so.
  useEffect(() => {
    setAssigneeType(null)
    setAssigneeId(null)
    if (!crewId) { setAgents([]); setAgentLoad("idle"); return }
    let cancelled = false
    setAgents([])
    setAgentLoad("loading")
    async function fetchAgents() {
      try {
        const res = await apiFetch(
          `/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}&crew_id=${encodeURIComponent(crewId)}`,
        )
        if (cancelled) return
        if (!res.ok) { setAgentLoad("error"); return }
        const data = await res.json()
        if (cancelled) return
        const list: AgentRow[] = Array.isArray(data) ? data : data.agents ?? []
        setAgents(
          list.map((a) => ({
            id: a.id, name: a.name, type: "agent" as const, slug: a.slug,
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
    setRefusal(null)
  }

  // Anything typed or picked that closing would throw away. The crew is not
  // in here: it auto-selects on open, so it is never the user's input.
  const dirty =
    title.trim() !== "" ||
    description.trim() !== "" ||
    priority !== "none" ||
    assigneeId !== null ||
    projectId !== null ||
    routineId !== null ||
    selectedLabels.length > 0

  const selectedCrew = crews.find((c) => c.id === crewId)
  const crewPrefix = selectedCrew?.slug?.toUpperCase().slice(0, 3) ?? "CRE"
  const selectedProject = projects.find((p) => p.id === projectId)
  const selectedRoutine = routines.find((r) => r.id === routineId)
  const selectedAgent = assigneeId ? agents.find((a) => a.id === assigneeId) ?? null : null
  const assigneeName = selectedAgent?.name ?? null

  const handleSubmit = useCallback(async () => {
    if (!crewId) { toast.error("Please select a crew"); return }
    if (!title.trim()) { toast.error("Title is required"); return }

    setSaving(true)
    setRefusal(null)
    try {
      const res = await apiFetch(
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
        setRefusal(body?.detail ?? "Failed to create issue")
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
      setRefusal("Failed to create issue")
    } finally {
      setSaving(false)
    }
  }, [crewId, title, description, priority, selectedLabels, assigneeType, assigneeId, projectId, routineId, workspaceId, onCreated, createMore, onOpenChange])

  function toggleLabel(labelId: string) {
    setSelectedLabels((prev) =>
      prev.includes(labelId) ? prev.filter((id) => id !== labelId) : [...prev, labelId],
    )
  }

  return (
    <CreateSurface
      open={open}
      onOpenChange={onOpenChange}
      dirty={dirty}
      discardLabel="this issue"
      size="md"
      onSubmit={handleSubmit}
    >
      {/* ── Header ── */}
      <CreateSurfaceHeader
        concept="issues"
        title="New issue"
        onClose={() => onOpenChange(false)}
        // The crew is the breadcrumb: it says where the issue lands before
        // anything is typed. It is also the only place the crew can be
        // changed, which is why it is a control and not a label.
        context={
          <Popover open={crewOpen} onOpenChange={setCrewOpen}>
            <PopoverTrigger asChild>
              <button
                type="button"
                aria-label="Select crew"
                className="font-medium transition-colors hover:text-foreground"
              >
                {crewPrefix}
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
        }
      />

      {/* ── Body ── */}
      <CreateSurfaceBody className="space-y-1">
        <CreateSurfaceTitleInput
          ref={titleRef}
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder="Issue title"
        />
        <CreateSurfaceDescriptionInput
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="Add description..."
          rows={3}
        />
      </CreateSurfaceBody>

      {/* ── Metadata pills ── */}
      <CreateSurfacePills>
        {/* Status (read-only) */}
        <CreateSurfacePill readOnly>
          <StatusIcon status="BACKLOG" className="h-3.5 w-3.5" />
          <span>Backlog</span>
        </CreateSurfacePill>

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

        {/* Assignee */}
        {crewId && (
          <Popover open={assigneeOpen} onOpenChange={setAssigneeOpen}>
            <PopoverTrigger asChild>
              <CreateSurfacePill
                icon={assigneeType === "agent" ? Bot : User}
                // The pill is the picker's face when it is shut, so it wears
                // the agent's, not a generic robot.
                leading={
                  selectedAgent ? (
                    <AgentAvatar
                      seed={selectedAgent.avatar_seed || selectedAgent.name}
                      style={selectedAgent.avatar_style || selectedAgent.crew_avatar_style}
                      agentId={selectedAgent.id}
                      avatarUrl={selectedAgent.avatar_url}
                      className="h-3.5 w-3.5 shrink-0"
                    />
                  ) : undefined
                }
                accent="purple"
                set={assigneeId !== null}
              >
                <span>{assigneeName ?? "Assignee"}</span>
              </CreateSurfacePill>
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
                  {/* Three ways to have no agents to list, and they are not the
                      same thing. Plain nodes rather than CommandItems: they are
                      not selectable, and cmdk must not filter them away when
                      the search box has text in it. */}
                  {agentLoad === "loading" && (
                    <p className="px-3 py-2 text-[11px] text-muted-foreground">Loading agents…</p>
                  )}
                  {agentLoad === "error" && (
                    <p role="status" className="px-3 py-2 text-[11px] leading-relaxed text-destructive">
                      Agents could not be loaded. Re-pick the crew to try again.
                    </p>
                  )}
                  {agentLoad === "ready" && agents.length === 0 && (
                    <p className="px-3 py-2 text-[11px] leading-relaxed text-muted-foreground">
                      No agents in {selectedCrew?.name ?? "this crew"} — pick another crew in the
                      header to assign someone.
                    </p>
                  )}
                  {agents.length > 0 && (
                    <CommandGroup heading="Agents">
                      {agents.map((agent) => (
                        <CommandItem
                          key={agent.id}
                          onSelect={() => { setAssigneeType("agent"); setAssigneeId(agent.id); setAssigneeOpen(false) }}
                        >
                          {/* Exactly what crews-explorer.tsx and agent-canvas.tsx
                              pass. Every agent in this database has a NULL
                              avatar_seed, so the `|| name` fallback is the whole
                              reason a face appears at all. */}
                          <AgentAvatar
                            seed={agent.avatar_seed || agent.name}
                            style={agent.avatar_style || agent.crew_avatar_style}
                            agentId={agent.id}
                            avatarUrl={agent.avatar_url}
                            className="mr-2 h-4 w-4 shrink-0"
                          />
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
            <CreateSurfacePill
              icon={FolderKanban}
              leading={
                selectedProject ? (
                  <CrewIcon
                    icon={selectedProject.icon || "folder"}
                    color={selectedProject.color}
                    size="sm"
                    className="!h-4 !w-4 !rounded-sm"
                  />
                ) : undefined
              }
              accent="blue"
              set={projectId !== null}
            >
              <span>{selectedProject?.name ?? "Project"}</span>
            </CreateSurfacePill>
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
                      {/* Projects carry a real icon and colour. `|| "folder"` is
                          the fallback project-card-detail.tsx and
                          issue-card-detail.tsx already use; CrewIcon handles the
                          hex-vs-palette-id split via crewColorHex(). */}
                      <CrewIcon
                        icon={p.icon || "folder"}
                        color={p.color}
                        size="sm"
                        className="mr-2 !h-5 !w-5 !rounded-md"
                      />
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
            <CreateSurfacePill
              concept="routines"
              leading={
                selectedRoutine ? (
                  <CrewIcon
                    icon={resolveRoutineIcon(selectedRoutine)}
                    color={resolveRoutineColor(selectedRoutine)}
                    size="sm"
                    className="!h-4 !w-4 !rounded-sm"
                  />
                ) : undefined
              }
              set={routineId !== null}
            >
              <span>{selectedRoutine?.name ?? "Routine"}</span>
            </CreateSurfacePill>
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
                      {/* `pipelines` has no icon or colour column, so both are
                          derived from the slug — the same two functions
                          routines-explorer.tsx uses, because one routine
                          rendering two different icons is worse than none. */}
                      <CrewIcon
                        icon={resolveRoutineIcon(r)}
                        color={resolveRoutineColor(r)}
                        size="sm"
                        className="mr-2 !h-5 !w-5 !rounded-md"
                      />
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
              <CreateSurfacePill icon={Tag} accent="green" set={selectedLabels.length > 0}>
                <span>{selectedLabels.length > 0 ? `${selectedLabels.length} label${selectedLabels.length > 1 ? "s" : ""}` : "Labels"}</span>
              </CreateSurfacePill>
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
      </CreateSurfacePills>

      {/* ── Refusal ── */}
      <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />

      {/* ── Footer ── */}
      <CreateSurfaceFooter
        hint={
          <>
            <kbd className="font-mono">⌘↵</kbd> to create · <kbd className="font-mono">Esc</kbd> to cancel
          </>
        }
        aside={
          <>
            {/* Decorative. The attachment endpoints exist; this modal has
                never called them, and this migration does not start. */}
            <Paperclip className="h-3.5 w-3.5 text-muted-foreground/40" />
            <label className="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer whitespace-nowrap">
              <Switch
                size="sm"
                checked={createMore}
                onCheckedChange={setCreateMore}
              />
              Create more
            </label>
          </>
        }
        onCancel={() => onOpenChange(false)}
        primaryLabel="Create issue"
        primaryDisabled={!title.trim() || !crewId}
        busy={saving}
        onPrimary={handleSubmit}
      />
    </CreateSurface>
  )
}
