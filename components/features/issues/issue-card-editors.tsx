"use client"

// The editors that make the issue card the real issue detail.
//
// They come from three places that each had part of the set and none of which
// had all of it:
//
//   issue-sidebar.tsx            status · priority · assignee · due · estimate
//                                labels · project · relations (read-only)
//                                start / stop / approve / request changes
//   issue-properties-panel.tsx   the same five, plus MILESTONE, which the
//                                sidebar never had
//   issue-detail-inline.tsx      routine binding, relation add/remove,
//                                label creation, and REOPEN, which neither
//                                of the others had
//
// Promoting any one of those screens on its own would have quietly dropped a
// verb. So the union lives here, once, and components/features/issues/
// __tests__/issue-card-editing.test.tsx asserts every trigger by name — a
// picker that disappears fails a test rather than a code review.
//
// Everything is a plain <button> inside a Radix popover rather than a cmdk
// Command: the search is four lines, and a list a test can click is worth more
// here than one that filters itself.

import * as React from "react"
import {
  CalendarDays,
  Check,
  CircleDot,
  Flag,
  FolderKanban,
  GitBranch,
  Hash,
  Play,
  Plus,
  RotateCcw,
  Search,
  Square,
  ThumbsDown,
  ThumbsUp,
  UserCircle2,
  X,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { isImeComposing } from "@/lib/ime"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Button } from "@/components/ui/button"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { ISSUE_STATUSES, ALL_PRIORITIES, RELATION_TYPE_OPTIONS } from "@/components/features/issues/issue-constants"
import { getCrewDotColor } from "@/lib/entities"
import type {
  IssueLabel,
  IssuePriority,
  Milestone,
  Mission,
  MissionStatus,
  Project,
  RelationType,
} from "@/lib/types/mission"

/** An agent that can be put on an issue. The roster row, nothing more. */
export interface PickableAgent {
  id: string
  name: string
  slug?: string
}

/** A routine that can be bound to an issue. */
export interface PickableRoutine {
  id: string
  name: string
  slug: string
}

/**
 * Everything the card needs to write.
 *
 * Passed as one object rather than fifteen props because the host owns all of
 * it together — the endpoints, the workspace, the refetch. When it is absent
 * the card renders read-only, which is what the design preview was and what a
 * viewer-role reader should still get.
 */
export interface IssueCardEdit {
  /** Assignable agents — crew-scoped where the host can narrow it. */
  agents: PickableAgent[]
  /** Every label in the workspace, for the add/remove picker. */
  labels: IssueLabel[]
  projects: Project[]
  routines: PickableRoutine[]
  /** Milestones of the issue's current project. Empty until one is set. */
  milestones: Milestone[]
  /** PATCHes the issue. Resolves true when the write landed. */
  patch: (body: Record<string, unknown>) => Promise<boolean>
  /** Creates a workspace label and attaches it in one gesture. */
  createLabel?: (name: string) => Promise<void>
  addRelation: (targetIdentifier: string, type: RelationType) => Promise<boolean>
  removeRelation: (relationId: string) => Promise<void>
  /** Fires the bound routine now. Absent when nothing is bound. */
  runRoutine?: () => Promise<void>
  /** A write is in flight — pickers stay usable, the actions do not. */
  busy?: boolean
}

/* ------------------------------------------------------------------ *
 *  The popover shell every picker uses                                *
 * ------------------------------------------------------------------ */

interface PickerProps {
  /** Names the trigger for a screen reader and for a test: "Change status". */
  label: string
  /** What the trigger shows when closed. */
  children: React.ReactNode
  /** Rendered inside the popover. */
  menu: (close: () => void) => React.ReactNode
  align?: "start" | "end"
  className?: string
}

function Picker({ label, children, menu, align = "start", className }: PickerProps) {
  const [open, setOpen] = React.useState(false)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label={label}
          className={cn(
            "-mx-1 inline-flex min-w-0 max-w-full items-center gap-1.5 rounded px-1 py-0.5",
            "text-left transition-colors hover:bg-white/[0.06]",
            className,
          )}
        >
          {children}
        </button>
      </PopoverTrigger>
      <PopoverContent align={align} sideOffset={4} className="w-[240px] p-1">
        {menu(() => setOpen(false))}
      </PopoverContent>
    </Popover>
  )
}

/** One row inside a picker. */
function Option({
  onSelect,
  current,
  children,
  tone,
}: {
  onSelect: () => void
  current?: boolean
  children: React.ReactNode
  tone?: "muted" | "destructive"
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      className={cn(
        "flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-[12px] transition-colors hover:bg-white/[0.06]",
        current && "bg-primary/10 text-primary",
        tone === "muted" && "text-muted-foreground",
        tone === "destructive" && "text-destructive/90",
      )}
    >
      {children}
      {current && <Check className="ml-auto h-3.5 w-3.5 shrink-0" />}
    </button>
  )
}

/** The search box a long roster needs. `role="searchbox"` comes from type. */
function PickerSearch({
  value,
  onChange,
  placeholder,
}: {
  value: string
  onChange: (v: string) => void
  placeholder: string
}) {
  return (
    <div className="flex items-center gap-1.5 border-b border-hairline px-2 pb-1.5">
      <Search className="h-3 w-3 shrink-0 text-muted-foreground-soft" />
      <input
        type="search"
        aria-label={placeholder}
        placeholder={placeholder}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="h-6 w-full bg-transparent text-[12px] outline-none placeholder:text-muted-foreground-soft"
      />
    </div>
  )
}

function matches(haystack: string, q: string): boolean {
  return haystack.toLowerCase().includes(q.trim().toLowerCase())
}

/* ------------------------------------------------------------------ *
 *  Property row — label left, editor right                            *
 * ------------------------------------------------------------------ */

export function EditRow({
  icon: Icon,
  label,
  children,
}: {
  icon: React.ComponentType<{ className?: string }>
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="flex items-center gap-2 py-1 text-[12px]">
      <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
      <dt className="w-[70px] shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 flex-1 text-foreground/85">{children}</dd>
    </div>
  )
}

function Muted({ children }: { children: React.ReactNode }) {
  return <span className="text-muted-foreground-soft">{children}</span>
}

/* ------------------------------------------------------------------ *
 *  The pickers                                                        *
 * ------------------------------------------------------------------ */

export function StatusPicker({ issue, edit }: { issue: Mission; edit: IssueCardEdit }) {
  return (
    <Picker
      label="Change status"
      menu={(close) => (
        <>
          {ISSUE_STATUSES.map((s: MissionStatus) => (
            <Option
              key={s}
              current={issue.status === s}
              onSelect={() => {
                void edit.patch({ status: s })
                close()
              }}
            >
              <StatusIcon status={s} className="h-3.5 w-3.5 shrink-0" />
              {statusLabel[s] ?? s}
            </Option>
          ))}
        </>
      )}
    >
      <StatusIcon status={issue.status} className="h-3.5 w-3.5 shrink-0" />
      <span className="truncate">{statusLabel[issue.status] ?? issue.status}</span>
    </Picker>
  )
}

export function PriorityPicker({ issue, edit }: { issue: Mission; edit: IssueCardEdit }) {
  const current = issue.priority ?? "none"
  return (
    <Picker
      label="Change priority"
      menu={(close) => (
        <>
          {ALL_PRIORITIES.map((p: IssuePriority) => (
            <Option
              key={p}
              current={current === p}
              onSelect={() => {
                void edit.patch({ priority: p })
                close()
              }}
            >
              <PriorityIcon priority={p} className="h-3.5 w-3.5 shrink-0" />
              {priorityLabel[p]}
            </Option>
          ))}
        </>
      )}
    >
      <PriorityIcon priority={current} className="h-3.5 w-3.5 shrink-0" />
      <span className="truncate">{priorityLabel[current]}</span>
    </Picker>
  )
}

export function AssigneePicker({ issue, edit }: { issue: Mission; edit: IssueCardEdit }) {
  const [q, setQ] = React.useState("")
  const shown = edit.agents.filter((a) => matches(`${a.name} ${a.slug ?? ""}`, q))
  return (
    <Picker
      label="Change assignee"
      menu={(close) => (
        <>
          <PickerSearch value={q} onChange={setQ} placeholder="Search agents" />
          <div className="max-h-56 overflow-y-auto pt-1">
            <Option
              current={!issue.assignee_id}
              tone="muted"
              onSelect={() => {
                // "" and not null. PATCH reads every field as an optional
                // pointer, so a JSON null is indistinguishable from an
                // omitted field and clears nothing — which is why the old
                // sidebar's Unassigned did nothing at all
                // (internal/api/issue_handler_update.go:129).
                void edit.patch({ assignee_type: "", assignee_id: "" })
                close()
              }}
            >
              Unassigned
            </Option>
            {shown.map((a) => (
              <Option
                key={a.id}
                current={issue.assignee_id === a.id}
                onSelect={() => {
                  void edit.patch({ assignee_type: "agent", assignee_id: a.id })
                  close()
                }}
              >
                <AgentAvatar seed={a.slug ?? a.name} className="h-4 w-4 shrink-0 rounded-full" alt="" />
                <span className="truncate">{a.name}</span>
              </Option>
            ))}
            {shown.length === 0 && (
              <p className="px-2 py-2 text-[11px] text-muted-foreground-soft">No agents match.</p>
            )}
          </div>
        </>
      )}
    >
      {issue.assignee_id ? (
        <>
          <AgentAvatar
            seed={issue.assignee_id ?? issue.assignee_name ?? ""}
            className="h-4 w-4 shrink-0 rounded-full"
            alt=""
          />
          <span className="truncate">{issue.assignee_name ?? "Assigned"}</span>
        </>
      ) : (
        <Muted>Unassigned</Muted>
      )}
    </Picker>
  )
}

export function DueDatePicker({ issue, edit }: { issue: Mission; edit: IssueCardEdit }) {
  return (
    <Picker
      label="Change due date"
      menu={(close) => (
        <div className="space-y-2 p-1">
          <input
            type="date"
            aria-label="Due date"
            defaultValue={issue.due_date?.split("T")[0] ?? ""}
            onChange={(e) => void edit.patch({ due_date: e.target.value || "" })}
            className="h-7 w-full rounded border border-border/60 bg-card px-2 text-[12px] outline-none focus:border-primary/50"
          />
          {issue.due_date && (
            <button
              type="button"
              onClick={() => {
                void edit.patch({ due_date: "" })
                close()
              }}
              className="w-full rounded px-2 py-1 text-left text-[11px] text-muted-foreground transition-colors hover:bg-white/[0.06]"
            >
              Clear due date
            </button>
          )}
        </div>
      )}
    >
      <CalendarDays className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
      {issue.due_date ? (
        <span className="truncate">{issue.due_date.split("T")[0]}</span>
      ) : (
        <Muted>No due date</Muted>
      )}
    </Picker>
  )
}

/** Fibonacci-ish points, the set the sidebar and the panel both used. */
const ESTIMATES = [1, 2, 3, 5, 8, 13, 21]

export function EstimatePicker({ issue, edit }: { issue: Mission; edit: IssueCardEdit }) {
  return (
    <Picker
      label="Change estimate"
      menu={(close) => (
        <>
          {ESTIMATES.map((pts) => (
            <Option
              key={pts}
              current={issue.estimate === pts}
              onSelect={() => {
                void edit.patch({ estimate: pts })
                close()
              }}
            >
              {pts} points
            </Option>
          ))}
          {issue.estimate != null && (
            <Option
              tone="muted"
              onSelect={() => {
                void edit.patch({ estimate: null })
                close()
              }}
            >
              Clear estimate
            </Option>
          )}
        </>
      )}
    >
      <Hash className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
      {issue.estimate != null ? (
        <span className="truncate">{issue.estimate} pts</span>
      ) : (
        <Muted>No estimate</Muted>
      )}
    </Picker>
  )
}

export function MilestonePicker({ issue, edit }: { issue: Mission; edit: IssueCardEdit }) {
  const current = edit.milestones.find((m) => m.id === issue.milestone_id)
  return (
    <Picker
      label="Change milestone"
      menu={(close) => (
        <>
          {edit.milestones.length === 0 ? (
            <p className="px-2 py-2 text-[11px] text-muted-foreground-soft">
              {issue.project_id ? "No milestones in this project." : "Set a project first."}
            </p>
          ) : (
            edit.milestones.map((m) => (
              <Option
                key={m.id}
                current={issue.milestone_id === m.id}
                onSelect={() => {
                  void edit.patch({ milestone_id: m.id })
                  close()
                }}
              >
                <Flag className="h-3 w-3 shrink-0" />
                <span className="truncate">{m.name}</span>
              </Option>
            ))
          )}
          {issue.milestone_id && (
            <Option
              tone="muted"
              onSelect={() => {
                void edit.patch({ milestone_id: "" })
                close()
              }}
            >
              Clear milestone
            </Option>
          )}
        </>
      )}
    >
      <Flag className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
      {current ? <span className="truncate">{current.name}</span> : <Muted>No milestone</Muted>}
    </Picker>
  )
}

export function ProjectPicker({
  issue,
  edit,
  children,
}: {
  issue: Mission
  edit: IssueCardEdit
  children: React.ReactNode
}) {
  const [q, setQ] = React.useState("")
  const shown = edit.projects.filter((p) => matches(p.name, q))
  return (
    <Picker
      label="Change project"
      menu={(close) => (
        <>
          <PickerSearch value={q} onChange={setQ} placeholder="Search projects" />
          <div className="max-h-56 overflow-y-auto pt-1">
            <Option
              current={!issue.project_id}
              tone="muted"
              onSelect={() => {
                void edit.patch({ project_id: "" })
                close()
              }}
            >
              No project
            </Option>
            {shown.map((p) => (
              <Option
                key={p.id}
                current={issue.project_id === p.id}
                onSelect={() => {
                  void edit.patch({ project_id: p.id })
                  close()
                }}
              >
                <span
                  className="h-2.5 w-2.5 shrink-0 rounded-sm"
                  style={{ backgroundColor: getCrewDotColor(p.color) }}
                />
                <span className="truncate">{p.name}</span>
              </Option>
            ))}
          </div>
        </>
      )}
    >
      {children}
    </Picker>
  )
}

export function RoutinePicker({
  issue,
  edit,
  children,
}: {
  issue: Mission
  edit: IssueCardEdit
  children: React.ReactNode
}) {
  const [q, setQ] = React.useState("")
  const shown = edit.routines.filter((r) => matches(`${r.name} ${r.slug}`, q))
  return (
    <Picker
      label="Change routine"
      menu={(close) => (
        <>
          <PickerSearch value={q} onChange={setQ} placeholder="Search routines" />
          <div className="max-h-56 overflow-y-auto pt-1">
            <Option
              current={!issue.routine_id}
              tone="muted"
              onSelect={() => {
                // "" and not null: the PATCH handler reads an empty string as
                // "unbind", the way issue-detail-inline sent it.
                void edit.patch({ routine_id: "" })
                close()
              }}
            >
              No routine
            </Option>
            {shown.map((r) => (
              <Option
                key={r.id}
                current={issue.routine_id === r.id}
                onSelect={() => {
                  void edit.patch({ routine_id: r.id })
                  close()
                }}
              >
                <GitBranch className="h-3 w-3 shrink-0" />
                <span className="min-w-0 flex-1 truncate">{r.name}</span>
              </Option>
            ))}
            {shown.length === 0 && (
              <p className="px-2 py-2 text-[11px] text-muted-foreground-soft">No routines match.</p>
            )}
          </div>
        </>
      )}
    >
      {children}
    </Picker>
  )
}

/**
 * Add / remove a label, and create one that does not exist yet.
 *
 * The create path is the one the sidebar never had: typing a name nothing
 * matches offers to make it, which is how a label gets invented at the moment
 * somebody needs it rather than in a settings screen an hour later.
 */
export function LabelsPicker({ issue, edit }: { issue: Mission; edit: IssueCardEdit }) {
  const [q, setQ] = React.useState("")
  const attached = issue.labels ?? []
  const attachedIds = new Set(attached.map((l) => l.id))
  const shown = edit.labels.filter((l) => matches(l.name, q))
  const typed = q.trim()
  const exact = edit.labels.some((l) => l.name.toLowerCase() === typed.toLowerCase())

  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label="Add label"
          className="rounded p-1 text-muted-foreground-soft transition-colors hover:bg-white/[0.06] hover:text-foreground"
        >
          <Plus className="h-3.5 w-3.5" />
        </button>
      </PopoverTrigger>
      <PopoverContent align="end" sideOffset={4} className="w-[240px] p-1">
        <PickerSearch value={q} onChange={setQ} placeholder="Search labels" />
        <div className="max-h-56 overflow-y-auto pt-1">
          {shown.map((l) => (
            <Option
              key={l.id}
              current={attachedIds.has(l.id)}
              onSelect={() => {
                const next = attachedIds.has(l.id)
                  ? attached.filter((x) => x.id !== l.id).map((x) => x.id)
                  : [...attached.map((x) => x.id), l.id]
                // `labels`, not `label_ids`. The shipped sidebar sent
                // label_ids, which the handler has no field for — so the
                // request reached "No fields to update" and 400ed, and
                // toggling a label on /issues/<id> never worked
                // (internal/api/issue_handler_update.go:36).
                void edit.patch({ labels: next })
              }}
            >
              <span
                className="h-2.5 w-2.5 shrink-0 rounded-full"
                style={{ backgroundColor: l.color }}
              />
              <span className="truncate">{l.name}</span>
            </Option>
          ))}
          {typed && !exact && edit.createLabel && (
            <Option tone="muted" onSelect={() => void edit.createLabel!(typed)}>
              <Plus className="h-3 w-3 shrink-0" />
              <span className="truncate">Create &quot;{typed}&quot;</span>
            </Option>
          )}
          {shown.length === 0 && !typed && (
            <p className="px-2 py-2 text-[11px] text-muted-foreground-soft">
              No labels in this workspace yet.
            </p>
          )}
        </div>
      </PopoverContent>
    </Popover>
  )
}

/**
 * Add a link to another issue.
 *
 * The read side lives in the card (the Links list); this is the write side,
 * which only issue-detail-inline had — the shipped /issues/&lt;id&gt; page could
 * follow a relation but never make one.
 */
export function AddRelationPicker({ edit }: { edit: IssueCardEdit }) {
  const [open, setOpen] = React.useState(false)
  const [target, setTarget] = React.useState("")
  const [type, setType] = React.useState<RelationType>("relates_to")
  const [adding, setAdding] = React.useState(false)

  async function submit() {
    if (!target.trim() || adding) return
    setAdding(true)
    try {
      if (await edit.addRelation(target.trim(), type)) {
        setTarget("")
        setOpen(false)
      }
    } finally {
      setAdding(false)
    }
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label="Add link"
          className="rounded p-1 text-muted-foreground-soft transition-colors hover:bg-white/[0.06] hover:text-foreground"
        >
          <Plus className="h-3.5 w-3.5" />
        </button>
      </PopoverTrigger>
      <PopoverContent align="end" sideOffset={4} className="w-[260px] space-y-2 p-3">
        <input
          aria-label="Target issue identifier"
          value={target}
          onChange={(e) => setTarget(e.target.value)}
          onKeyDown={(e) => {
            // Enter confirms an IME candidate before it means "add".
            if (isImeComposing(e)) return
            if (e.key === "Enter") void submit()
          }}
          placeholder="Target identifier (e.g. ENG-5)"
          className="h-7 w-full rounded border border-border/60 bg-card px-2 text-[12px] outline-none focus:border-primary/50"
        />
        <div className="flex flex-wrap gap-1">
          {RELATION_TYPE_OPTIONS.map((opt) => (
            <button
              key={opt.value}
              type="button"
              onClick={() => setType(opt.value)}
              aria-pressed={type === opt.value}
              className={cn(
                "rounded border px-2 py-0.5 text-[10px] transition-colors",
                type === opt.value
                  ? "border-primary/50 bg-primary/10 text-primary"
                  : "border-border/60 text-muted-foreground hover:text-foreground",
              )}
            >
              {opt.label}
            </button>
          ))}
        </div>
        <Button
          type="button"
          size="sm"
          className="h-7 w-full text-[11px]"
          disabled={!target.trim() || adding}
          onClick={() => void submit()}
        >
          Add link
        </Button>
      </PopoverContent>
    </Popover>
  )
}

/** The X beside a link. Named for the issue it points at, not "remove". */
export function RemoveRelationButton({
  identifier,
  onRemove,
}: {
  identifier: string
  onRemove: () => void
}) {
  return (
    <button
      type="button"
      aria-label={`Remove link to ${identifier}`}
      onClick={onRemove}
      className="rounded p-0.5 text-muted-foreground-soft opacity-0 transition-all hover:bg-white/[0.08] hover:text-destructive focus-visible:opacity-100 group-hover:opacity-100"
    >
      <X className="h-3 w-3" />
    </button>
  )
}

/* ------------------------------------------------------------------ *
 *  Title                                                              *
 * ------------------------------------------------------------------ */

export function TitleEditor({
  title,
  onSave,
}: {
  title: string
  onSave: (next: string) => void
}) {
  const [editing, setEditing] = React.useState(false)
  const [draft, setDraft] = React.useState(title)

  if (editing) {
    return (
      <input
        autoFocus
        aria-label="Issue title"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={() => setEditing(false)}
        onKeyDown={(e) => {
          // The destructive one. This Enter PATCHes the title straight onto
          // the issue, and mid-composition it saves a fragment of what the
          // reader was typing — with no `mission_activity` row recording
          // what the title used to be. Escape is guarded for the same
          // reason: mid-composition it means "cancel this candidate", not
          // "throw the draft away".
          if (isImeComposing(e)) return
          if (e.key === "Enter") {
            e.preventDefault()
            const next = draft.trim()
            if (next && next !== title) onSave(next)
            setEditing(false)
          }
          if (e.key === "Escape") {
            e.preventDefault()
            setDraft(title)
            setEditing(false)
          }
        }}
        className="w-full rounded-md border border-border bg-card px-2 py-1 text-lg font-semibold tracking-tight outline-none focus:border-primary/50"
      />
    )
  }

  return (
    <span className="group/title flex min-w-0 items-center gap-1.5">
      <h1 className="truncate text-lg font-semibold tracking-tight">{title}</h1>
      <button
        type="button"
        aria-label="Edit title"
        onClick={() => {
          setDraft(title)
          setEditing(true)
        }}
        className="shrink-0 rounded p-1 text-muted-foreground-soft opacity-0 transition-opacity hover:text-foreground focus-visible:opacity-100 group-hover/title:opacity-100"
      >
        <svg className="h-3.5 w-3.5" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
          <path d="M11.5 2.5a1.4 1.4 0 0 1 2 2L6 12l-3 1 1-3z" />
        </svg>
      </button>
    </span>
  )
}

/* ------------------------------------------------------------------ *
 *  Workflow                                                           *
 * ------------------------------------------------------------------ */

export type WorkflowAction = "start" | "stop" | "approve" | "request_changes" | "reopen"

/**
 * The five verbs, gated on status the way the server gates them.
 *
 * `Start work` needs an assignee because the endpoint does: starting an
 * unassigned issue has nobody to hand it to, and offering the button anyway
 * only buys a toast.
 */
export function IssueWorkflowActions({
  issue,
  onAction,
  busy,
}: {
  issue: Mission
  onAction: (action: WorkflowAction, comment?: string) => void | Promise<void>
  busy?: boolean
}) {
  const [asking, setAsking] = React.useState(false)
  const [reason, setReason] = React.useState("")

  const canStart = (issue.status === "BACKLOG" || issue.status === "TODO") && !!issue.assignee_id
  const canStop = issue.status === "IN_PROGRESS"
  const canReview = issue.status === "REVIEW"
  const canReopen =
    issue.status === "DONE" || issue.status === "CANCELLED" || issue.status === "COMPLETED"

  return (
    <div className="flex flex-wrap items-center justify-end gap-1.5">
      {canStart && (
        <Button size="sm" className="h-8 gap-1.5 text-[12px]" disabled={busy} onClick={() => void onAction("start", undefined)}>
          <Play className="h-3.5 w-3.5" />
          Start work
        </Button>
      )}
      {canStop && (
        <Button
          size="sm"
          variant="outline"
          className="h-8 gap-1.5 text-[12px]"
          disabled={busy}
          onClick={() => void onAction("stop", undefined)}
        >
          <Square className="h-3.5 w-3.5" />
          Stop
        </Button>
      )}
      {canReview && (
        <>
          <Button
            size="sm"
            className="h-8 gap-1.5 text-[12px]"
            disabled={busy}
            onClick={() => void onAction("approve", undefined)}
          >
            <ThumbsUp className="h-3.5 w-3.5" />
            Approve
          </Button>
          <Button
            size="sm"
            variant="outline"
            className="h-8 gap-1.5 text-[12px]"
            disabled={busy}
            onClick={() => setAsking((v) => !v)}
          >
            <ThumbsDown className="h-3.5 w-3.5" />
            Request changes
          </Button>
        </>
      )}
      {canReopen && (
        <Button
          size="sm"
          variant="outline"
          className="h-8 gap-1.5 text-[12px]"
          disabled={busy}
          onClick={() => void onAction("reopen", undefined)}
        >
          <RotateCcw className="h-3.5 w-3.5" />
          Reopen
        </Button>
      )}

      {asking && (
        <div className="mt-2 w-[280px] max-w-full space-y-2 rounded-lg border border-warn/30 bg-card p-2.5">
          <textarea
            aria-label="What needs to change"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder="What needs to change…"
            className="h-20 w-full resize-none rounded-md border border-border/60 bg-background p-2 text-[12px] outline-none focus:border-primary/50"
          />
          <div className="flex justify-end gap-2">
            <Button
              size="sm"
              variant="ghost"
              className="h-7 text-[11px]"
              onClick={() => {
                setAsking(false)
                setReason("")
              }}
            >
              Cancel
            </Button>
            <Button
              size="sm"
              className="h-7 text-[11px]"
              disabled={busy}
              onClick={() => {
                void onAction("request_changes", reason.trim() || undefined)
                setAsking(false)
                setReason("")
              }}
            >
              Send
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

/** Fires the routine bound to this issue. Rendered beside the Routine card. */
export function RunRoutineButton({ onRun, busy }: { onRun: () => void; busy?: boolean }) {
  return (
    <button
      type="button"
      aria-label="Run this routine now"
      onClick={onRun}
      disabled={busy}
      className="inline-flex items-center gap-1 rounded bg-success/15 px-2 py-0.5 text-[10px] font-medium text-success transition-colors hover:bg-success/25 disabled:opacity-50"
    >
      <Play className="h-3 w-3" />
      Run
    </button>
  )
}

/** Re-exported so the card can draw the same icon set as the pickers. */
export const EDIT_ICONS = {
  status: CircleDot,
  priority: Flag,
  assignee: UserCircle2,
  project: FolderKanban,
} as const
