"use client"

import * as React from "react"
import Link from "next/link"
import { CircleDot } from "lucide-react"

import type { Mission } from "@/lib/types/mission"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { cn } from "@/lib/utils"

export interface IssueBoardCounts {
  backlog: number
  inProgress: number
  review: number
  done: number
  open: number
}

/** Pure: the board's columns from the missions list the page already holds.
 *  Backlog folds TODO and PLANNING; done folds COMPLETED and DONE. Cancelled
 *  and duplicates count nowhere — they are neither work nor an outcome. */
export function issueBoardCounts(missions: Mission[]): IssueBoardCounts {
  const counts = { backlog: 0, inProgress: 0, review: 0, done: 0, open: 0 }
  for (const m of missions) {
    switch (m.status) {
      case "BACKLOG":
      case "TODO":
      case "PLANNING":
        counts.backlog += 1
        counts.open += 1
        break
      case "IN_PROGRESS":
        counts.inProgress += 1
        counts.open += 1
        break
      case "REVIEW":
        counts.review += 1
        counts.open += 1
        break
      case "COMPLETED":
      case "DONE":
        counts.done += 1
        break
      default:
        break
    }
  }
  return counts
}

const STATUS_PILL: Record<string, { label: string; className: string }> = {
  IN_PROGRESS: { label: "In progress", className: "border-primary/25 bg-primary/10 text-primary-hover" },
  REVIEW: { label: "Review", className: "border-warn/25 bg-warn/10 text-warn" },
  COMPLETED: { label: "Done", className: "border-success/25 bg-success/10 text-success" },
  DONE: { label: "Done", className: "border-success/25 bg-success/10 text-success" },
  FAILED: { label: "Failed", className: "border-destructive/25 bg-destructive/10 text-destructive" },
  BACKLOG: { label: "Backlog", className: "border-border bg-muted text-muted-foreground" },
  TODO: { label: "Todo", className: "border-border bg-muted text-muted-foreground" },
  PLANNING: { label: "Planning", className: "border-border bg-muted text-muted-foreground" },
}

export function WorkSnapshot({ missions, workspaceId }: { missions: Mission[]; workspaceId: string | null }) {
  const counts = React.useMemo(() => issueBoardCounts(missions), [missions])
  const recent = React.useMemo(
    () => [...missions].sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()).slice(0, 4),
    [missions],
  )
  const columns = [
    { key: "backlog", label: "Backlog", value: counts.backlog, className: "border-border/60 bg-foreground/[0.03]" },
    { key: "inProgress", label: "In progress", value: counts.inProgress, className: "border-primary/30 bg-primary/[0.08]" },
    { key: "review", label: "Review", value: counts.review, className: "border-warn/30 bg-warn/[0.08]" },
    { key: "done", label: "Done", value: counts.done, className: "border-success/30 bg-success/[0.08]" },
  ]
  return (
    <DashboardCard
      title="Work · issues"
      icon={CircleDot}
      hint={missions.length > 0 ? `${counts.open} open` : "no issues yet"}
      action={<Link href="/issues" className="text-primary-hover hover:underline">Board →</Link>}
      className="h-full"
    >
      {missions.length === 0 ? (
        <div className="flex items-center justify-between gap-3 rounded-lg border border-dashed border-border/60 px-3 py-3 text-label text-muted-foreground">
          <span>No issues yet. Give a crew its first task and it lands here.</span>
          <Link href="/issues?create=1" className="shrink-0 text-primary-hover hover:underline">New issue →</Link>
        </div>
      ) : (
        <>
          <div className="mb-3 grid grid-cols-4 gap-2" data-testid="dashboard-issue-board">
            {columns.map((col) => (
              <div key={col.key} className={cn("rounded-lg border px-2.5 py-2", col.className)}>
                <span className="block text-micro text-muted-foreground">{col.label}</span>
                <span className="block text-[20px] font-semibold leading-tight tabular-nums">{col.value}</span>
              </div>
            ))}
          </div>
          <div className="flex flex-col">
            {recent.map((mission) => {
              const pill = STATUS_PILL[mission.status] ?? { label: mission.status.toLowerCase().replace("_", " "), className: "border-border bg-muted text-muted-foreground" }
              const owner = mission.assignee_name || mission.lead_agent_name || mission.crew_name || ""
              const ownerSeed = mission.lead_agent_slug || mission.assignee_id || owner
              return (
                <Link
                  key={mission.id}
                  href={mission.identifier ? `/issues/${mission.identifier}` : "/issues"}
                  className="group flex items-center gap-2.5 border-t border-border/50 py-2 transition-colors hover:bg-foreground/[0.025]"
                >
                  <span className="w-14 shrink-0 truncate font-mono text-micro text-muted-foreground">{mission.identifier ?? "—"}</span>
                  <span className="min-w-0 flex-1 truncate text-body text-foreground/90">{mission.title}</span>
                  {owner && (
                    <AgentAvatar seed={ownerSeed} workspaceId={workspaceId} alt={owner} className="h-5 w-5 shrink-0 rounded-md bg-muted ring-1 ring-border" />
                  )}
                  <span className={cn("shrink-0 rounded-full border px-2 py-0.5 text-micro font-medium", pill.className)}>{pill.label}</span>
                </Link>
              )
            })}
          </div>
        </>
      )}
    </DashboardCard>
  )
}
