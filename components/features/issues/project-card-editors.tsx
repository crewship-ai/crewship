"use client"

// The editors that make the project card the real project detail.
//
// They come from project-detail-inline.tsx, which was a 360px right rail
// rendered at full width: a 72px label column on the left, `justify-end`
// values on the right, and a metre of nothing between them on a wide screen.
// The controls were fine; the container was the problem. So the controls move
// here and the container does not.
//
// One addition rather than a port: Dates. The rail drew a row reading "Set
// dates" that was a plain PropertyRow with no popover behind it — the only
// thing it could do was say the dates out loud. PATCH /api/v1/projects has
// taken start_date and target_date since it was written.

import * as React from "react"

import { cn } from "@/lib/utils"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIconPopover } from "@/components/crew-icon-popover"
import { PriorityIcon } from "@/components/features/issues/priority-icon"
import { ProjectStatusIcon } from "@/components/features/issues/project-status-icon"
import {
  HEALTH_OPTIONS,
  PRIORITY_OPTIONS,
  PROJECT_STATUSES,
} from "@/components/features/issues/issue-constants"
import type { PickableAgent } from "@/components/features/issues/issue-card-editors"
import type { IssuePriority, Project, ProjectStatus } from "@/lib/types/mission"

/** What the project card needs to write. Absent, the card renders read-only. */
export interface ProjectCardEdit {
  /** Agents that can lead the project. */
  agents: PickableAgent[]
  patch: (body: Record<string, unknown>) => Promise<boolean>
  busy?: boolean
}

function Trigger({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <PopoverTrigger asChild>
      <button
        type="button"
        aria-label={label}
        className="-mx-1 inline-flex min-w-0 max-w-full items-center gap-1.5 rounded px-1 py-0.5 text-left transition-colors hover:bg-white/[0.06]"
      >
        {children}
      </button>
    </PopoverTrigger>
  )
}

function Option({
  onSelect,
  current,
  children,
  tone,
}: {
  onSelect: () => void
  current?: boolean
  children: React.ReactNode
  tone?: "muted"
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      className={cn(
        "flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-[12px] transition-colors hover:bg-white/[0.06]",
        current && "bg-primary/10 text-primary",
        tone === "muted" && "text-muted-foreground",
      )}
    >
      {children}
    </button>
  )
}

export function ProjectStatusPicker({
  project,
  edit,
}: {
  project: Project
  edit: ProjectCardEdit
}) {
  const [open, setOpen] = React.useState(false)
  const current = PROJECT_STATUSES.find((s) => s.value === project.status)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <Trigger label="Change project status">
        <ProjectStatusIcon status={project.status} className="h-3.5 w-3.5 shrink-0" />
        <span className="truncate text-[12px]">{current?.label ?? project.status}</span>
      </Trigger>
      <PopoverContent align="start" sideOffset={4} className="w-[200px] p-1">
        {PROJECT_STATUSES.map((s) => (
          <Option
            key={s.value}
            current={s.value === project.status}
            onSelect={() => {
              void edit.patch({ status: s.value })
              setOpen(false)
            }}
          >
            <ProjectStatusIcon status={s.value as ProjectStatus} className="h-3.5 w-3.5 shrink-0" />
            {s.label}
          </Option>
        ))}
      </PopoverContent>
    </Popover>
  )
}

export function ProjectPriorityPicker({
  project,
  edit,
}: {
  project: Project
  edit: ProjectCardEdit
}) {
  const [open, setOpen] = React.useState(false)
  const current = PRIORITY_OPTIONS.find((p) => p.value === (project.priority ?? "none"))
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <Trigger label="Change project priority">
        <PriorityIcon priority={project.priority ?? "none"} className="h-3.5 w-3.5 shrink-0" />
        <span className="truncate text-[12px]">{current?.label ?? "No priority"}</span>
      </Trigger>
      <PopoverContent align="start" sideOffset={4} className="w-[200px] p-1">
        {PRIORITY_OPTIONS.map((p) => (
          <Option
            key={p.value}
            current={p.value === project.priority}
            onSelect={() => {
              void edit.patch({ priority: p.value })
              setOpen(false)
            }}
          >
            <PriorityIcon priority={p.value as IssuePriority} className="h-3.5 w-3.5 shrink-0" />
            {p.label}
          </Option>
        ))}
      </PopoverContent>
    </Popover>
  )
}

export function ProjectHealthPicker({
  project,
  edit,
}: {
  project: Project
  edit: ProjectCardEdit
}) {
  const [open, setOpen] = React.useState(false)
  const current = HEALTH_OPTIONS.find((h) => h.value === project.health)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <Trigger label="Change health">
        <span className={cn("truncate text-[12px] font-medium", current?.color)}>
          {current?.label ?? project.health}
        </span>
      </Trigger>
      <PopoverContent align="start" sideOffset={4} className="w-[200px] p-1">
        {HEALTH_OPTIONS.map((h) => (
          <Option
            key={h.value}
            current={h.value === project.health}
            onSelect={() => {
              void edit.patch({ health: h.value })
              setOpen(false)
            }}
          >
            <span className={h.color}>{h.label}</span>
          </Option>
        ))}
      </PopoverContent>
    </Popover>
  )
}

export function ProjectLeadPicker({
  project,
  edit,
}: {
  project: Project
  edit: ProjectCardEdit
}) {
  const [open, setOpen] = React.useState(false)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <Trigger label="Change lead">
        {project.lead_id ? (
          <>
            <AgentAvatar seed={project.lead_id} className="h-4 w-4 shrink-0 rounded-full" alt="" />
            <span className="truncate text-[12px]">{project.lead_name ?? "Lead"}</span>
          </>
        ) : (
          <span className="text-[12px] text-muted-foreground-soft">No lead</span>
        )}
      </Trigger>
      <PopoverContent align="start" sideOffset={4} className="w-[220px] p-1">
        <Option
          tone="muted"
          current={!project.lead_id}
          onSelect={() => {
            // "" and not null — the handler reads every field as an optional
            // pointer, so a JSON null is the same as an omitted field.
            void edit.patch({ lead_type: "", lead_id: "" })
            setOpen(false)
          }}
        >
          No lead
        </Option>
        {edit.agents.map((a) => (
          <Option
            key={a.id}
            current={project.lead_id === a.id}
            onSelect={() => {
              void edit.patch({ lead_type: "agent", lead_id: a.id })
              setOpen(false)
            }}
          >
            <AgentAvatar seed={a.slug ?? a.name} className="h-4 w-4 shrink-0 rounded-full" alt="" />
            <span className="truncate">{a.name}</span>
          </Option>
        ))}
      </PopoverContent>
    </Popover>
  )
}

export function ProjectDatesPicker({
  project,
  edit,
  children,
}: {
  project: Project
  edit: ProjectCardEdit
  children: React.ReactNode
}) {
  return (
    <Popover>
      <Trigger label="Change dates">{children}</Trigger>
      <PopoverContent align="start" sideOffset={4} className="w-[220px] space-y-2 p-3">
        <label className="block text-[10px] uppercase tracking-wider text-muted-foreground-soft">
          Start date
          <input
            type="date"
            aria-label="Start date"
            defaultValue={project.start_date?.split("T")[0] ?? ""}
            onChange={(e) => void edit.patch({ start_date: e.target.value || "" })}
            className="mt-1 h-7 w-full rounded border border-border/60 bg-card px-2 text-[12px] normal-case tracking-normal text-foreground outline-none focus:border-primary/50"
          />
        </label>
        <label className="block text-[10px] uppercase tracking-wider text-muted-foreground-soft">
          Target date
          <input
            type="date"
            aria-label="Target date"
            defaultValue={project.target_date?.split("T")[0] ?? ""}
            onChange={(e) => void edit.patch({ target_date: e.target.value || "" })}
            className="mt-1 h-7 w-full rounded border border-border/60 bg-card px-2 text-[12px] normal-case tracking-normal text-foreground outline-none focus:border-primary/50"
          />
        </label>
      </PopoverContent>
    </Popover>
  )
}

/** The icon + colour swatch, straight from the rail's header. */
export function ProjectIconEditor({
  project,
  edit,
}: {
  project: Project
  edit: ProjectCardEdit
}) {
  return (
    <CrewIconPopover
      icon={project.icon || "folder"}
      color={project.color || "blue"}
      size="sm"
      onIconChange={(icon) => void edit.patch({ icon })}
      onColorChange={(color) => void edit.patch({ color })}
    />
  )
}

export function ProjectNameEditor({
  name,
  onSave,
}: {
  name: string
  onSave: (next: string) => void
}) {
  const [editing, setEditing] = React.useState(false)
  const [draft, setDraft] = React.useState(name)

  if (editing) {
    return (
      <input
        autoFocus
        aria-label="Project name"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={() => setEditing(false)}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault()
            const next = draft.trim()
            if (next && next !== name) onSave(next)
            setEditing(false)
          }
          if (e.key === "Escape") {
            e.preventDefault()
            setDraft(name)
            setEditing(false)
          }
        }}
        className="w-full rounded-md border border-border bg-card px-2 py-1 text-lg font-semibold tracking-tight outline-none focus:border-primary/50"
      />
    )
  }

  return (
    <span className="group/name flex min-w-0 items-center gap-1.5">
      <h1 className="truncate text-lg font-semibold tracking-tight">{name}</h1>
      <button
        type="button"
        aria-label="Edit project name"
        onClick={() => {
          setDraft(name)
          setEditing(true)
        }}
        className="shrink-0 rounded p-1 text-muted-foreground-soft opacity-0 transition-opacity hover:text-foreground focus-visible:opacity-100 group-hover/name:opacity-100"
      >
        <svg className="h-3.5 w-3.5" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
          <path d="M11.5 2.5a1.4 1.4 0 0 1 2 2L6 12l-3 1 1-3z" />
        </svg>
      </button>
    </span>
  )
}
