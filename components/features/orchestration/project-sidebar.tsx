"use client"

import { useState } from "react"
import {
  CalendarIcon,
  Clock,
  Plus,
  User,
} from "lucide-react"
import { Calendar } from "@/components/ui/calendar"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { SectionHeader, PropertyRow } from "@/components/features/issues/property-row"
import { ProjectStatusIcon } from "@/components/features/issues/project-status-icon"
import { PROJECT_STATUSES, PRIORITY_OPTIONS } from "@/components/features/issues/issue-constants"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"
import { cn } from "@/lib/utils"
import { ISSUE_STATUS_COLORS } from "@/lib/colors"
import type {
  IssuePriority,
  Milestone,
  Project,
  ProjectStats,
  ProjectStatus,
} from "@/lib/types/mission"

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function formatDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  })
}

function formatShortDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
  })
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface ProjectSidebarProps {
  project: Project
  stats: ProjectStats | null
  agents: { id: string; name: string; slug?: string }[]
  donutPaths: { status: string; value: number; pct: number; color: string; dasharray: string; dashoffset: number }[]
  donutSegments: { status: string; value: number; pct: number; color: string }[]
  progressTab: "assignees" | "labels"
  setProgressTab: (v: "assignees" | "labels") => void
  propertiesOpen: boolean
  setPropertiesOpen: (v: boolean) => void
  milestonesOpen: boolean
  setMilestonesOpen: (v: boolean) => void
  progressOpen: boolean
  setProgressOpen: (v: boolean) => void
  activityOpen: boolean
  setActivityOpen: (v: boolean) => void
  statusOpen: boolean
  setStatusOpen: (v: boolean) => void
  priorityOpen: boolean
  setPriorityOpen: (v: boolean) => void
  leadOpen: boolean
  setLeadOpen: (v: boolean) => void
  patchProject: (fields: Record<string, unknown>) => Promise<void>
  milestones: Milestone[]
  addingMilestone: boolean
  setAddingMilestone: (v: boolean) => void
  newMilestoneName: string
  setNewMilestoneName: (v: string) => void
  newMilestoneDate: string
  setNewMilestoneDate: (v: string) => void
  handleAddMilestone: () => Promise<void>
  editingMilestoneId: string | null
  setEditingMilestoneId: (v: string | null) => void
  editMilestoneName: string
  setEditMilestoneName: (v: string) => void
  handleRenameMilestone: (id: string) => Promise<void>
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function ProjectSidebar({
  project,
  stats,
  agents,
  donutPaths,
  donutSegments,
  progressTab,
  setProgressTab,
  propertiesOpen,
  setPropertiesOpen,
  milestonesOpen,
  setMilestonesOpen,
  progressOpen,
  setProgressOpen,
  activityOpen,
  setActivityOpen,
  statusOpen,
  setStatusOpen,
  priorityOpen,
  setPriorityOpen,
  leadOpen,
  setLeadOpen,
  patchProject,
  milestones,
  addingMilestone,
  setAddingMilestone,
  newMilestoneName,
  setNewMilestoneName,
  newMilestoneDate,
  setNewMilestoneDate,
  handleAddMilestone,
  editingMilestoneId,
  setEditingMilestoneId,
  editMilestoneName,
  setEditMilestoneName,
  handleRenameMilestone,
}: ProjectSidebarProps) {
  const [startDateOpen, setStartDateOpen] = useState(false)
  const [targetDateOpen, setTargetDateOpen] = useState(false)

  return (
    <div className="p-4 space-y-1">
      {/* ── Properties ─────────────────────────────────────────── */}
      <div className="rounded-lg border border-white/[0.04] bg-[#18171D]">
        <SectionHeader
          title="Properties"
          open={propertiesOpen}
          onToggle={() => setPropertiesOpen(!propertiesOpen)}
        />
        {propertiesOpen && (
          <div className="px-3 pb-2 space-y-0.5">
          {/* Status */}
          <Popover open={statusOpen} onOpenChange={setStatusOpen}>
            <PopoverTrigger asChild>
              <div>
                <PropertyRow label="Status">
                  <button className="flex items-center gap-1.5 px-2 py-0.5 rounded hover:bg-white/[0.06] transition-colors">
                    <ProjectStatusIcon status={project.status} className="h-3.5 w-3.5 text-muted-foreground/70" />
                    <span className="text-xs text-foreground/80">
                      {PROJECT_STATUSES.find((s) => s.value === project.status)?.label || project.status}
                    </span>
                  </button>
                </PropertyRow>
              </div>
            </PopoverTrigger>
            <PopoverContent className="w-48 p-1" align="end">
              {PROJECT_STATUSES.map((s) => (
                <button
                  key={s.value}
                  onClick={() => { patchProject({ status: s.value }); setStatusOpen(false) }}
                  className={cn(
                    "flex items-center gap-2 w-full px-2 py-1.5 rounded text-xs hover:bg-white/[0.06]",
                    s.value === project.status && "bg-white/[0.04]",
                  )}
                >
                  <ProjectStatusIcon status={s.value as ProjectStatus} className="h-3.5 w-3.5" />
                  {s.label}
                </button>
              ))}
            </PopoverContent>
          </Popover>

          {/* Priority */}
          <Popover open={priorityOpen} onOpenChange={setPriorityOpen}>
            <PopoverTrigger asChild>
              <div>
                <PropertyRow label="Priority">
                  <button className="flex items-center gap-1.5 px-2 py-0.5 rounded hover:bg-white/[0.06] transition-colors">
                    <PriorityIcon priority={project.priority || "none"} className="h-3.5 w-3.5" />
                    <span className="text-xs text-foreground/80">
                      {priorityLabel[project.priority || "none"]}
                    </span>
                  </button>
                </PropertyRow>
              </div>
            </PopoverTrigger>
            <PopoverContent className="w-48 p-1" align="end">
              {PRIORITY_OPTIONS.map((p) => (
                <button
                  key={p.value}
                  onClick={() => { patchProject({ priority: p.value }); setPriorityOpen(false) }}
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

          {/* Lead */}
          <Popover open={leadOpen} onOpenChange={setLeadOpen}>
            <PopoverTrigger asChild>
              <div>
                <PropertyRow label="Lead">
                  <button className="flex items-center gap-1.5 px-2 py-0.5 rounded hover:bg-white/[0.06] transition-colors">
                    {project.lead_id ? (
                      <>
                        <img src={getAgentAvatarUrl(project.lead_id)} alt="" className="h-4 w-4 rounded-full" />
                        <span className="text-xs text-foreground/80">{project.lead_name || "Lead"}</span>
                      </>
                    ) : (
                      <span className="text-xs text-muted-foreground/40">Add lead</span>
                    )}
                  </button>
                </PropertyRow>
              </div>
            </PopoverTrigger>
            <PopoverContent className="w-52 p-0" align="end">
              <Command>
                <CommandInput placeholder="Search agents..." />
                <CommandList>
                  <CommandEmpty>No agents found</CommandEmpty>
                  <CommandGroup>
                    <CommandItem onSelect={() => { patchProject({ lead_type: null, lead_id: null }); setLeadOpen(false) }}>
                      <User className="h-3.5 w-3.5 text-muted-foreground/50 mr-2" />
                      No lead
                    </CommandItem>
                    {agents.map((a) => (
                      <CommandItem
                        key={a.id}
                        onSelect={() => { patchProject({ lead_type: "agent", lead_id: a.id }); setLeadOpen(false) }}
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
          <PropertyRow label="Members">
            {stats?.by_assignee && stats.by_assignee.length > 0 ? (
              <div className="flex -space-x-1">
                {stats.by_assignee.slice(0, 5).map((a) => (
                  <img
                    key={a.agent_id}
                    src={getAgentAvatarUrl(a.agent_id || a.agent_name)}
                    alt={a.agent_name}
                    title={a.agent_name}
                    className="h-5 w-5 rounded-full ring-1 ring-card"
                  />
                ))}
                {stats.by_assignee.length > 5 && (
                  <span className="text-[10px] text-muted-foreground/50 pl-1.5 self-center">
                    +{stats.by_assignee.length - 5}
                  </span>
                )}
              </div>
            ) : (
              <span className="text-xs text-muted-foreground/40">Add members</span>
            )}
          </PropertyRow>

          {/* Start date */}
          <PropertyRow label="Start">
            <Popover open={startDateOpen} onOpenChange={setStartDateOpen}>
              <PopoverTrigger asChild>
                <button className="flex items-center gap-1 px-2 py-0.5 rounded hover:bg-white/[0.06] transition-colors text-xs text-foreground/70">
                  <CalendarIcon className="h-3 w-3 text-muted-foreground/50" />
                  {project.start_date ? formatShortDate(project.start_date) : <span className="text-muted-foreground/40">Set date</span>}
                </button>
              </PopoverTrigger>
              <PopoverContent className="w-auto p-0" align="end">
                <Calendar
                  mode="single"
                  selected={project.start_date ? new Date(project.start_date) : undefined}
                  onSelect={(date) => {
                    if (date) {
                      const v = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`
                      patchProject({ start_date: v })
                    } else {
                      patchProject({ start_date: null })
                    }
                    setStartDateOpen(false)
                  }}
                  className="rounded-md"
                />
                {project.start_date && (
                  <div className="border-t border-border px-3 py-2">
                    <button className="text-[11px] text-red-400 hover:underline" onClick={() => { patchProject({ start_date: null }); setStartDateOpen(false) }}>
                      Remove date
                    </button>
                  </div>
                )}
              </PopoverContent>
            </Popover>
          </PropertyRow>

          {/* Target date */}
          <PropertyRow label="Target">
            <Popover open={targetDateOpen} onOpenChange={setTargetDateOpen}>
              <PopoverTrigger asChild>
                <button className="flex items-center gap-1 px-2 py-0.5 rounded hover:bg-white/[0.06] transition-colors text-xs text-foreground/70">
                  <CalendarIcon className="h-3 w-3 text-muted-foreground/50" />
                  {project.target_date ? formatShortDate(project.target_date) : <span className="text-muted-foreground/40">Set date</span>}
                </button>
              </PopoverTrigger>
              <PopoverContent className="w-auto p-0" align="end">
                <Calendar
                  mode="single"
                  selected={project.target_date ? new Date(project.target_date) : undefined}
                  onSelect={(date) => {
                    if (date) {
                      const v = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`
                      patchProject({ target_date: v })
                    } else {
                      patchProject({ target_date: null })
                    }
                    setTargetDateOpen(false)
                  }}
                  className="rounded-md"
                />
                {project.target_date && (
                  <div className="border-t border-border px-3 py-2">
                    <button className="text-[11px] text-red-400 hover:underline" onClick={() => { patchProject({ target_date: null }); setTargetDateOpen(false) }}>
                      Remove date
                    </button>
                  </div>
                )}
              </PopoverContent>
            </Popover>
          </PropertyRow>

          {/* Teams */}
          <PropertyRow label="Teams">
            {stats?.crews && stats.crews.length > 0 ? (
              <div className="flex items-center gap-1 flex-wrap justify-end">
                {stats.crews.map((crew) => (
                  <span
                    key={crew}
                    className="text-[10px] px-1.5 py-0.5 rounded bg-white/[0.06] text-muted-foreground/70"
                  >
                    {crew}
                  </span>
                ))}
              </div>
            ) : (
              <span className="text-xs text-muted-foreground/40">No teams</span>
            )}
          </PropertyRow>

          </div>
        )}
      </div>

      {/* ── Labels ─────────────────────────────────────────────── */}
      <div className="rounded-lg border border-white/[0.04] bg-[#18171D]">
        <SectionHeader
          title="Labels"
          open={true}
          onToggle={() => {}}
        />
        <div className="px-3 pb-2">
          {stats?.by_label && stats.by_label.length > 0 ? (
            <div className="flex items-center gap-1 flex-wrap">
              {stats.by_label.map((l) => (
                <span
                  key={l.label_name}
                  className="text-[10px] px-1.5 py-0.5 rounded flex items-center gap-1"
                  style={{ backgroundColor: `${l.color}20`, color: l.color }}
                >
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
      <div className="rounded-lg border border-white/[0.04] bg-[#18171D]">
      <SectionHeader
        title={`Milestones${milestones.length > 0 ? ` (${milestones.length})` : ""}`}
        open={milestonesOpen}
        onToggle={() => setMilestonesOpen(!milestonesOpen)}
        action={
          <button
            onClick={() => { setMilestonesOpen(true); setAddingMilestone(true) }}
            className="p-0.5 rounded hover:bg-white/[0.06] text-muted-foreground/40 hover:text-muted-foreground/60 transition-colors"
          >
            <Plus className="h-3 w-3" />
          </button>
        }
      />
      {milestonesOpen && (
        <div className="px-3 pb-2 space-y-2">
          {milestones.length === 0 && !addingMilestone && (
            <p className="text-[12px] text-muted-foreground/40">No milestones yet</p>
          )}

          {milestones.map((m) => {
            const progress = m.issue_count && m.issue_count > 0
              ? Math.round(((m.done_count ?? 0) / m.issue_count) * 100)
              : 0
            const isEditing = editingMilestoneId === m.id

            return (
              <div
                key={m.id}
                className="group bg-white/[0.02] border border-white/[0.06] rounded-md px-3 py-2"
              >
                {isEditing ? (
                  <input
                    autoFocus
                    value={editMilestoneName}
                    onChange={(e) => setEditMilestoneName(e.target.value)}
                    onBlur={() => handleRenameMilestone(m.id)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") handleRenameMilestone(m.id)
                      if (e.key === "Escape") setEditingMilestoneId(null)
                    }}
                    className="bg-transparent text-[12px] text-foreground/80 font-medium outline-none w-full border-b border-blue-400/40 pb-0.5"
                  />
                ) : (
                  <button
                    onClick={() => {
                      setEditingMilestoneId(m.id)
                      setEditMilestoneName(m.name)
                    }}
                    className="text-[12px] text-foreground/80 font-medium hover:text-foreground transition-colors text-left w-full"
                  >
                    {m.name}
                  </button>
                )}
                <div className="flex items-center justify-between mt-1.5">
                  {m.target_date && (
                    <span className="text-[10px] text-muted-foreground/50">
                      <Clock className="h-2.5 w-2.5 inline mr-0.5" />
                      {new Date(m.target_date).toLocaleDateString(undefined, { month: "short", day: "numeric" })}
                    </span>
                  )}
                  <span className="text-[10px] text-muted-foreground/50 ml-auto">
                    {m.done_count ?? 0}/{m.issue_count ?? 0} done
                  </span>
                </div>
                {(m.issue_count ?? 0) > 0 && (
                  <div className="mt-1.5 h-1 bg-white/[0.06] rounded-full overflow-hidden">
                    <div
                      className="h-full bg-green-500/70 rounded-full transition-all"
                      style={{ width: `${progress}%` }}
                    />
                  </div>
                )}
              </div>
            )
          })}

          {addingMilestone && (
            <div className="bg-white/[0.02] border border-white/[0.08] rounded-md p-2.5 space-y-2">
              <input
                autoFocus
                placeholder="Milestone name"
                value={newMilestoneName}
                onChange={(e) => setNewMilestoneName(e.target.value)}
                onKeyDown={(e) => { if (e.key === "Enter") handleAddMilestone() }}
                className="w-full bg-transparent border border-white/[0.1] rounded px-2 py-1 text-[11px] text-foreground placeholder:text-muted-foreground/30 outline-none focus:border-blue-400/40"
              />
              <input
                type="date"
                value={newMilestoneDate}
                onChange={(e) => setNewMilestoneDate(e.target.value)}
                className="w-full bg-transparent border border-white/[0.1] rounded px-2 py-1 text-[11px] text-foreground outline-none focus:border-blue-400/40"
              />
              <div className="flex gap-1.5">
                <button
                  onClick={handleAddMilestone}
                  disabled={!newMilestoneName.trim()}
                  className={cn(
                    "flex-1 h-6 rounded text-[11px] font-medium transition-colors",
                    newMilestoneName.trim()
                      ? "bg-blue-600 text-white hover:bg-blue-500"
                      : "bg-white/[0.04] text-muted-foreground/30 cursor-not-allowed",
                  )}
                >
                  Add
                </button>
                <button
                  onClick={() => { setAddingMilestone(false); setNewMilestoneName(""); setNewMilestoneDate("") }}
                  className="flex-1 h-6 rounded text-[11px] bg-white/[0.04] text-muted-foreground/60 hover:bg-white/[0.08] transition-colors"
                >
                  Cancel
                </button>
              </div>
            </div>
          )}
        </div>
      )}
      </div>

      {/* ── Progress ─────────────────────────────────────────── */}
      <div className="rounded-lg border border-white/[0.04] bg-[#18171D]">
        <SectionHeader
          title="Progress"
          open={progressOpen}
          onToggle={() => setProgressOpen(!progressOpen)}
        />
        {progressOpen && (
          <div className="px-3 pb-3 space-y-3">
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
              <svg viewBox="0 0 40 40" className="w-14 h-14 shrink-0">
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
                stats.by_assignee.map((a) => {
                  const pct = a.total > 0 ? Math.round((a.completed / a.total) * 100) : 0
                  return (
                    <div key={a.agent_id || a.agent_name} className="flex items-center gap-2">
                      <img
                        src={getAgentAvatarUrl(a.agent_id || a.agent_name)}
                        alt=""
                        className="h-5 w-5 rounded-full shrink-0"
                      />
                      <span className="text-[12px] text-foreground/80 flex-1 truncate">{a.agent_name}</span>
                      <span className="text-[10px] text-muted-foreground/50 tabular-nums shrink-0">
                        {pct}% of {a.total}
                      </span>
                      {/* Mini progress ring */}
                      <svg viewBox="0 0 20 20" className="w-4 h-4 shrink-0">
                        <circle cx="10" cy="10" r="8" fill="none" stroke="currentColor" strokeWidth="2" className="text-white/[0.06]" />
                        <circle
                          cx="10"
                          cy="10"
                          r="8"
                          fill="none"
                          stroke={ISSUE_STATUS_COLORS.IN_PROGRESS}
                          strokeWidth="2"
                          strokeDasharray={`${(pct / 100) * 2 * Math.PI * 8} ${2 * Math.PI * 8}`}
                          strokeDashoffset={0}
                          transform="rotate(-90 10 10)"
                          strokeLinecap="round"
                          className="transition-all duration-300"
                        />
                      </svg>
                    </div>
                  )
                })
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
      <div className="rounded-lg border border-white/[0.04] bg-[#18171D]">
        <SectionHeader
          title="Activity"
          open={activityOpen}
          onToggle={() => setActivityOpen(!activityOpen)}
          action={
            <button className="text-[10px] text-muted-foreground/40 hover:text-muted-foreground/60 transition-colors">
              See all
            </button>
          }
        />
        {activityOpen && (
          <div className="px-3 pb-3 space-y-2">
          <div className="flex items-start gap-2">
            <div className="w-4 h-4 rounded-full bg-white/[0.06] flex items-center justify-center shrink-0 mt-0.5">
              <svg className="h-2.5 w-2.5 text-muted-foreground/50" viewBox="0 0 16 16" fill="currentColor">
                <polygon points="8,2 10,6 14,7 11,10 12,14 8,12 4,14 5,10 2,7 6,6" />
              </svg>
            </div>
            <div className="min-w-0">
              <p className="text-[11px] text-muted-foreground/60">
                Created
                <span className="text-muted-foreground/40 ml-1.5">
                  {formatDate(project.created_at)}
                </span>
              </p>
            </div>
          </div>
        </div>
        )}
      </div>
    </div>
  )
}
