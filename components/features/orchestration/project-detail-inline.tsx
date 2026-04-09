"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { ScrollArea } from "@/components/ui/scroll-area"
import { X, User, FolderKanban, CalendarIcon } from "lucide-react"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { CrewIconPopover } from "@/components/crew-icon-popover"
import { SectionHeader, PropertyRow } from "@/components/features/issues/property-row"
import { PROJECT_STATUSES, HEALTH_OPTIONS, PRIORITY_OPTIONS } from "@/components/features/issues/issue-constants"
import { cn } from "@/lib/utils"
import { ISSUE_STATUS_COLORS, CREW_COLOR_DEFAULT } from "@/lib/colors"
import { toast } from "sonner"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import type { Project, ProjectStatus, IssuePriority } from "@/lib/types/mission"

/* -------------------------------------------------------------------------- */
/*  ProjectStatusIcon                                                         */
/* -------------------------------------------------------------------------- */

function ProjectStatusIcon({ status, className }: { status: ProjectStatus; className?: string }) {
  switch (status) {
    case "backlog":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" strokeDasharray="3 3" opacity="0.5" />
        </svg>
      )
    case "planned":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" opacity="0.6" />
        </svg>
      )
    case "in_progress":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" opacity="0.3" />
          <path d="M8 2a6 6 0 0 1 6 6" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
        </svg>
      )
    case "paused":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" opacity="0.4" />
          <rect x="6" y="5" width="1.5" height="6" rx="0.5" fill="currentColor" opacity="0.6" />
          <rect x="8.5" y="5" width="1.5" height="6" rx="0.5" fill="currentColor" opacity="0.6" />
        </svg>
      )
    case "completed":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" fill="currentColor" opacity="0.15" stroke="currentColor" strokeWidth="1.5" />
          <path d="M5.5 8l2 2 3.5-3.5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      )
    case "cancelled":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" opacity="0.3" />
          <path d="M5.5 5.5l5 5M10.5 5.5l-5 5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
        </svg>
      )
    default:
      return null
  }
}

/* -------------------------------------------------------------------------- */
/*  HealthBadge                                                               */
/* -------------------------------------------------------------------------- */

function HealthBadge({ health }: { health: string }) {
  switch (health) {
    case "at_risk":
      return <span className="text-[11px] text-amber-400">At risk</span>
    case "off_track":
      return <span className="text-[11px] text-red-400">Off track</span>
    default:
      return <span className="text-[11px] text-muted-foreground/40">No updates</span>
  }
}

/* -------------------------------------------------------------------------- */
/*  ProjectsListView — Linear-style projects table for center panel           */
/* -------------------------------------------------------------------------- */

interface ProjectsListViewProps {
  projects: Project[]
  onRefresh: () => void
  workspaceId: string
  onProjectClick?: (projectId: string) => void
}

export function ProjectsListView({ projects, onRefresh: _onRefresh, workspaceId: _workspaceId, onProjectClick }: ProjectsListViewProps) {
  const sorted = useMemo(
    () => [...projects].sort((a, b) => a.name.localeCompare(b.name)),
    [projects],
  )

  if (projects.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-muted-foreground/50">
        <FolderKanban className="h-6 w-6 mb-2" />
        <p className="text-[12px]">No projects yet</p>
        <p className="text-[10px] text-muted-foreground/30 mt-1">Projects will appear here once created</p>
      </div>
    )
  }

  return (
    <div>
      <table className="w-full text-[12px]">
        <thead>
          <tr className="text-left text-muted-foreground/60 border-b border-white/[0.06]">
            <th className="py-2 px-3 font-medium">Name</th>
            <th className="py-2 px-3 font-medium w-24">Health</th>
            <th className="py-2 px-3 font-medium w-20">Priority</th>
            <th className="py-2 px-3 font-medium w-28">Lead</th>
            <th className="py-2 px-3 font-medium w-28">Target date</th>
            <th className="py-2 px-3 font-medium w-32">Status</th>
          </tr>
        </thead>
        <tbody>
          {sorted.map((p) => (
            <tr key={p.id} className="border-b border-white/[0.04] hover:bg-white/[0.02] transition-colors cursor-pointer" onClick={() => onProjectClick?.(p.id)}>
              {/* Name */}
              <td className="py-2 px-3">
                <div className="flex items-center gap-2">
                  <div className="w-3 h-3 rounded-sm shrink-0 flex items-center justify-center" style={{ backgroundColor: p.color }}>
                    {p.icon ? (
                      <span className="text-[8px] text-white font-bold">{p.icon.charAt(0).toUpperCase()}</span>
                    ) : null}
                  </div>
                  <span className="text-foreground/90 font-medium truncate">{p.name}</span>
                </div>
              </td>
              {/* Health */}
              <td className="py-2 px-3">
                <HealthBadge health={p.health} />
              </td>
              {/* Priority */}
              <td className="py-2 px-3">
                <div className="flex items-center gap-1.5">
                  <PriorityIcon priority={p.priority || "none"} className="h-3.5 w-3.5" />
                  <span className="text-foreground/60 capitalize">{p.priority || "None"}</span>
                </div>
              </td>
              {/* Lead */}
              <td className="py-2 px-3">
                {p.lead_name ? (
                  <div className="flex items-center gap-1.5">
                    <div className="w-4 h-4 rounded-full bg-white/[0.08] flex items-center justify-center shrink-0">
                      <span className="text-[8px] font-semibold text-muted-foreground/60">
                        {p.lead_name.charAt(0).toUpperCase()}
                      </span>
                    </div>
                    <span className="text-foreground/60 truncate">{p.lead_name}</span>
                  </div>
                ) : (
                  <span className="text-muted-foreground/30">&mdash;</span>
                )}
              </td>
              {/* Target date */}
              <td className="py-2 px-3">
                {p.target_date ? (
                  <span className="text-foreground/60">{new Date(p.target_date).toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" })}</span>
                ) : (
                  <span className="text-muted-foreground/30">&mdash;</span>
                )}
              </td>
              {/* Status / Progress */}
              <td className="py-2 px-3">
                <div className="flex items-center gap-2">
                  <ProjectStatusIcon status={p.status} className="h-3.5 w-3.5 text-muted-foreground/70 shrink-0" />
                  <span className="text-foreground/60 tabular-nums w-8 text-right">{p.progress}%</span>
                  <div className="w-12 h-1.5 bg-white/[0.06] rounded-full overflow-hidden">
                    <div
                      className={cn(
                        "h-full rounded-full transition-all",
                        p.progress >= 100 ? "bg-green-500/70" : "bg-blue-500/60",
                      )}
                      style={{ width: `${Math.min(p.progress, 100)}%` }}
                    />
                  </div>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}


/*  ProjectDetailInline — Right panel for editing project properties          */
/* ═══════════════════════════════════════════════════════════════════════════ */

interface ProjectDetailInlineProps {
  project: Project
  workspaceId: string
  onClose: () => void
  onUpdated: () => void
}

interface ProjectStats {
  total_issues: number
  completed_issues: number
  by_status: Record<string, number>
  by_assignee: { agent_id: string; agent_name: string; total: number; completed: number }[]
  by_label: { label_name: string; color: string; count: number }[]
  crews: string[]
}


export function ProjectDetailInline({ project, workspaceId, onClose, onUpdated }: ProjectDetailInlineProps) {
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState(project.name)
  const [stats, setStats] = useState<ProjectStats | null>(null)
  const [allAgents, setAllAgents] = useState<{ id: string; name: string; slug: string }[]>([])

  // Section collapse state
  const [propertiesOpen, setPropertiesOpen] = useState(true)
  const [milestonesOpen, setMilestonesOpen] = useState(false)
  const [progressOpen, setProgressOpen] = useState(true)

  // Popover state
  const [leadOpen, setLeadOpen] = useState(false)

  // Progress breakdown tab
  const [progressTab, setProgressTab] = useState<"assignees" | "labels">("assignees")

  // Fetch stats + agents
  useEffect(() => {
    fetch(`/api/v1/projects/${project.id}/stats?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : null))
      .then(setStats)
      .catch(() => {})
    fetch(`/api/v1/agents?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((agents: { id: string; name: string; slug: string }[]) =>
        setAllAgents(agents.map((a) => ({ id: a.id, name: a.name, slug: a.slug }))),
      )
      .catch(() => {})
  }, [project.id, workspaceId])

  const patchProject = useCallback(
    async (fields: Record<string, unknown>) => {
      const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`
      const res = await fetch(`/api/v1/projects/${project.id}${qs}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(fields),
      })
      if (res.ok) {
        toast.success("Project updated")
        onUpdated()
      } else {
        const err = await res.json().catch(() => null)
        toast.error(err?.detail || "Failed to update project")
      }
    },
    [project.id, workspaceId, onUpdated],
  )

  // Donut chart for status breakdown
  const donutSegments = useMemo(() => {
    if (!stats?.by_status) return []
    const entries = Object.entries(stats.by_status).filter(([, v]) => v > 0)
    const total = entries.reduce((sum, [, v]) => sum + v, 0)
    if (total === 0) return []
    const segments: { status: string; value: number; pct: number; color: string }[] = []
    entries.forEach(([status, value]) => {
      segments.push({
        status,
        value,
        pct: (value / total) * 100,
        color: ISSUE_STATUS_COLORS[status] || CREW_COLOR_DEFAULT,
      })
    })
    return segments
  }, [stats?.by_status])

  const donutPaths = useMemo(() => {
    if (donutSegments.length === 0) return []
    const radius = 16
    const circumference = 2 * Math.PI * radius
    let offset = 0
    return donutSegments.map((seg) => {
      const dashLen = (seg.pct / 100) * circumference
      const path = {
        ...seg,
        dasharray: `${dashLen} ${circumference - dashLen}`,
        dashoffset: -offset,
      }
      offset += dashLen
      return path
    })
  }, [donutSegments])

  return (
    <div className="h-full flex flex-col border-l border-white/[0.06] bg-card overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-white/[0.06] shrink-0">
        <div className="flex items-center gap-2">
          <CrewIconPopover
            icon={project.icon || "folder"}
            color={project.color || "blue"}
            size="sm"
            onIconChange={(icon) => patchProject({ icon })}
            onColorChange={(color) => patchProject({ color })}
          />
          <span className="text-[11px] font-mono text-muted-foreground/60">Project</span>
        </div>
        <div className="flex items-center gap-1">
          <a
            href={`/orchestration/projects/${project.id}`}
            className="text-muted-foreground/40 hover:text-foreground p-1 rounded hover:bg-white/[0.06] transition-colors"
            title="Open full page"
          >
            <svg className="h-3.5 w-3.5" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="M6 2H3a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1v-3" />
              <path d="M10 2h4v4" />
              <path d="M14 2L7 9" />
            </svg>
          </a>
          <button
            onClick={onClose}
            className="p-1 rounded hover:bg-white/[0.06] text-muted-foreground/50 hover:text-muted-foreground transition-colors"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>

      <ScrollArea className="flex-1">
        <div className="py-3 space-y-1">
          {/* Title */}
          <div className="px-4">
          {editingTitle ? (
            <input
              className="text-[15px] font-semibold text-foreground bg-transparent border-b border-blue-500 outline-none w-full pb-1"
              value={titleDraft}
              onChange={(e) => setTitleDraft(e.target.value)}
              onBlur={() => {
                if (titleDraft.trim() && titleDraft !== project.name) patchProject({ name: titleDraft.trim() })
                setEditingTitle(false)
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter") (e.target as HTMLInputElement).blur()
                if (e.key === "Escape") {
                  setTitleDraft(project.name)
                  setEditingTitle(false)
                }
              }}
              autoFocus
            />
          ) : (
            <h2
              className="text-[15px] font-semibold text-foreground cursor-pointer hover:text-blue-400 transition-colors"
              onClick={() => {
                setTitleDraft(project.name)
                setEditingTitle(true)
              }}
            >
              {project.name}
            </h2>
          )}

          {project.description && (
            <p className="text-[12px] text-muted-foreground/70 leading-relaxed">{project.description}</p>
          )}
          </div>

          {/* ── Properties ─────────────────────────────────────────── */}
          <div className="mt-2 mx-2 rounded-lg border border-white/[0.04] bg-[#18171D]">
          <SectionHeader title="Properties" open={propertiesOpen} onToggle={() => setPropertiesOpen((v) => !v)} />
          {propertiesOpen && (
            <div className="px-1 pb-1">
              {/* Status */}
              <Popover>
                <PopoverTrigger asChild>
                  <div>
                    <PropertyRow label="Status">
                      <ProjectStatusIcon status={project.status} className="h-3.5 w-3.5 shrink-0" />
                      {PROJECT_STATUSES.find((s) => s.value === project.status)?.label || project.status}
                    </PropertyRow>
                  </div>
                </PopoverTrigger>
                <PopoverContent className="w-48 p-1" align="start">
                  {PROJECT_STATUSES.map((s) => (
                    <button
                      key={s.value}
                      onClick={() => patchProject({ status: s.value })}
                      className={cn(
                        "flex items-center gap-2 w-full px-2 py-1.5 rounded text-xs hover:bg-white/[0.06]",
                        s.value === project.status && "bg-white/[0.04]",
                      )}
                    >
                      <ProjectStatusIcon status={s.value as ProjectStatus} className="h-3.5 w-3.5 shrink-0" />
                      {s.label}
                    </button>
                  ))}
                </PopoverContent>
              </Popover>

              {/* Priority */}
              <Popover>
                <PopoverTrigger asChild>
                  <div>
                    <PropertyRow label="Priority">
                      <PriorityIcon priority={project.priority || "none"} className="h-3.5 w-3.5" />
                      {priorityLabel[project.priority || "none"]}
                    </PropertyRow>
                  </div>
                </PopoverTrigger>
                <PopoverContent className="w-48 p-1" align="start">
                  {PRIORITY_OPTIONS.map((p) => (
                    <button
                      key={p.value}
                      onClick={() => patchProject({ priority: p.value })}
                      className={cn(
                        "flex items-center gap-2 w-full px-2 py-1.5 rounded text-xs hover:bg-white/[0.06]",
                        p.value === project.priority && "bg-white/[0.04]",
                      )}
                    >
                      <PriorityIcon priority={p.value as IssuePriority} className="h-3.5 w-3.5" />
                      {p.label}
                    </button>
                  ))}
                </PopoverContent>
              </Popover>

              {/* Health */}
              <Popover>
                <PopoverTrigger asChild>
                  <div>
                    <PropertyRow label="Health">
                      <span className={cn("font-medium", HEALTH_OPTIONS.find((h) => h.value === project.health)?.color || "text-muted-foreground")}>
                        {HEALTH_OPTIONS.find((h) => h.value === project.health)?.label || project.health}
                      </span>
                    </PropertyRow>
                  </div>
                </PopoverTrigger>
                <PopoverContent className="w-48 p-1" align="start">
                  {HEALTH_OPTIONS.map((h) => (
                    <button
                      key={h.value}
                      onClick={() => patchProject({ health: h.value })}
                      className={cn(
                        "flex items-center gap-2 w-full px-2 py-1.5 rounded text-xs hover:bg-white/[0.06]",
                        h.value === project.health && "bg-white/[0.04]",
                      )}
                    >
                      <span className={h.color}>{h.label}</span>
                    </button>
                  ))}
                </PopoverContent>
              </Popover>

              {/* Lead */}
              <Popover open={leadOpen} onOpenChange={setLeadOpen}>
                <PopoverTrigger asChild>
                  <div>
                    <PropertyRow label="Lead">
                      {project.lead_id && <img src={getAgentAvatarUrl(project.lead_id)} alt="" className="h-4 w-4 rounded-full" />}
                      {project.lead_name || <span className="text-muted-foreground/40">Add lead</span>}
                    </PropertyRow>
                  </div>
                </PopoverTrigger>
                <PopoverContent className="w-52 p-0" align="start">
                  <Command>
                    <CommandInput placeholder="Search agents..." />
                    <CommandList>
                      <CommandEmpty>No agents found</CommandEmpty>
                      <CommandGroup>
                        <CommandItem
                          onSelect={() => {
                            patchProject({ lead_type: null, lead_id: null })
                            setLeadOpen(false)
                          }}
                        >
                          <User className="h-3.5 w-3.5 text-muted-foreground/50 mr-2" />
                          No lead
                        </CommandItem>
                        {allAgents.map((a) => (
                          <CommandItem
                            key={a.id}
                            onSelect={() => {
                              patchProject({ lead_type: "agent", lead_id: a.id })
                              setLeadOpen(false)
                            }}
                          >
                            <img src={getAgentAvatarUrl(a.id)} alt="" className="h-4 w-4 rounded-full mr-2" />
                            {a.name}
                          </CommandItem>
                        ))}
                      </CommandGroup>
                    </CommandList>
                  </Command>
                </PopoverContent>
              </Popover>

              {/* Members */}
              {stats?.by_assignee && stats.by_assignee.length > 0 && (
                <PropertyRow label="Members" className="cursor-default">
                  <div className="flex -space-x-1">
                    {stats.by_assignee.slice(0, 5).map((a) => (
                      <img key={a.agent_id} src={getAgentAvatarUrl(a.agent_id || a.agent_name)} alt={a.agent_name} title={a.agent_name} className="h-4 w-4 rounded-full ring-1 ring-card" />
                    ))}
                    {stats.by_assignee.length > 5 && (
                      <span className="text-[10px] text-muted-foreground/50 pl-1">+{stats.by_assignee.length - 5}</span>
                    )}
                  </div>
                </PropertyRow>
              )}

              {/* Dates */}
              <PropertyRow label="Dates">
                <span className="flex items-center gap-1.5 text-muted-foreground/60">
                  <CalendarIcon className="h-3 w-3" />
                  {project.start_date || project.target_date
                    ? `${project.start_date ? new Date(project.start_date).toLocaleDateString(undefined, { month: "short", day: "numeric" }) : "?"} → ${project.target_date ? new Date(project.target_date).toLocaleDateString(undefined, { month: "short", day: "numeric" }) : "?"}`
                    : "Set dates"}
                </span>
              </PropertyRow>

              {/* Teams */}
              {stats?.crews && stats.crews.length > 0 && (
                <PropertyRow label="Teams" className="cursor-default">
                  <div className="flex items-center gap-1 flex-wrap justify-end">
                    {stats.crews.map((crew) => (
                      <span key={crew} className="text-[9px] px-1.5 py-0.5 rounded bg-white/[0.04] text-muted-foreground/50">{crew}</span>
                    ))}
                  </div>
                </PropertyRow>
              )}
            </div>
          )}
          </div>

          {/* ── Labels ─────────────────────────────────────────────── */}
          <div className="mt-1 mx-2 rounded-lg border border-white/[0.04] bg-[#18171D]">
            <SectionHeader title="Labels" open={true} onToggle={() => {}} />
            <div className="px-3 pb-2">
              {stats?.by_label && stats.by_label.length > 0 ? (
                <div className="flex items-center gap-1 flex-wrap">
                  {stats.by_label.map((l) => (
                    <span key={l.label_name} className="text-[9px] px-1.5 py-0.5 rounded-full flex items-center gap-1" style={{ backgroundColor: `${l.color}18`, color: l.color }}>
                      <span className="w-1.5 h-1.5 rounded-full" style={{ backgroundColor: l.color }} />
                      {l.label_name}
                    </span>
                  ))}
                </div>
              ) : (
                <span className="text-[11px] text-muted-foreground/40 pl-0.5">No labels</span>
              )}
            </div>
          </div>

          {/* ── Milestones ─────────────────────────────────────────── */}
          <div className="mt-1 mx-2 rounded-lg border border-white/[0.04] bg-[#18171D]">
            <SectionHeader title="Milestones" open={milestonesOpen} onToggle={() => setMilestonesOpen((v) => !v)} />
            {milestonesOpen && (
              <div className="px-3 pb-2">
                <p className="text-[12px] text-muted-foreground/50">No milestones yet</p>
              </div>
            )}
          </div>

          {/* ── Progress ─────────────────────────────────────────── */}
          <div className="mt-1 mx-2 rounded-lg border border-white/[0.04] bg-[#18171D]">
          <SectionHeader title="Progress" open={progressOpen} onToggle={() => setProgressOpen((v) => !v)} />
          {progressOpen && (
            <div className="space-y-3 px-3 pb-3">
              {/* Stat boxes */}
              <div className="grid grid-cols-2 gap-2">
                <div className="bg-white/[0.03] border border-white/[0.06] rounded-md px-3 py-2">
                  <div className="text-[10px] text-muted-foreground/50 uppercase tracking-wider">Scope</div>
                  <div className="text-[18px] font-semibold text-foreground tabular-nums">
                    {stats?.total_issues ?? project.issue_count}
                  </div>
                </div>
                <div className="bg-white/[0.03] border border-white/[0.06] rounded-md px-3 py-2">
                  <div className="text-[10px] text-muted-foreground/50 uppercase tracking-wider">Completed</div>
                  <div className="text-[18px] font-semibold text-green-400 tabular-nums">
                    {stats?.completed_issues ?? project.done_count}
                  </div>
                </div>
              </div>

              {/* Donut chart */}
              {donutPaths.length > 0 && (
                <div className="flex items-center gap-4">
                  <svg viewBox="0 0 40 40" className="w-12 h-12 shrink-0">
                    {donutPaths.map((seg) => (
                      <circle
                        key={seg.status}
                        cx="20"
                        cy="20"
                        r="16"
                        fill="none"
                        stroke={seg.color}
                        strokeWidth="5"
                        strokeDasharray={seg.dasharray}
                        strokeDashoffset={seg.dashoffset}
                        transform="rotate(-90 20 20)"
                        className="transition-all duration-300"
                      />
                    ))}
                  </svg>
                  <div className="space-y-0.5 flex-1 min-w-0">
                    {donutSegments.map((seg) => (
                      <div key={seg.status} className="flex items-center gap-1.5">
                        <span className="w-2 h-2 rounded-sm shrink-0" style={{ backgroundColor: seg.color }} />
                        <span className="text-[10px] text-muted-foreground/70 truncate flex-1">
                          {seg.status.replace(/_/g, " ")}
                        </span>
                        <span className="text-[10px] text-muted-foreground/50 tabular-nums">{seg.value}</span>
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* Tabs */}
              <div className="flex items-center gap-0 border-b border-white/[0.06]">
                <button
                  onClick={() => setProgressTab("assignees")}
                  className={cn(
                    "text-[11px] px-2 py-1.5 border-b-2 transition-colors",
                    progressTab === "assignees"
                      ? "border-blue-500 text-foreground"
                      : "border-transparent text-muted-foreground/50 hover:text-muted-foreground/70",
                  )}
                >
                  Assignees
                </button>
                <button
                  onClick={() => setProgressTab("labels")}
                  className={cn(
                    "text-[11px] px-2 py-1.5 border-b-2 transition-colors",
                    progressTab === "labels"
                      ? "border-blue-500 text-foreground"
                      : "border-transparent text-muted-foreground/50 hover:text-muted-foreground/70",
                  )}
                >
                  Labels
                </button>
              </div>

              {/* Assignees tab */}
              {progressTab === "assignees" && (
                <div className="space-y-2">
                  {stats?.by_assignee && stats.by_assignee.length > 0 ? (
                    stats.by_assignee.map((a) => (
                      <div key={a.agent_id || a.agent_name} className="flex items-center gap-2">
                        <img
                          src={getAgentAvatarUrl(a.agent_id || a.agent_name)}
                          alt=""
                          className="h-5 w-5 rounded-full"
                        />
                        <span className="text-[12px] text-foreground/80 flex-1 truncate">{a.agent_name}</span>
                        <span className="text-[11px] text-muted-foreground/50 tabular-nums">
                          {a.completed} of {a.total}
                        </span>
                        <div className="w-8 h-1.5 bg-white/[0.06] rounded-full overflow-hidden">
                          <div
                            className="h-full bg-blue-500/70 rounded-full"
                            style={{ width: `${a.total > 0 ? (a.completed / a.total) * 100 : 0}%` }}
                          />
                        </div>
                      </div>
                    ))
                  ) : (
                    <p className="text-[11px] text-muted-foreground/40">No assignees yet</p>
                  )}
                </div>
              )}

              {/* Labels tab */}
              {progressTab === "labels" && (
                <div className="space-y-2">
                  {stats?.by_label && stats.by_label.length > 0 ? (
                    stats.by_label.map((l) => (
                      <div key={l.label_name} className="flex items-center gap-2">
                        <span className="w-2.5 h-2.5 rounded-full shrink-0" style={{ backgroundColor: l.color }} />
                        <span className="text-[12px] text-foreground/80 flex-1 truncate">{l.label_name}</span>
                        <span className="text-[11px] text-muted-foreground/50 tabular-nums">{l.count}</span>
                      </div>
                    ))
                  ) : (
                    <p className="text-[11px] text-muted-foreground/40">No labels yet</p>
                  )}
                </div>
              )}
            </div>
          )}
          </div>

          {/* ── Activity ─────────────────────────────────────────── */}
          <div className="mt-1 mx-2 rounded-lg border border-white/[0.04] bg-[#18171D] px-3 py-2 space-y-1">
            <div className="text-[10px] text-muted-foreground/40 font-mono">
              Created: {new Date(project.created_at).toLocaleDateString()}
            </div>
            <div className="text-[10px] text-muted-foreground/40 font-mono">ID: {project.id}</div>
          </div>
        </div>
      </ScrollArea>
    </div>
  )
}
