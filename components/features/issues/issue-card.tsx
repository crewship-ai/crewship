"use client"

import { Card } from "@/components/ui/card"
import { PriorityIcon } from "./priority-icon"
import { Clock } from "lucide-react"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import type { Mission } from "@/lib/types/mission"

function isOverdue(dueDate: string | null | undefined, status: string): boolean {
  if (!dueDate || status === "COMPLETED" || status === "DONE" || status === "CANCELLED") return false
  return new Date(dueDate) < new Date()
}

interface IssueCardProps {
  issue: Mission
  onClick: () => void
}

const LABEL_DOT_COLORS: Record<string, string> = {
  red: "bg-red-500",
  orange: "bg-orange-500",
  yellow: "bg-yellow-500",
  green: "bg-green-500",
  blue: "bg-blue-500",
  purple: "bg-purple-500",
  pink: "bg-pink-500",
  gray: "bg-gray-500",
  slate: "bg-slate-500",
  cyan: "bg-cyan-500",
  teal: "bg-teal-500",
  indigo: "bg-indigo-500",
  violet: "bg-violet-500",
  amber: "bg-amber-500",
  emerald: "bg-emerald-500",
  rose: "bg-rose-500",
  lime: "bg-lime-500",
  fuchsia: "bg-fuchsia-500",
}

export function IssueCard({ issue, onClick }: IssueCardProps) {
  const overdue = isOverdue(issue.due_date, issue.status)

  return (
    <Card
      className={cn(
        "px-3 py-2.5 cursor-pointer hover:bg-accent/50 transition-colors border-border/60",
        overdue && "border-red-500/40"
      )}
      onClick={onClick}
    >
      {/* Top: identifier + priority + overdue */}
      <div className="flex items-center justify-between gap-2 mb-1">
        <div className="flex items-center gap-1.5">
          {issue.identifier && (
            <span className="text-xs font-mono text-muted-foreground">
              {issue.identifier}
            </span>
          )}
          {overdue && (
            <Clock className="h-3 w-3 text-red-500" />
          )}
        </div>
        <PriorityIcon
          priority={issue.priority || "none"}
          className="h-3.5 w-3.5 shrink-0"
        />
      </div>

      {/* Title */}
      <p className="text-sm font-medium leading-snug line-clamp-2 text-foreground">
        {issue.title}
      </p>

      {/* Bottom: assignee + labels */}
      <div className="flex items-center gap-2 mt-2">
        {issue.assignee_name && (
          <div className="flex items-center gap-1.5 min-w-0">
            <img
              src={getAgentAvatarUrl(issue.assignee_id || issue.assignee_name)}
              alt=""
              className="h-[18px] w-[18px] rounded-full shrink-0"
            />
            <span className="text-xs text-muted-foreground truncate">
              {issue.assignee_name}
            </span>
          </div>
        )}
        <div className="flex-1" />
        {issue.labels && issue.labels.length > 0 && (
          <div className="flex items-center gap-1">
            {issue.labels.slice(0, 3).map((label) => {
              const dotClass =
                LABEL_DOT_COLORS[label.color.toLowerCase()] || "bg-gray-400"
              return (
                <span
                  key={label.id}
                  className={cn("h-2 w-2 rounded-full shrink-0", dotClass)}
                  title={label.name}
                  style={
                    !LABEL_DOT_COLORS[label.color.toLowerCase()]
                      ? { backgroundColor: label.color }
                      : undefined
                  }
                />
              )
            })}
          </div>
        )}
      </div>
    </Card>
  )
}
