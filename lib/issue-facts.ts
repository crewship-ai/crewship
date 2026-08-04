// The figures band for an issue and for a project.
//
// Pure, and separate from the components that render it, for two reasons.
// The first is that "is this overdue" is a decision, not a format: it
// depends on the status as well as the date, and a decision made inline in
// JSX is a decision nothing can test. The second is that both surfaces feed
// the same `StatStrip` from the detail kit, so the shape has to be one
// shape — the moment each screen builds its own row of figures we are back
// to four different ideas of what a number looks like.
//
// See components/ui/detail: the strip is ONE bordered band split by
// hairlines, never a row of separate cards. Six facts is what fits before
// the band stops being glanceable.

import { formatShortDate, relTime } from "@/lib/time"
import type {
  IssuePriority,
  Mission,
  MissionStatus,
  Project,
  ProjectHealth,
  ProjectStats,
} from "@/lib/types/mission"

/** One cell of the figures band. Mirrors `StatItem` in components/ui/detail. */
export interface StatFact {
  label: string
  value: string
  /** Renders the value in mono — dates, ids, durations. */
  mono?: boolean
  tone?: "default" | "success" | "warn" | "destructive"
}

const DASH = "—"

/** Statuses past which an issue can no longer miss a due date. */
const TERMINAL_STATUSES: MissionStatus[] = [
  "COMPLETED",
  "DONE",
  "CANCELLED",
  "DUPLICATE",
]

export function isTerminal(status: MissionStatus): boolean {
  return TERMINAL_STATUSES.includes(status)
}

/**
 * Six facts about an issue, in the order an operator asks them: when did
 * this arrive, when did it last move, when is it owed, how big is it, what
 * hangs off it, and how much has been said about it.
 *
 * `now` is injectable so the overdue decision is testable; the relative
 * strings themselves come from `relTime`, which reads the wall clock and is
 * not asserted anywhere.
 */
export function issueFacts(
  issue: Mission,
  { comments, now = new Date() }: { comments: number; now?: Date },
): StatFact[] {
  const closed = issue.completed_at && isTerminal(issue.status)

  // A completed issue reports when it closed. Keeping a due date there
  // would keep scoring a race that finished.
  const timing: StatFact = closed
    ? { label: "Closed", value: relTime(issue.completed_at!), tone: "success" }
    : {
        label: "Due",
        value: issue.due_date ? formatShortDate(issue.due_date) : DASH,
        tone: overdueTone(issue.due_date, now, isTerminal(issue.status)),
      }

  return [
    { label: "Opened", value: relTime(issue.created_at) },
    { label: "Updated", value: relTime(issue.updated_at) },
    timing,
    {
      label: "Estimate",
      value: issue.estimate != null ? `${issue.estimate} pts` : DASH,
      mono: true,
    },
    { label: "Sub-issues", value: String(issue.sub_issues_count ?? 0) },
    { label: "Comments", value: String(comments) },
  ]
}

/**
 * Six facts about a project: how much work it holds, how much is finished,
 * how much is moving, and the two dates that bound it.
 *
 * `stats` is the /stats endpoint's answer and wins wherever it is loaded —
 * `issue_count` / `done_count` / `progress` on the row are denormalised
 * counters that lag a write.
 */
export function projectFacts(
  project: Project,
  stats: ProjectStats | null,
  { now = new Date() }: { now?: Date } = {},
): StatFact[] {
  const scope = stats?.total_issues ?? project.issue_count
  const done = stats?.completed_issues ?? project.done_count
  // Review is in-flight work: somebody is waiting on a human, not on the
  // agent, but nothing about it is finished.
  const active = (stats?.by_status?.IN_PROGRESS ?? 0) + (stats?.by_status?.REVIEW ?? 0)
  const finished = project.status === "completed" || project.status === "cancelled"

  return [
    { label: "Scope", value: String(scope) },
    { label: "Completed", value: String(done), tone: done > 0 ? "success" : undefined },
    { label: "In progress", value: String(active) },
    { label: "Progress", value: `${clampPercent(project.progress)}%` },
    {
      label: "Started",
      value: project.start_date ? formatShortDate(project.start_date) : DASH,
      mono: true,
    },
    {
      label: "Target",
      value: project.target_date ? formatShortDate(project.target_date) : DASH,
      mono: true,
      tone: overdueTone(project.target_date, now, finished),
    },
  ]
}

/** Destructive once the date is behind us and the thing is still open. */
function overdueTone(
  date: string | null | undefined,
  now: Date,
  finished: boolean,
): StatFact["tone"] {
  if (!date || finished) return undefined
  const t = new Date(date).getTime()
  if (Number.isNaN(t)) return undefined
  return t < now.getTime() ? "destructive" : undefined
}

function clampPercent(n: number): number {
  if (!Number.isFinite(n)) return 0
  return Math.max(0, Math.min(100, Math.round(n)))
}

export type DetailTone = "default" | "success" | "destructive" | "warn" | "blue" | "purple"

/**
 * Issue status in the shared detail palette, so a Backlog pill here is the
 * same colour as a Backlog pill in Inbox, Activity and the board.
 */
export function issueStatusTone(status: MissionStatus | undefined): DetailTone {
  switch (status) {
    case "COMPLETED":
    case "DONE":
      return "success"
    case "FAILED":
    case "CANCELLED":
    case "DUPLICATE":
      return "destructive"
    case "IN_PROGRESS":
      return "blue"
    case "REVIEW":
      return "purple"
    case "PLANNING":
    case "TODO":
      return "warn"
    default:
      return "default"
  }
}

/**
 * Only urgent and high get an alarm colour. Medium and low are the ordinary
 * case, and a board where every pill is coloured says nothing.
 */
export function issuePriorityTone(priority: IssuePriority | undefined): DetailTone {
  switch (priority) {
    case "urgent":
      return "destructive"
    case "high":
      return "warn"
    default:
      return "default"
  }
}

export function projectHealthTone(health: ProjectHealth | undefined): DetailTone {
  switch (health) {
    case "at_risk":
      return "warn"
    case "off_track":
      return "destructive"
    default:
      return "success"
  }
}

/** Project status as a sentence-cased label — `in_progress` reads as machine output. */
export function projectStatusLabel(status: Project["status"]): string {
  return status.replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase())
}

export function projectHealthLabel(health: ProjectHealth): string {
  switch (health) {
    case "at_risk":
      return "At risk"
    case "off_track":
      return "Off track"
    default:
      return "On track"
  }
}
