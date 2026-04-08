"use client"

import { ScrollArea } from "@/components/ui/scroll-area"
import { IssueCard } from "./issue-card"
import { StatusIcon, statusLabel, statusColor } from "./status-icon"
import { cn } from "@/lib/utils"
import { CirclePlus } from "lucide-react"
import type { Mission, MissionStatus } from "@/lib/types/mission"

interface IssuesBoardViewProps {
  issues: Mission[]
  onIssueClick: (issue: Mission) => void
  onCreateClick?: () => void
}

const MAIN_STATUSES: MissionStatus[] = ["BACKLOG", "TODO", "IN_PROGRESS", "REVIEW", "COMPLETED"]
const SECONDARY_STATUSES: MissionStatus[] = ["FAILED", "CANCELLED", "DUPLICATE"]

export function IssuesBoardView({ issues, onIssueClick, onCreateClick }: IssuesBoardViewProps) {
  const hasIssues = issues.length > 0
  const secondaryIssues = issues.filter((i) =>
    SECONDARY_STATUSES.includes(i.status)
  )
  const showSecondary = secondaryIssues.length > 0

  if (!hasIssues) {
    return (
      <div className="flex flex-col items-center justify-center h-[50vh] text-center">
        <div className="rounded-full bg-muted/50 p-4 mb-4">
          <CirclePlus className="h-8 w-8 text-muted-foreground/50" />
        </div>
        <h3 className="text-lg font-medium mb-1">No issues yet</h3>
        <p className="text-sm text-muted-foreground mb-4 max-w-sm">
          Create your first issue to start tracking work. Issues can be assigned to agents or team members.
        </p>
        {onCreateClick && (
          <button
            onClick={onCreateClick}
            className="text-sm text-primary hover:underline"
          >
            Create an issue
          </button>
        )}
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-4 h-[calc(100vh-12rem)]">
      {/* Main columns */}
      <div className="flex gap-4 flex-1 overflow-x-auto pb-2">
        {MAIN_STATUSES.map((status) => {
          const colIssues = issues.filter((i) => i.status === status)
          return (
            <div key={status} className="flex flex-col min-w-[280px] w-[280px] shrink-0">
              <div className="flex items-center gap-2 px-1 pb-3">
                <StatusIcon status={status} className="h-3.5 w-3.5" />
                <span className="text-sm font-medium text-foreground/80">
                  {statusLabel[status]}
                </span>
                <span className="text-xs text-muted-foreground/70 tabular-nums">
                  {colIssues.length}
                </span>
              </div>
              <ScrollArea className="flex-1 -mx-1">
                <div className="flex flex-col gap-2 px-1 pb-2">
                  {colIssues.length === 0 ? (
                    <div className="flex items-center justify-center h-20 rounded-lg border border-dashed border-border/50">
                      <span className="text-xs text-muted-foreground/50">No issues</span>
                    </div>
                  ) : (
                    colIssues.map((issue) => (
                      <IssueCard
                        key={issue.id}
                        issue={issue}
                        onClick={() => onIssueClick(issue)}
                      />
                    ))
                  )}
                </div>
              </ScrollArea>
            </div>
          )
        })}
      </div>

      {/* Secondary rows (Failed/Cancelled/Duplicate) */}
      {showSecondary && (
        <div className="flex gap-4 border-t pt-3">
          {SECONDARY_STATUSES.map((status) => {
            const colIssues = issues.filter((i) => i.status === status)
            if (colIssues.length === 0) return null
            return (
              <div key={status} className="flex-1">
                <div className="flex items-center gap-2 mb-2">
                  <StatusIcon status={status} className="h-3.5 w-3.5" />
                  <span className="text-xs font-medium text-muted-foreground">
                    {statusLabel[status]}
                  </span>
                  <span className="text-xs text-muted-foreground/70 tabular-nums">
                    {colIssues.length}
                  </span>
                </div>
                <div className="flex gap-2 overflow-x-auto">
                  {colIssues.map((issue) => (
                    <div key={issue.id} className="w-[240px] shrink-0">
                      <IssueCard issue={issue} onClick={() => onIssueClick(issue)} />
                    </div>
                  ))}
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

