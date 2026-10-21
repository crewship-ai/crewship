import { getIssueWorker } from "@/lib/issue-execution"
import type { IssuePriority, Mission, MissionStatus } from "@/lib/types/mission"

/**
 * What the Issues list and board show, as pure functions: grouping, order,
 * the quick views and the row fields a person can switch off. Kept apart from
 * the components so each rule is testable without a render.
 */

/** Status words in sentence case, as Harbor 2 writes them. */
export const STATUS_TEXT: Record<string, string> = {
  BACKLOG: "Backlog",
  TODO: "Todo",
  PLANNING: "Planning",
  IN_PROGRESS: "In progress",
  REVIEW: "In review",
  COMPLETED: "Done",
  DONE: "Done",
  FAILED: "Failed",
  CANCELLED: "Cancelled",
  DUPLICATE: "Duplicate",
}

export const PRIORITY_TEXT: Record<IssuePriority, string> = {
  urgent: "Urgent",
  high: "High",
  medium: "Medium",
  low: "Low",
  none: "No priority",
}

/** COMPLETED is the retired spelling of DONE; both land in one place. */
export function normalStatus(s: MissionStatus | string): string {
  return s === "COMPLETED" ? "DONE" : s
}

/* ------------------------------------------------------------ row fields */

export type RowField = "id" | "labels" | "sub" | "due" | "owner" | "updated"

/** The Display options' "Show on rows", in their order. */
export const ROW_FIELDS: { id: RowField; label: string }[] = [
  { id: "id", label: "ID" },
  { id: "labels", label: "Labels" },
  { id: "sub", label: "Sub-issues" },
  { id: "due", label: "Due date" },
  { id: "owner", label: "Owner" },
  { id: "updated", label: "Updated" },
]

export type RowFields = Record<RowField, boolean>

export const DEFAULT_ROW_FIELDS: RowFields = { id: true, labels: true, sub: true, due: true, owner: true, updated: true }

/* ---------------------------------------------------------- group, order */

export type GroupBy = "status" | "project" | "delegate" | "priority" | "none"
export type OrderBy = "manual" | "priority" | "updated" | "due" | "created"

export const GROUP_OPTIONS: { id: GroupBy; label: string }[] = [
  { id: "status", label: "Status" },
  { id: "project", label: "Project" },
  { id: "delegate", label: "Delegate" },
  { id: "priority", label: "Priority" },
  { id: "none", label: "None" },
]

export const ORDER_OPTIONS: { id: OrderBy; label: string }[] = [
  { id: "manual", label: "Manual" },
  { id: "priority", label: "Priority" },
  { id: "updated", label: "Updated" },
  { id: "due", label: "Due date" },
  { id: "created", label: "Created" },
]

/** Group order for status: the work in motion first, then what waits, then what is closed. */
export const STATUS_GROUP_ORDER = ["IN_PROGRESS", "REVIEW", "TODO", "PLANNING", "BACKLOG", "DONE", "FAILED", "CANCELLED", "DUPLICATE"]

const PRIORITY_RANK: Record<IssuePriority, number> = { urgent: 0, high: 1, medium: 2, low: 3, none: 4 }
const PRIORITY_ORDER: IssuePriority[] = ["urgent", "high", "medium", "low", "none"]

const time = (s: string | null | undefined) => {
  const t = s ? new Date(s).getTime() : NaN
  return Number.isNaN(t) ? null : t
}

export function orderIssues(issues: Mission[], order: OrderBy): Mission[] {
  const arr = [...issues]
  const created = (a: Mission, b: Mission) => (time(b.created_at) ?? 0) - (time(a.created_at) ?? 0)
  switch (order) {
    case "manual":
      return arr.sort((a, b) => (a.sort_order ?? 0) - (b.sort_order ?? 0) || created(a, b))
    case "priority":
      return arr.sort((a, b) => PRIORITY_RANK[a.priority || "none"] - PRIORITY_RANK[b.priority || "none"] || created(a, b))
    case "updated":
      return arr.sort((a, b) => (time(b.updated_at) ?? 0) - (time(a.updated_at) ?? 0))
    case "due":
      // Soonest first; an issue without a due date goes last.
      return arr.sort((a, b) => {
        const da = time(a.due_date)
        const db = time(b.due_date)
        if (da === db) return created(a, b)
        if (da === null) return 1
        if (db === null) return -1
        return da - db
      })
    case "created":
    default:
      return arr.sort(created)
  }
}

export interface IssueGroup {
  key: string
  label: string
  /** The status, when the list is grouped by status (drives the icon and the prefilled New issue). */
  status?: string
  priority?: IssuePriority
  issues: Mission[]
}

/** Groups with no issues are left out; the order inside a group is kept. */
export function groupIssues(issues: Mission[], groupBy: GroupBy): IssueGroup[] {
  if (groupBy === "none") return issues.length ? [{ key: "all", label: "All issues", issues }] : []
  if (groupBy === "status") {
    const by = new Map<string, Mission[]>()
    for (const i of issues) {
      const s = normalStatus(i.status)
      by.set(s, [...(by.get(s) ?? []), i])
    }
    const known = STATUS_GROUP_ORDER.filter((s) => by.has(s))
    const other = [...by.keys()].filter((s) => !STATUS_GROUP_ORDER.includes(s))
    return [...known, ...other].map((s) => ({ key: s, label: STATUS_TEXT[s] ?? s, status: s, issues: by.get(s)! }))
  }
  if (groupBy === "priority") {
    return PRIORITY_ORDER.map((p) => ({
      key: p,
      label: PRIORITY_TEXT[p],
      priority: p,
      issues: issues.filter((i) => (i.priority || "none") === p),
    })).filter((g) => g.issues.length)
  }
  // Project or delegate: named groups alphabetically, the empty one last.
  const name = (i: Mission): string | null => {
    if (groupBy === "project") return i.project_id ? i.project_name || "Untitled project" : null
    const w = getIssueWorker(i)
    return i.delegate?.id ? i.delegate.name || "Agent" : w.id && !w.isHuman ? w.name || "Agent" : null
  }
  const by = new Map<string, Mission[]>()
  const none: Mission[] = []
  for (const i of issues) {
    const n = name(i)
    if (n === null) none.push(i)
    else by.set(n, [...(by.get(n) ?? []), i])
  }
  const groups: IssueGroup[] = [...by.keys()].sort((a, b) => a.localeCompare(b)).map((n) => ({ key: n, label: n, issues: by.get(n)! }))
  if (none.length) groups.push({ key: "__none", label: groupBy === "project" ? "No project" : "No delegate", issues: none })
  return groups
}

/* ----------------------------------------------------------- quick views */

export type QuickView = "mine" | "reply" | "working" | "review" | "blocked" | "all"

/**
 * The Views section, in v67's order. Every view is a server filter on
 * GET /issues (internal/api/issue_list_views.go): a filter over the loaded
 * page (at most 100) would quietly leave issues out, and a count taken from
 * it would lie.
 *
 *  - My work: open issues I own, or hold as the worker
 *  - Needs my reply: open, I own or hold it, and a person is wanted (session
 *    awaiting_input or error, or the execution needs_human)
 *  - Agents working: an agent holds the issue and its session is active or stale
 *  - Waiting for my review: in review with client acceptance on, and I asked
 *    for it (the requester, not the owner)
 *  - Blocked: an open issue blocks it
 */
export const QUICK_VIEWS: { id: QuickView; label: string }[] = [
  { id: "mine", label: "My work" },
  { id: "reply", label: "Needs my reply" },
  { id: "working", label: "Agents working" },
  { id: "review", label: "Waiting for my review" },
  { id: "blocked", label: "Blocked" },
  { id: "all", label: "All issues" },
]

const QUICK_QUERY: Record<QuickView, string> = {
  mine: "mine=1",
  reply: "needs=reply",
  working: "working=1",
  review: "needs=review",
  blocked: "blocked=1",
  all: "",
}

/** The GET /issues query string that selects this view ("" for All issues). */
export function quickViewQuery(view: QuickView): string {
  return QUICK_QUERY[view]
}

export function isQuickView(v: string | null | undefined): v is QuickView {
  return !!v && v in QUICK_QUERY
}

const CLOSED = new Set(["DONE", "COMPLETED", "CANCELLED", "DUPLICATE"])

/* ------------------------------------------------------------ small bits */

/** "3h", "2d" — the list's updated column. */
export function shortAgo(iso: string | null | undefined, now = Date.now()): string {
  const t = time(iso)
  if (t === null) return ""
  const s = Math.max(0, Math.floor((now - t) / 1000))
  if (s < 60) return "now"
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h`
  if (s < 86400 * 30) return `${Math.floor(s / 86400)}d`
  if (s < 86400 * 365) return `${Math.floor(s / (86400 * 30))}mo`
  return `${Math.floor(s / (86400 * 365))}y`
}

/** The due chip: "Today", "Tomorrow", "Overdue" or a short date; `late` turns it red. */
export function dueLabel(iso: string | null | undefined, status: string, now = new Date()): { text: string; late: boolean } | null {
  const t = time(iso)
  if (t === null) return null
  const d = new Date(t)
  const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime()
  const diff = Math.round((day(d) - day(now)) / 86400000)
  const open = !CLOSED.has(status) && status !== "FAILED"
  if (diff === 0) return { text: "Today", late: open }
  if (diff === 1) return { text: "Tomorrow", late: false }
  if (diff < 0) return { text: open ? "Overdue" : d.toLocaleDateString(undefined, { day: "numeric", month: "short" }), late: open }
  return { text: d.toLocaleDateString(undefined, { day: "numeric", month: "short" }), late: false }
}

/** Board columns, in order. Cancelled and duplicate issues stay in the list. */
export const BOARD_STATUSES = ["BACKLOG", "TODO", "IN_PROGRESS", "REVIEW", "DONE", "FAILED"] as const
