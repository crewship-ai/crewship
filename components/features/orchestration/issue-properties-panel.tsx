"use client"

import { useEffect, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { Flag, CalendarIcon } from "lucide-react"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { Calendar } from "@/components/ui/calendar"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { SectionHeader, PropertyRow } from "@/components/features/issues/property-row"
import { ISSUE_STATUSES, ALL_PRIORITIES } from "@/components/features/issues/issue-constants"
import type { Mission, IssuePriority, Milestone } from "@/lib/types/mission"

interface IssuePropertiesPanelProps {
  issue: Mission
  workspaceId: string
  patchIssue: (patch: Record<string, unknown>) => Promise<void>
}

export function IssuePropertiesPanel({ issue, workspaceId, patchIssue }: IssuePropertiesPanelProps) {
  const [propertiesOpen, setPropertiesOpen] = useState(true)

  // Popover open state
  const [statusOpen, setStatusOpen] = useState(false)
  const [priorityOpen, setPriorityOpen] = useState(false)
  const [assigneeOpen, setAssigneeOpen] = useState(false)
  const [crewAgents, setCrewAgents] = useState<{id: string, name: string, slug: string, crew_slug?: string}[]>([])
  const [dueDateOpen, setDueDateOpen] = useState(false)

  // Milestones
  const [milestones, setMilestones] = useState<Milestone[]>([])
  const currentMilestone = milestones.find((m) => m.id === issue.milestone_id)

  // Fetch milestones for the project
  useEffect(() => {
    if (!issue.project_id || !workspaceId) return
    fetch(`/api/v1/projects/${issue.project_id}/milestones?workspace_id=${workspaceId}`)
      .then((r) => r.ok ? r.json() : [])
      .then((data) => setMilestones(Array.isArray(data) ? data : data.milestones ?? []))
      .catch(() => {})
  }, [issue.project_id, workspaceId])

  // Fetch agents for assignee picker — crew-scoped when available, all agents otherwise
  useEffect(() => {
    if (!workspaceId) return
    const url = issue.crew_id
      ? `/api/v1/agents?workspace_id=${workspaceId}&crew_id=${issue.crew_id}`
      : `/api/v1/agents?workspace_id=${workspaceId}`
    fetch(url)
      .then(r => r.ok ? r.json() : [])
      .then((agents: Array<{id: string, name: string, slug: string, crew?: {slug: string}}>) =>
        setCrewAgents(agents.map(a => ({ id: a.id, name: a.name, slug: a.slug, crew_slug: a.crew?.slug })))
      )
      .catch(() => {})
  }, [issue.crew_id, workspaceId])

  return (
    <div className="mt-2 mx-2 rounded-lg border border-white/[0.04] bg-[#18171D]">
      <SectionHeader
        title="Properties"
        open={propertiesOpen}
        onToggle={() => setPropertiesOpen((v) => !v)}
      />
      <AnimatePresence initial={false}>
      {propertiesOpen && (
        <motion.div initial={{ height: 0, opacity: 0 }} animate={{ height: "auto", opacity: 1 }} exit={{ height: 0, opacity: 0 }} transition={{ duration: 0.2 }}>
          {/* Status */}
          <Popover open={statusOpen} onOpenChange={setStatusOpen}>
            <PopoverTrigger asChild>
              <div>
                <PropertyRow label="Status">
                  <StatusIcon status={issue.status} className="h-3.5 w-3.5" />
                  {statusLabel[issue.status] || issue.status}
                </PropertyRow>
              </div>
            </PopoverTrigger>
            <PopoverContent className="w-[200px] p-0" align="start" sideOffset={4}>
              <Command>
                <CommandInput placeholder="Set status..." className="text-xs h-8" />
                <CommandList>
                  <CommandEmpty>No status found.</CommandEmpty>
                  <CommandGroup>
                    {ISSUE_STATUSES.map((s) => (
                      <CommandItem
                        key={s}
                        value={s}
                        onSelect={() => {
                          patchIssue({ status: s })
                          setStatusOpen(false)
                        }}
                        className="flex items-center gap-2 text-xs"
                      >
                        <StatusIcon status={s} className="h-3.5 w-3.5" />
                        <span>{statusLabel[s] || s}</span>
                        {s === issue.status && (
                          <span className="ml-auto text-blue-400 text-[10px]">current</span>
                        )}
                      </CommandItem>
                    ))}
                  </CommandGroup>
                </CommandList>
              </Command>
            </PopoverContent>
          </Popover>

          {/* Priority */}
          <Popover open={priorityOpen} onOpenChange={setPriorityOpen}>
            <PopoverTrigger asChild>
              <div>
                <PropertyRow label="Priority">
                  <PriorityIcon priority={(issue.priority || "none") as IssuePriority} className="h-3.5 w-3.5" />
                  {priorityLabel[(issue.priority || "none") as IssuePriority]}
                </PropertyRow>
              </div>
            </PopoverTrigger>
            <PopoverContent className="w-[200px] p-0" align="start" sideOffset={4}>
              <Command>
                <CommandList>
                  <CommandGroup>
                    {ALL_PRIORITIES.map((p) => (
                      <CommandItem
                        key={p}
                        value={p}
                        onSelect={() => {
                          patchIssue({ priority: p })
                          setPriorityOpen(false)
                        }}
                        className="flex items-center gap-2 text-xs"
                      >
                        <PriorityIcon priority={p} className="h-3.5 w-3.5" />
                        <span>{priorityLabel[p]}</span>
                        {p === (issue.priority || "none") && (
                          <span className="ml-auto text-blue-400 text-[10px]">current</span>
                        )}
                      </CommandItem>
                    ))}
                  </CommandGroup>
                </CommandList>
              </Command>
            </PopoverContent>
          </Popover>

          {/* Assignee */}
          <Popover open={assigneeOpen} onOpenChange={setAssigneeOpen}>
            <PopoverTrigger asChild>
              <div>
                <PropertyRow label="Assignee">
                  {issue.assignee_id && (
                    <img src={getAgentAvatarUrl(issue.assignee_id)} alt="" className="h-4 w-4 rounded-full" />
                  )}
                  {issue.assignee_name || <span className="text-foreground/40">Unassigned</span>}
                </PropertyRow>
              </div>
            </PopoverTrigger>
            <PopoverContent className="w-56 p-1" align="start">
              <Command>
                <CommandInput placeholder="Search agents..." className="h-7 text-xs" />
                <CommandList>
                  <CommandEmpty className="text-xs text-center py-2">No agents found</CommandEmpty>
                  <CommandGroup>
                    <CommandItem
                      value="unassigned"
                      className="text-xs"
                      onSelect={() => {
                        patchIssue({ assignee_type: null, assignee_id: null })
                        setAssigneeOpen(false)
                      }}
                    >
                      Unassigned
                    </CommandItem>
                    {crewAgents.map(agent => (
                      <CommandItem
                        key={agent.id}
                        value={`${agent.name} ${agent.slug} ${agent.crew_slug || ""}`}
                        className="text-xs"
                        onSelect={() => {
                          patchIssue({ assignee_type: "agent", assignee_id: agent.id })
                          setAssigneeOpen(false)
                        }}
                      >
                        <span>{agent.name}</span>
                        <span className="text-muted-foreground/40 ml-1">@{agent.slug}</span>
                        {agent.crew_slug && <span className="text-muted-foreground/30 ml-auto text-[10px]">{agent.crew_slug}</span>}
                      </CommandItem>
                    ))}
                  </CommandGroup>
                </CommandList>
              </Command>
            </PopoverContent>
          </Popover>

          {/* Due date */}
          <Popover open={dueDateOpen} onOpenChange={setDueDateOpen}>
            <PopoverTrigger asChild>
              <div>
                <PropertyRow label="Due date">
                  <span className="flex items-center gap-1.5">
                    <CalendarIcon className="h-3 w-3 text-muted-foreground/50" />
                    {issue.due_date ? new Date(issue.due_date).toLocaleDateString() : <span className="text-foreground/40">No due date</span>}
                  </span>
                </PropertyRow>
              </div>
            </PopoverTrigger>
            <PopoverContent className="w-auto p-0" align="start" sideOffset={4}>
              <Calendar
                mode="single"
                selected={issue.due_date ? new Date(issue.due_date) : undefined}
                onSelect={(date) => {
                  if (date) {
                    const yyyy = date.getFullYear()
                    const mm = String(date.getMonth() + 1).padStart(2, "0")
                    const dd = String(date.getDate()).padStart(2, "0")
                    patchIssue({ due_date: `${yyyy}-${mm}-${dd}` })
                  } else {
                    patchIssue({ due_date: null })
                  }
                  setDueDateOpen(false)
                }}
                className="rounded-md"
              />
              {issue.due_date && (
                <div className="border-t border-border px-3 py-2">
                  <button
                    className="text-[11px] text-red-400 hover:underline"
                    onClick={() => { patchIssue({ due_date: null }); setDueDateOpen(false) }}
                  >
                    Remove date
                  </button>
                </div>
              )}
            </PopoverContent>
          </Popover>

          {/* Estimate */}
          <Popover>
            <PopoverTrigger asChild>
              <div>
                <PropertyRow label="Estimate">
                  {issue.estimate ? `${issue.estimate} pts` : <span className="text-foreground/40">&mdash;</span>}
                </PropertyRow>
              </div>
            </PopoverTrigger>
            <PopoverContent className="w-48 p-1" align="start">
              {[1, 2, 3, 5, 8, 13, 21].map((pts) => (
                <button
                  key={pts}
                  onClick={() => patchIssue({ estimate: pts })}
                  className={cn(
                    "w-full px-2 py-1.5 text-xs text-left rounded hover:bg-white/[0.06]",
                    issue.estimate === pts && "bg-blue-500/10 text-blue-400",
                  )}
                >
                  {pts} points
                </button>
              ))}
              <button
                onClick={() => patchIssue({ estimate: null })}
                className="w-full px-2 py-1.5 text-xs text-left rounded hover:bg-white/[0.06] text-muted-foreground/50"
              >
                Clear estimate
              </button>
            </PopoverContent>
          </Popover>

          {/* Milestone */}
          <Popover>
            <PopoverTrigger asChild>
              <div>
                <PropertyRow label="Milestone">
                  {currentMilestone ? currentMilestone.name : <span className="text-foreground/40">&mdash;</span>}
                </PropertyRow>
              </div>
            </PopoverTrigger>
            <PopoverContent className="w-52 p-1" align="start">
              {milestones.length === 0 ? (
                <p className="text-[11px] text-muted-foreground/40 px-2 py-3 text-center">
                  {issue.project_id ? "No milestones in project" : "Set a project first"}
                </p>
              ) : (
                <>
                  {milestones.map((m) => (
                    <button
                      key={m.id}
                      onClick={() => patchIssue({ milestone_id: m.id })}
                      className={cn(
                        "w-full px-2 py-1.5 text-xs text-left rounded hover:bg-white/[0.06] flex items-center gap-2",
                        issue.milestone_id === m.id && "bg-blue-500/10 text-blue-400",
                      )}
                    >
                      <Flag className="h-3 w-3 shrink-0" />
                      <span className="truncate">{m.name}</span>
                      {m.target_date && (
                        <span className="ml-auto text-[10px] text-muted-foreground/40 shrink-0">
                          {new Date(m.target_date).toLocaleDateString(undefined, { month: "short", day: "numeric" })}
                        </span>
                      )}
                    </button>
                  ))}
                  {issue.milestone_id && (
                    <button
                      onClick={() => patchIssue({ milestone_id: null })}
                      className="w-full px-2 py-1.5 text-xs text-left rounded hover:bg-white/[0.06] text-muted-foreground/50"
                    >
                      Clear milestone
                    </button>
                  )}
                </>
              )}
            </PopoverContent>
          </Popover>
        </motion.div>
      )}
      </AnimatePresence>
    </div>
  )
}
