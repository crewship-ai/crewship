"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import {
  Paperclip,
  User,
  Bot,
  UserX,
  Check,
  Tag,
  FolderKanban,
  Calendar,
  Hash,
  Milestone as MilestoneIcon,
  ListTree,
  Plus,
  Search,
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
import { LABEL_PRESET_COLORS } from "@/lib/colors"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { StatusIcon } from "@/components/features/issues/status-icon"
import type { AssigneeOption } from "@/components/features/issues/assignee-picker"
import { cn } from "@/lib/utils"
import { isImeComposing } from "@/lib/ime"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "sonner"
import type { IssueLabel, IssuePriority, Milestone, Project } from "@/lib/types/mission"
import type { CrewSummary } from "@/lib/types/orchestration"

const PRIORITIES: IssuePriority[] = ["urgent", "high", "medium", "low", "none"]

/** Fibonacci-ish points — the same set issue-card-editors.tsx's EstimatePicker
 *  uses, so an issue estimated here reads the same way once it is on a board. */
const ESTIMATES = [1, 2, 3, 5, 8, 13, 21]

/** The subset of GET /api/v1/issues this modal reads for the parent picker. */
interface IssueRow {
  id: string
  identifier: string | null
  title: string
}

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
 * Where a fetched list is between "not asked yet" and "here it is".
 *
 * An empty `<CommandGroup>` renders identically whether the crew has nobody in
 * it, the request 500'd, or the request is still in flight — which is the
 * actual defect behind "the Assignee picker offers nobody". Three states,
 * three sentences. Shared by the assignee, milestone and parent-issue
 * pickers below — three fetches, one shape.
 */
type PickerLoad = "idle" | "loading" | "ready" | "error"

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
  /** Refetch the workspace label list after one is created here. Optional —
   *  without it the Create action is hidden rather than silently useless. */
  onLabelsChanged?: () => void
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
  onLabelsChanged,
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
  const [agentLoad, setAgentLoad] = useState<PickerLoad>("idle")
  /** Bumped by the error state's Try again, purely to re-run the fetch effect. */
  const [agentReload, setAgentReload] = useState(0)
  // due_date, estimate, parent_issue_id and milestone_id: the API accepts all
  // four (internal/api/issue_handler_create.go:38-42) and nothing on this
  // modal ever offered them.
  const [dueDate, setDueDate] = useState("")
  const [estimate, setEstimate] = useState<number | null>(null)
  const [parentIssueId, setParentIssueId] = useState<string | null>(null)
  const [parentCandidates, setParentCandidates] = useState<IssueRow[]>([])
  const [parentLoad, setParentLoad] = useState<PickerLoad>("idle")
  const [parentReload, setParentReload] = useState(0)
  const [milestoneId, setMilestoneId] = useState<string | null>(null)
  const [milestones, setMilestones] = useState<Milestone[]>([])
  const [milestoneLoad, setMilestoneLoad] = useState<PickerLoad>("idle")
  const [milestoneReload, setMilestoneReload] = useState(0)
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
  const [labelQuery, setLabelQuery] = useState("")
  const [creatingLabel, setCreatingLabel] = useState(false)
  const [routineOpen, setRoutineOpen] = useState(false)
  const [dueDateOpen, setDueDateOpen] = useState(false)
  const [estimateOpen, setEstimateOpen] = useState(false)
  const [milestoneOpen, setMilestoneOpen] = useState(false)
  const [parentOpen, setParentOpen] = useState(false)

  // A ref, not the `saving` state, and it is the difference between one create
  // and two. The footer's primary is disabled while `saving` is true, but ⌘↵
  // does not go through the footer — the shell calls `onSubmit` on the
  // keystroke and does not know this surface is busy. State also does not flip
  // until React re-renders, so two fast presses both read the old value. The
  // ref flips synchronously, before any await.
  const submittingRef = useRef(false)

  const titleRef = useRef<HTMLInputElement>(null)

  /**
   * Make a label from what is typed in the filter box, and select it.
   *
   * `POST /api/v1/labels` and the LabelsDialog behind Issues → Labels have
   * always been able to do this; the create path could not reach either.
   * Noticing halfway through writing an issue that the label you want does
   * not exist meant abandoning the issue, opening another dialog, making the
   * label, and starting again.
   *
   * The colour is taken from the preset ring rather than asked for. A colour
   * picker here would be a third layer of UI inside a popover inside a
   * dialog, for a decision nobody has an opinion about at this moment — and
   * Issues → Labels can still change it afterwards. Cycling by the current
   * label count keeps consecutive labels visually distinct instead of
   * handing everyone the same one.
   */
  const createLabel = useCallback(async () => {
    const name = labelQuery.trim()
    if (!name || creatingLabel) return
    setCreatingLabel(true)
    try {
      const color = LABEL_PRESET_COLORS[labels.length % LABEL_PRESET_COLORS.length].value
      const res = await apiFetch(`/api/v1/labels?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name, color }),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        toast.error(body?.error ?? body?.detail ?? "Could not create the label")
        return
      }
      const created = await res.json() as { id?: string }
      // Select it straight away: making it was a step towards putting it on
      // THIS issue, and leaving it unticked makes you find it again.
      if (created?.id) setSelectedLabels((prev) => [...prev, created.id!])
      setLabelQuery("")
      onLabelsChanged?.()
    } catch {
      toast.error("Could not create the label")
    } finally {
      setCreatingLabel(false)
    }
  }, [labelQuery, creatingLabel, labels.length, workspaceId, onLabelsChanged])

  const filteredLabels = useMemo(() => {
    const q = labelQuery.trim().toLowerCase()
    if (!q) return labels
    return labels.filter((l) => l.name.toLowerCase().includes(q))
  }, [labels, labelQuery])

  /**
   * Offer to create only when the typed name is not already a label.
   *
   * Exact-match, not "no results": typing "bug" while a Bug label exists
   * filters to it AND would otherwise offer to make a second one, which the
   * server accepts and nobody wants. Gated on onLabelsChanged, because
   * without a way to refetch, a created label would not appear in the list
   * it was created from.
   */
  const canCreateLabel = useMemo(() => {
    const q = labelQuery.trim()
    if (!q || !onLabelsChanged) return false
    return !labels.some((l) => l.name.toLowerCase() === q.toLowerCase())
  }, [labelQuery, labels, onLabelsChanged])

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
  }, [crewId, workspaceId, agentReload])

  // Fetch parent-issue candidates when crew changes. Same shape as the
  // agents fetch above: GET /api/v1/issues has no picker anywhere in the app
  // today (parent_issue_id is edited nowhere else either), so this is a new
  // fetch rather than a reused one — but it is the same crew-scoped,
  // three-state pattern as Assignee, not a new one invented for this field.
  useEffect(() => {
    setParentIssueId(null)
    if (!crewId) { setParentCandidates([]); setParentLoad("idle"); return }
    let cancelled = false
    setParentCandidates([])
    setParentLoad("loading")
    async function fetchParents() {
      try {
        const res = await apiFetch(
          `/api/v1/issues?workspace_id=${encodeURIComponent(workspaceId)}&crew_id=${encodeURIComponent(crewId)}&limit=50`,
        )
        if (cancelled) return
        if (!res.ok) { setParentLoad("error"); return }
        const data = await res.json()
        if (cancelled) return
        const list: IssueRow[] = Array.isArray(data) ? data : data.issues ?? []
        setParentCandidates(list.map((i) => ({ id: i.id, identifier: i.identifier, title: i.title })))
        setParentLoad("ready")
      } catch {
        if (!cancelled) setParentLoad("error")
      }
    }
    fetchParents()
    return () => { cancelled = true }
  }, [crewId, workspaceId, parentReload])

  // Fetch milestones when project changes. Milestones belong to a project
  // (GET /api/v1/projects/{projectId}/milestones), so the picker has nothing
  // to offer until one is picked — mirrored from issue-detail-surface.tsx's
  // "Milestones belong to the project, so they reset with it" effect, with
  // the same three-state load as the pickers above instead of that effect's
  // silent `.catch(() => {})`.
  useEffect(() => {
    setMilestoneId(null)
    if (!projectId) { setMilestones([]); setMilestoneLoad("idle"); return }
    const pid = projectId
    let cancelled = false
    setMilestones([])
    setMilestoneLoad("loading")
    async function fetchMilestones() {
      try {
        const res = await apiFetch(
          `/api/v1/projects/${encodeURIComponent(pid)}/milestones?workspace_id=${encodeURIComponent(workspaceId)}`,
        )
        if (cancelled) return
        if (!res.ok) { setMilestoneLoad("error"); return }
        const data = await res.json()
        if (cancelled) return
        setMilestones(Array.isArray(data) ? data : data.milestones ?? [])
        setMilestoneLoad("ready")
      } catch {
        if (!cancelled) setMilestoneLoad("error")
      }
    }
    fetchMilestones()
    return () => { cancelled = true }
  }, [projectId, workspaceId, milestoneReload])

  function reset() {
    setTitle("")
    setDescription("")
    setPriority("none")
    setAssigneeType(null)
    setAssigneeId(null)
    setProjectId(null)
    setSelectedLabels([])
    setRoutineId(null)
    setDueDate("")
    setEstimate(null)
    setParentIssueId(null)
    setMilestoneId(null)
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
    dueDate !== "" ||
    estimate !== null ||
    parentIssueId !== null ||
    milestoneId !== null ||
    routineId !== null ||
    selectedLabels.length > 0

  const selectedCrew = crews.find((c) => c.id === crewId)
  const crewPrefix = selectedCrew?.slug?.toUpperCase().slice(0, 3) ?? "CRE"
  const selectedProject = projects.find((p) => p.id === projectId)
  const selectedRoutine = routines.find((r) => r.id === routineId)
  const selectedAgent = assigneeId ? agents.find((a) => a.id === assigneeId) ?? null : null
  const assigneeName = selectedAgent?.name ?? null
  const selectedMilestone = milestoneId ? milestones.find((m) => m.id === milestoneId) ?? null : null
  const selectedParent = parentIssueId ? parentCandidates.find((i) => i.id === parentIssueId) ?? null : null

  const handleSubmit = useCallback(async () => {
    if (submittingRef.current) return
    if (!crewId) { toast.error("Please select a crew"); return }
    if (!title.trim()) { toast.error("Title is required"); return }

    submittingRef.current = true
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
            // Where a value is optional, absent must stay absent on the wire —
            // `?? undefined` rather than "" / null, so readJSON's *string /
            // *int pointers on issue_handler_create.go:38-42 land nil, not a
            // zero value the server would try to validate or store.
            due_date: dueDate || undefined,
            estimate: estimate ?? undefined,
            parent_issue_id: parentIssueId ?? undefined,
            milestone_id: milestoneId ?? undefined,
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
      submittingRef.current = false
    }
  }, [crewId, title, description, priority, selectedLabels, assigneeType, assigneeId, projectId, routineId, dueDate, estimate, parentIssueId, milestoneId, workspaceId, onCreated, createMore, onOpenChange])

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
          <Popover open={crewOpen} onOpenChange={setCrewOpen} modal>
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
                        {/* Same treatment as the Project and Routine pickers below
                            (`icon || "folder"` fallback, CrewIcon's hex-vs-palette
                            split via crewColorHex()) — this popover was the one
                            picker in the file still drawing a bare name. */}
                        <CrewIcon
                          icon={crew.icon || "folder"}
                          color={crew.color}
                          size="sm"
                          className="mr-2 !h-5 !w-5 !rounded-md"
                        />
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
        <Popover open={priorityOpen} onOpenChange={setPriorityOpen} modal>
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
          <Popover open={assigneeOpen} onOpenChange={setAssigneeOpen} modal>
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
                    // Stacked, not a row: the popover is 200px wide and a row
                    // put "Try again" in a two-line column beside the message.
                    <div role="status" className="flex flex-col items-start gap-1 px-3 py-2">
                      <span className="text-[11px] leading-relaxed text-destructive">
                        Agents could not be loaded.
                      </span>
                      {/* This used to read "re-pick the crew to try again",
                          which was advice that did not work: picking the crew
                          that is already picked sets the same id, React bails
                          out of the render, and the effect never re-runs. A
                          button that bumps a nonce is the retry the sentence
                          was promising. */}
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
        <Popover open={projectOpen} onOpenChange={setProjectOpen} modal>
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

        {/* Milestone — belongs to the project, so it resets when the project
            does (see the fetch effect above). Always rendered, same as the
            sidebar's MilestonePicker (issue-card-editors.tsx), rather than
            hidden until a project is picked — the "set a project first" state
            below is what tells you why the list is empty. */}
        <Popover open={milestoneOpen} onOpenChange={setMilestoneOpen} modal>
          <PopoverTrigger asChild>
            <CreateSurfacePill
              icon={MilestoneIcon}
              accent="purple"
              set={milestoneId !== null}
            >
              <span>{selectedMilestone?.name ?? "Milestone"}</span>
            </CreateSurfacePill>
          </PopoverTrigger>
          <PopoverContent className="w-[220px] p-0" align="start">
            <Command>
              <CommandInput placeholder="Search milestone..." className="h-8 text-xs" />
              <CommandList>
                <CommandEmpty>No results found.</CommandEmpty>
                <CommandGroup>
                  <CommandItem onSelect={() => { setMilestoneId(null); setMilestoneOpen(false) }}>
                    <span className="text-xs text-muted-foreground">No milestone</span>
                    {!milestoneId && <Check className="ml-auto h-3.5 w-3.5" />}
                  </CommandItem>
                </CommandGroup>
                {!projectId && (
                  <p className="px-3 py-2 text-[11px] leading-relaxed text-muted-foreground">
                    Set a project first — milestones belong to a project.
                  </p>
                )}
                {projectId && milestoneLoad === "loading" && (
                  <p className="px-3 py-2 text-[11px] text-muted-foreground">Loading milestones…</p>
                )}
                {projectId && milestoneLoad === "error" && (
                  <div role="status" className="flex flex-col items-start gap-1 px-3 py-2">
                    <span className="text-[11px] leading-relaxed text-destructive">
                      Milestones could not be loaded.
                    </span>
                    <button
                      type="button"
                      onClick={() => setMilestoneReload((n) => n + 1)}
                      className="text-[11px] font-medium text-primary underline-offset-2 hover:underline"
                    >
                      Try again
                    </button>
                  </div>
                )}
                {projectId && milestoneLoad === "ready" && milestones.length === 0 && (
                  <p className="px-3 py-2 text-[11px] leading-relaxed text-muted-foreground">
                    No milestones in {selectedProject?.name ?? "this project"}.
                  </p>
                )}
                {milestones.length > 0 && (
                  <CommandGroup heading="Milestones">
                    {milestones.map((m) => (
                      <CommandItem
                        key={m.id}
                        onSelect={() => { setMilestoneId(m.id); setMilestoneOpen(false) }}
                      >
                        <MilestoneIcon className="mr-2 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                        <span className="text-xs">{m.name}</span>
                        {milestoneId === m.id && <Check className="ml-auto h-3.5 w-3.5" />}
                      </CommandItem>
                    ))}
                  </CommandGroup>
                )}
              </CommandList>
            </Command>
          </PopoverContent>
        </Popover>

        {/* Routine — bind a saved routine to handle this issue */}
        <Popover open={routineOpen} onOpenChange={setRoutineOpen} modal>
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

        {/* Due date */}
        <Popover open={dueDateOpen} onOpenChange={setDueDateOpen} modal>
          <PopoverTrigger asChild>
            <CreateSurfacePill icon={Calendar} accent="amber" set={dueDate !== ""}>
              <span>{dueDate || "Due date"}</span>
            </CreateSurfacePill>
          </PopoverTrigger>
          <PopoverContent className="w-auto p-3" align="start">
            <p className="text-xs text-muted-foreground mb-2">Due date</p>
            <input
              type="date"
              value={dueDate}
              onChange={(e) => { setDueDate(e.target.value); setDueDateOpen(false) }}
              className="bg-transparent text-sm text-foreground outline-none border border-white/[0.1] rounded-md px-2 py-1"
            />
            {dueDate && (
              <button
                onClick={() => { setDueDate(""); setDueDateOpen(false) }}
                className="block mt-2 text-xs text-muted-foreground hover:text-foreground"
              >
                Clear
              </button>
            )}
          </PopoverContent>
        </Popover>

        {/* Estimate — the same Fibonacci-ish point set as the sidebar's
            EstimatePicker (issue-card-editors.tsx), so an issue estimated
            here reads the same once it lands on a board. */}
        <Popover open={estimateOpen} onOpenChange={setEstimateOpen} modal>
          <PopoverTrigger asChild>
            <CreateSurfacePill icon={Hash} accent="teal" set={estimate !== null}>
              <span>{estimate != null ? `${estimate} pts` : "Estimate"}</span>
            </CreateSurfacePill>
          </PopoverTrigger>
          <PopoverContent className="w-[140px] p-1" align="start">
            {ESTIMATES.map((pts) => (
              <button
                key={pts}
                onClick={() => { setEstimate(pts); setEstimateOpen(false) }}
                className={cn(
                  "w-full flex items-center gap-2 px-2 py-1.5 rounded-md text-xs hover:bg-white/[0.08] transition-colors",
                  estimate === pts ? "text-foreground bg-white/[0.06]" : "text-muted-foreground",
                )}
              >
                <span>{pts} points</span>
                {estimate === pts && <Check className="ml-auto h-3 w-3" />}
              </button>
            ))}
            {estimate != null && (
              <button
                onClick={() => { setEstimate(null); setEstimateOpen(false) }}
                className="w-full flex items-center gap-2 px-2 py-1.5 rounded-md text-xs text-muted-foreground hover:bg-white/[0.08] transition-colors"
              >
                Clear estimate
              </button>
            )}
          </PopoverContent>
        </Popover>

        {/* Parent issue — crew-scoped, same fetch shape as Assignee. No
            picker for parent_issue_id exists anywhere else in the app (see
            the fetch effect above), so this is new rather than reused. */}
        <Popover open={parentOpen} onOpenChange={setParentOpen} modal>
          <PopoverTrigger asChild>
            <CreateSurfacePill icon={ListTree} accent="slate" set={parentIssueId !== null}>
              <span>{selectedParent ? (selectedParent.identifier ?? selectedParent.title) : "Parent issue"}</span>
            </CreateSurfacePill>
          </PopoverTrigger>
          <PopoverContent className="w-[260px] p-0" align="start">
            <Command>
              <CommandInput placeholder="Search issues..." className="h-8 text-xs" />
              <CommandList>
                <CommandEmpty>No results found.</CommandEmpty>
                <CommandGroup>
                  <CommandItem onSelect={() => { setParentIssueId(null); setParentOpen(false) }}>
                    <span className="text-xs text-muted-foreground">No parent</span>
                    {!parentIssueId && <Check className="ml-auto h-3.5 w-3.5" />}
                  </CommandItem>
                </CommandGroup>
                {parentLoad === "loading" && (
                  <p className="px-3 py-2 text-[11px] text-muted-foreground">Loading issues…</p>
                )}
                {parentLoad === "error" && (
                  <div role="status" className="flex flex-col items-start gap-1 px-3 py-2">
                    <span className="text-[11px] leading-relaxed text-destructive">
                      Issues could not be loaded.
                    </span>
                    <button
                      type="button"
                      onClick={() => setParentReload((n) => n + 1)}
                      className="text-[11px] font-medium text-primary underline-offset-2 hover:underline"
                    >
                      Try again
                    </button>
                  </div>
                )}
                {parentLoad === "ready" && parentCandidates.length === 0 && (
                  <p className="px-3 py-2 text-[11px] leading-relaxed text-muted-foreground">
                    No other issues in {selectedCrew?.name ?? "this crew"} to link as a parent.
                  </p>
                )}
                {parentCandidates.length > 0 && (
                  <CommandGroup heading="Issues">
                    {parentCandidates.map((i) => (
                      <CommandItem
                        key={i.id}
                        onSelect={() => { setParentIssueId(i.id); setParentOpen(false) }}
                      >
                        <span className="mr-2 shrink-0 text-[10px] tabular-nums text-muted-foreground-soft">
                          {i.identifier ?? "—"}
                        </span>
                        <span className="truncate text-xs">{i.title}</span>
                        {parentIssueId === i.id && <Check className="ml-auto h-3.5 w-3.5 shrink-0" />}
                      </CommandItem>
                    ))}
                  </CommandGroup>
                )}
              </CommandList>
            </Command>
          </PopoverContent>
        </Popover>

        {/* Labels
         *
         * The pill renders even with no labels in the workspace. Hiding it
         * meant a workspace that had not made any showed no Labels control at
         * all, which reads as "issues do not have labels" rather than "you
         * have none yet".
         *
         * The list is a search-and-scroll, not a bare list: every other
         * picker in this row already has a Command with a search box, and a
         * workspace with two dozen labels made this the one that asked you to
         * find yours by eye. */}
        <Popover open={labelsOpen} onOpenChange={setLabelsOpen} modal>
          <PopoverTrigger asChild>
            <CreateSurfacePill icon={Tag} accent="green" set={selectedLabels.length > 0}>
              <span>{selectedLabels.length > 0 ? `${selectedLabels.length} label${selectedLabels.length > 1 ? "s" : ""}` : "Labels"}</span>
            </CreateSurfacePill>
          </PopoverTrigger>
          <PopoverContent className="w-[260px] p-1" align="start">
            <div className="relative mb-1">
              <Search
                className="pointer-events-none absolute left-2 top-1/2 h-3 w-3 -translate-y-1/2 text-muted-foreground-soft"
                aria-hidden="true"
              />
              <input
                value={labelQuery}
                onChange={(e) => setLabelQuery(e.target.value)}
                onKeyDown={(e) => {
                  // A CJK user presses Enter to accept an IME candidate. Treat
                  // that as a commit and a half-composed string becomes a
                  // label. Same guard the egress allowlist input carries, for
                  // the same reason — this is the other place Enter turns free
                  // text into a row.
                  if (isImeComposing(e)) return
                  // Enter creates when nothing matches — the same key that
                  // would otherwise do nothing here.
                  if (e.key === "Enter" && canCreateLabel) {
                    e.preventDefault()
                    void createLabel()
                  }
                }}
                placeholder={labels.length === 0 ? "Name a new label…" : "Filter or create…"}
                aria-label="Filter or create a label"
                className="h-7 w-full rounded-md border border-hairline bg-background pl-7 pr-2 text-xs outline-none transition-colors focus:border-primary"
              />
            </div>

            <div className="max-h-[200px] overflow-y-auto">
              {filteredLabels.map((label) => (
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

              {/* The way out of "the label I want does not exist". Without it
                  the answer was: abandon the issue, open Issues → Labels,
                  make the label, start again. */}
              {canCreateLabel && (
                <button
                  onClick={() => void createLabel()}
                  disabled={creatingLabel}
                  className="w-full flex items-center gap-2 px-2 py-1.5 rounded-md text-xs text-primary hover:bg-primary/[0.12] transition-colors disabled:opacity-50"
                >
                  <Plus className="h-3.5 w-3.5 shrink-0" />
                  <span className="min-w-0 truncate">
                    {creatingLabel ? "Creating…" : <>Create &ldquo;{labelQuery.trim()}&rdquo;</>}
                  </span>
                </button>
              )}

              {filteredLabels.length === 0 && !canCreateLabel && (
                <p className="px-2 py-3 text-center text-[11px] leading-relaxed text-muted-foreground">
                  {labels.length === 0
                    ? "No labels in this workspace yet — type a name above to make one."
                    : `Nothing matches “${labelQuery}”.`}
                </p>
              )}
            </div>
          </PopoverContent>
        </Popover>
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
