"use client"

import { useState } from "react"
import { Check, ChevronDown, Clock, Lock, Pause } from "lucide-react"
import { cn } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import type { Mission, MissionTask, MissionTaskStatus } from "@/lib/types/mission"
import { type MissionPhase, groupTasksByStatusBucket } from "./mission-modes"

interface MissionSpecModeProps {
  mission: Mission
  phases: MissionPhase[]
}

/**
 * MissionSpecMode renders the wireframe's "Spec Mode" view: a horizontal
 * phase progress bar followed by collapsible sections for each phase.
 * Specify and Plan show their content (description / plan blob) and an
 * approval timestamp when the phase is done. Tasks shows the live task
 * list with status chips. Implement is a placeholder until the mission
 * crosses into post-task execution — the wireframe shows it explicitly
 * to communicate the four-step model even when nothing is happening
 * there yet.
 */
export function MissionSpecMode({ mission, phases }: MissionSpecModeProps) {
  const tasks = mission.tasks ?? []
  const buckets = groupTasksByStatusBucket(tasks)

  return (
    <div>
      <PhaseBar phases={phases} />

      <PhaseSection
        phase={phases[0]}
        title="1. Specify — what and why"
        defaultOpen={false}
      >
        <p className="text-foreground/90">{mission.description ?? "(no description)"}</p>
        {mission.created_at && (
          <ApprovalStamp label="Recorded" at={mission.created_at} />
        )}
      </PhaseSection>

      <PhaseSection
        phase={phases[1]}
        title={`2. Plan — how (${mission.lead_agent_name ? `@${mission.lead_agent_slug || mission.lead_agent_name}` : "lead agent"})`}
        defaultOpen={false}
      >
        {mission.plan ? (
          <pre className="whitespace-pre-wrap text-sm text-foreground/90 font-sans">
            {mission.plan}
          </pre>
        ) : (
          <p className="text-sm text-muted-foreground italic">
            No plan attached yet — waiting on the lead agent to draft one.
          </p>
        )}
        {mission.total_estimated_cost != null && (
          <div className="mt-3 text-sm font-semibold">
            Estimated cost: ${mission.total_estimated_cost.toFixed(2)}
          </div>
        )}
      </PhaseSection>

      <PhaseSection
        phase={phases[2]}
        title="3. Tasks — running"
        defaultOpen={true}
        rightHeader={
          tasks.length > 0 ? (
            <span className="ml-auto text-[11px] text-muted-foreground font-medium tabular-nums">
              {buckets.running.length} ⏳ · {buckets.done.length} ✅ ·{" "}
              {buckets.waiting.length} ⏸️ · {buckets.blocked.length} 🔒
            </span>
          ) : null
        }
      >
        {tasks.length === 0 ? (
          <p className="text-sm text-muted-foreground italic">
            No tasks created yet.
          </p>
        ) : (
          <ol className="flex flex-col gap-2">
            {tasks
              .slice()
              .sort((a, b) => a.task_order - b.task_order)
              .map((task) => (
                <TaskRow key={task.id} task={task} />
              ))}
          </ol>
        )}
      </PhaseSection>

      <PhaseSection
        phase={phases[3]}
        title="4. Implement"
        defaultOpen={false}
      >
        <div className="rounded-md bg-muted/50 px-4 py-6 text-center text-sm text-muted-foreground">
          {phases[3].status === "done"
            ? "Implementation complete."
            : phases[3].status === "active"
            ? "All tasks done — implementation can start."
            : "Waiting — runs once Tasks phase completes."}
        </div>
      </PhaseSection>
    </div>
  )
}

/**
 * PhaseBar is the horizontal four-step progress strip from the wireframe.
 * Each phase shows a circle (check / number / dot) and a label, joined
 * by a connector that fills when the prior phase is done. The active
 * phase pulses (scale animation only — pure visual, no layout shift).
 */
function PhaseBar({ phases }: { phases: MissionPhase[] }) {
  return (
    <div className="flex items-center gap-0 mb-7 px-2 py-4 bg-muted/40 rounded-lg">
      {phases.map((phase, idx) => (
        <div key={phase.id} className="flex items-center gap-2.5 flex-1 last:flex-none">
          <div className="flex items-center gap-2.5">
            <div
              className={cn(
                "w-8 h-8 rounded-full flex items-center justify-center text-xs font-bold flex-shrink-0",
                phase.status === "done" && "bg-emerald-500 text-white",
                phase.status === "active" &&
                  "bg-blue-500 text-white ring-4 ring-blue-500/20 animate-pulse",
                phase.status === "pending" &&
                  "bg-background text-muted-foreground border",
              )}
            >
              {phase.status === "done" ? <Check className="h-4 w-4" /> : idx + 1}
            </div>
            <span
              className={cn(
                "text-[13px] whitespace-nowrap",
                phase.status === "done" && "text-emerald-600 dark:text-emerald-400 font-medium",
                phase.status === "active" && "text-blue-600 dark:text-blue-400 font-semibold",
                phase.status === "pending" && "text-muted-foreground",
              )}
            >
              {phase.label}
            </span>
          </div>
          {idx < phases.length - 1 && (
            <div
              className={cn(
                "flex-1 h-0.5 mx-3",
                phase.status === "done" ? "bg-emerald-500" : "bg-border",
              )}
            />
          )}
        </div>
      ))}
    </div>
  )
}

interface PhaseSectionProps {
  phase: MissionPhase
  title: string
  defaultOpen?: boolean
  rightHeader?: React.ReactNode
  children: React.ReactNode
}

/**
 * PhaseSection is one collapsible row in the Spec view. Header shows the
 * phase status dot, the title, an optional right-aligned summary (used by
 * Tasks for the bucket counts), and a chevron. Body content is owned by
 * the caller — this component intentionally does not render plan/task
 * content directly so each phase can have its own layout.
 */
function PhaseSection({ phase, title, defaultOpen = false, rightHeader, children }: PhaseSectionProps) {
  const [open, setOpen] = useState(defaultOpen)

  return (
    <div className="border-b last:border-b-0">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="w-full flex items-center gap-3 py-3.5 text-left hover:bg-muted/40 px-2 -mx-2 rounded transition-colors"
      >
        <span
          className={cn(
            "h-2.5 w-2.5 rounded-full flex-shrink-0",
            phase.status === "done" && "bg-emerald-500",
            phase.status === "active" && "bg-blue-500 ring-4 ring-blue-500/20",
            phase.status === "pending" && "bg-border",
          )}
        />
        <span className="text-sm font-medium">{title}</span>
        {rightHeader}
        <ChevronDown
          className={cn(
            "h-4 w-4 text-muted-foreground transition-transform ml-2",
            open ? "rotate-0" : "-rotate-90",
          )}
        />
      </button>
      {open && <div className="pb-4 pl-5 pr-2 text-sm">{children}</div>}
    </div>
  )
}

function ApprovalStamp({ label, at }: { label: string; at: string }) {
  const ts = new Date(at)
  if (Number.isNaN(ts.getTime())) return null
  return (
    <div className="mt-3 inline-flex items-center gap-1 rounded bg-emerald-500/10 px-2 py-1 text-xs text-emerald-600 dark:text-emerald-400">
      <Check className="h-3 w-3" /> {label} {ts.toLocaleDateString()}
    </div>
  )
}

function TaskRow({ task }: { task: MissionTask }) {
  const visual = taskVisual(task.status)
  return (
    <li
      className={cn(
        "flex items-start gap-3 rounded-md border-l-4 px-3 py-2.5",
        visual.containerClass,
      )}
    >
      <visual.Icon className={cn("h-4 w-4 mt-0.5 flex-shrink-0", visual.iconClass)} />
      <div className="flex-1 min-w-0">
        <div className="text-sm font-medium leading-snug">
          {task.task_order}. {task.title}
        </div>
        <div className="mt-1 flex flex-wrap gap-1.5 text-[11px]">
          {task.agent_slug && (
            <Badge variant="secondary" className="font-mono text-[10px]">
              @{task.agent_slug}
            </Badge>
          )}
          {task.token_count != null && (
            <Badge variant="outline" className="text-[10px]">
              {task.token_count.toLocaleString()} tokens
            </Badge>
          )}
          {task.estimated_cost != null && task.estimated_cost > 0 && (
            <Badge variant="outline" className="text-[10px]">
              ${task.estimated_cost.toFixed(2)}
            </Badge>
          )}
          {task.depends_on && task.depends_on.length > 0 && (
            <Badge variant="outline" className="text-[10px]">
              dep: {task.depends_on}
            </Badge>
          )}
        </div>
      </div>
    </li>
  )
}

function taskVisual(status: MissionTaskStatus) {
  switch (status) {
    case "IN_PROGRESS":
      return {
        Icon: Clock,
        containerClass: "border-blue-500 bg-blue-500/5",
        iconClass: "text-blue-500",
      }
    case "COMPLETED":
    case "SKIPPED":
      return {
        Icon: Check,
        containerClass: "border-emerald-500 bg-emerald-500/5",
        iconClass: "text-emerald-500",
      }
    case "AWAITING_APPROVAL":
      return {
        Icon: Pause,
        containerClass: "border-amber-500 bg-amber-500/5",
        iconClass: "text-amber-500",
      }
    case "FAILED":
      return {
        Icon: Lock,
        containerClass: "border-rose-500 bg-rose-500/5",
        iconClass: "text-rose-500",
      }
    case "BLOCKED":
    case "PENDING":
    default:
      return {
        Icon: Lock,
        containerClass: "border-border bg-muted/40",
        iconClass: "text-muted-foreground",
      }
  }
}
