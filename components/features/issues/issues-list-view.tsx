"use client"

import { useCallback, useMemo, useState } from "react"
import { ArrowDown, ArrowUp, ArrowUpDown } from "lucide-react"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { PriorityIcon, priorityLabel } from "./priority-icon"
import { StatusIcon, statusLabel } from "./status-icon"
import { LabelBadge } from "./label-badge"
import { cn } from "@/lib/utils"
import type { Mission, MissionStatus, IssuePriority } from "@/lib/types/mission"

interface IssuesListViewProps {
  issues: Mission[]
  onIssueClick: (issue: Mission) => void
}

const STATUS_CONFIG: Record<string, { label: string; className: string }> = {
  BACKLOG: {
    label: "Backlog",
    className: "bg-slate-100 text-slate-700 dark:bg-slate-800/40 dark:text-slate-300",
  },
  TODO: {
    label: "Todo",
    className: "bg-slate-100 text-slate-700 dark:bg-slate-800/40 dark:text-slate-300",
  },
  PLANNING: {
    label: "Planning",
    className: "bg-slate-100 text-slate-700 dark:bg-slate-800/40 dark:text-slate-300",
  },
  IN_PROGRESS: {
    label: "In Progress",
    className: "bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300",
  },
  REVIEW: {
    label: "In Review",
    className: "bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300",
  },
  COMPLETED: {
    label: "Done",
    className: "bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300",
  },
  FAILED: {
    label: "Failed",
    className: "bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300",
  },
  CANCELLED: {
    label: "Cancelled",
    className: "bg-gray-100 text-gray-700 dark:bg-gray-900/40 dark:text-gray-300",
  },
  DUPLICATE: {
    label: "Duplicate",
    className: "bg-gray-100 text-gray-700 dark:bg-gray-900/40 dark:text-gray-300",
  },
}

const PRIORITY_ORDER: Record<IssuePriority, number> = {
  urgent: 0,
  high: 1,
  medium: 2,
  low: 3,
  none: 4,
}

const STATUS_ORDER: Record<MissionStatus, number> = {
  BACKLOG: 0,
  TODO: 1,
  PLANNING: 2,
  IN_PROGRESS: 3,
  REVIEW: 4,
  COMPLETED: 5,
  FAILED: 6,
  CANCELLED: 7,
  DUPLICATE: 8,
}

type SortKey = "identifier" | "title" | "status" | "priority" | "assignee" | "crew" | "updated"
type SortDir = "asc" | "desc"

function formatRelativeTime(dateStr: string): string {
  const now = Date.now()
  const date = new Date(dateStr).getTime()
  const diffMs = now - date
  const diffMin = Math.floor(diffMs / 60000)
  if (diffMin < 1) return "just now"
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHours = Math.floor(diffMin / 60)
  if (diffHours < 24) return `${diffHours}h ago`
  const diffDays = Math.floor(diffHours / 24)
  if (diffDays < 30) return `${diffDays}d ago`
  return new Date(dateStr).toLocaleDateString()
}

export function IssuesListView({ issues, onIssueClick }: IssuesListViewProps) {
  const [sortKey, setSortKey] = useState<SortKey>("updated")
  const [sortDir, setSortDir] = useState<SortDir>("desc")

  const handleSort = useCallback(
    (key: SortKey) => {
      if (sortKey === key) {
        setSortDir((d) => (d === "asc" ? "desc" : "asc"))
      } else {
        setSortKey(key)
        setSortDir(key === "updated" ? "desc" : "asc")
      }
    },
    [sortKey],
  )

  const sorted = useMemo(() => {
    const arr = [...issues]
    const dir = sortDir === "asc" ? 1 : -1
    arr.sort((a, b) => {
      switch (sortKey) {
        case "identifier":
          return dir * ((a.number ?? 0) - (b.number ?? 0))
        case "title":
          return dir * a.title.localeCompare(b.title)
        case "status":
          return dir * ((STATUS_ORDER[a.status] ?? 99) - (STATUS_ORDER[b.status] ?? 99))
        case "priority":
          return (
            dir *
            ((PRIORITY_ORDER[a.priority || "none"] ?? 4) -
              (PRIORITY_ORDER[b.priority || "none"] ?? 4))
          )
        case "assignee":
          return dir * (a.assignee_name || "").localeCompare(b.assignee_name || "")
        case "crew":
          return dir * (a.crew_name || "").localeCompare(b.crew_name || "")
        case "updated":
          return dir * (new Date(a.updated_at).getTime() - new Date(b.updated_at).getTime())
        default:
          return 0
      }
    })
    return arr
  }, [issues, sortKey, sortDir])

  function SortIcon({ column }: { column: SortKey }) {
    if (sortKey !== column)
      return <ArrowUpDown className="h-3 w-3 text-muted-foreground/40" />
    return sortDir === "asc" ? (
      <ArrowUp className="h-3 w-3" />
    ) : (
      <ArrowDown className="h-3 w-3" />
    )
  }

  if (issues.length === 0) {
    return (
      <div className="flex flex-col items-center gap-2 py-16 text-center">
        <p className="text-sm text-muted-foreground">No issues found</p>
        <p className="text-xs text-muted-foreground/60">
          Create your first issue to get started.
        </p>
      </div>
    )
  }

  return (
    <div className="rounded-lg border border-border overflow-hidden">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead
              className="w-[90px] cursor-pointer select-none"
              onClick={() => handleSort("identifier")}
            >
              <span className="flex items-center gap-1">
                ID <SortIcon column="identifier" />
              </span>
            </TableHead>
            <TableHead
              className="cursor-pointer select-none"
              onClick={() => handleSort("title")}
            >
              <span className="flex items-center gap-1">
                Title <SortIcon column="title" />
              </span>
            </TableHead>
            <TableHead
              className="w-[110px] cursor-pointer select-none"
              onClick={() => handleSort("status")}
            >
              <span className="flex items-center gap-1">
                Status <SortIcon column="status" />
              </span>
            </TableHead>
            <TableHead
              className="w-[90px] cursor-pointer select-none"
              onClick={() => handleSort("priority")}
            >
              <span className="flex items-center gap-1">
                Priority <SortIcon column="priority" />
              </span>
            </TableHead>
            <TableHead
              className="w-[120px] cursor-pointer select-none"
              onClick={() => handleSort("assignee")}
            >
              <span className="flex items-center gap-1">
                Assignee <SortIcon column="assignee" />
              </span>
            </TableHead>
            <TableHead
              className="w-[100px] cursor-pointer select-none"
              onClick={() => handleSort("crew")}
            >
              <span className="flex items-center gap-1">
                Crew <SortIcon column="crew" />
              </span>
            </TableHead>
            <TableHead className="w-[120px]">Labels</TableHead>
            <TableHead
              className="w-[90px] cursor-pointer select-none"
              onClick={() => handleSort("updated")}
            >
              <span className="flex items-center gap-1">
                Updated <SortIcon column="updated" />
              </span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {sorted.map((issue) => {
            const statusCfg = STATUS_CONFIG[issue.status] || STATUS_CONFIG.BACKLOG
            return (
              <TableRow
                key={issue.id}
                className="cursor-pointer"
                onClick={() => onIssueClick(issue)}
              >
                <TableCell className="text-xs font-mono text-muted-foreground">
                  {issue.identifier || "--"}
                </TableCell>
                <TableCell>
                  <span className="text-sm font-medium line-clamp-1">
                    {issue.title}
                  </span>
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1.5">
                    <StatusIcon status={issue.status} className="h-3.5 w-3.5" />
                    <span className="text-xs text-muted-foreground">
                      {statusLabel[issue.status] || issue.status}
                    </span>
                  </div>
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1.5">
                    <PriorityIcon
                      priority={issue.priority || "none"}
                      className="h-3.5 w-3.5"
                    />
                    <span className="text-xs text-muted-foreground">
                      {priorityLabel[issue.priority || "none"]}
                    </span>
                  </div>
                </TableCell>
                <TableCell className="text-xs text-muted-foreground truncate">
                  {issue.assignee_name || "--"}
                </TableCell>
                <TableCell className="text-xs text-muted-foreground truncate">
                  {issue.crew_name || "--"}
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1 flex-wrap">
                    {issue.labels && issue.labels.length > 0
                      ? issue.labels
                          .slice(0, 2)
                          .map((label) => (
                            <LabelBadge key={label.id} label={label} />
                          ))
                      : <span className="text-xs text-muted-foreground/50">--</span>}
                  </div>
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {formatRelativeTime(issue.updated_at)}
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
    </div>
  )
}
