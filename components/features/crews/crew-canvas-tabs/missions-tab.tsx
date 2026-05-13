"use client"

import Link from "next/link"
import { cn } from "@/lib/utils"

import type { CrewRecord, IssueRow, IssuesSnapshot, MissionData } from "./types"
import { issueStatusColor } from "./types"

export interface MissionsTabProps {
  crew: CrewRecord
  recentMissions: MissionData[]
  issues: IssuesSnapshot | null
  recentIssues: IssueRow[]
}

export function MissionsTab({ crew, recentMissions, issues, recentIssues }: MissionsTabProps) {
  return (
    <div className="space-y-7">
      {/* Recent missions */}
      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <h2 className="text-lg font-semibold">
            Recent missions
            {recentMissions.length > 0 && (
              <span className="text-muted-foreground text-sm font-normal ml-2">{recentMissions.length}</span>
            )}
          </h2>
          <Link href="/issues" className="text-xs text-blue-300 hover:underline">
            Open in /issues →
          </Link>
        </div>
        {recentMissions.length === 0 ? (
          <div className="rounded-xl border border-white/8 bg-card p-6 text-center text-xs text-muted-foreground">
            No missions yet for this crew.
          </div>
        ) : (
          <ul className="rounded-xl border border-white/8 bg-card divide-y divide-white/5">
            {recentMissions.map((m) => (
              <li key={m.id}>
                <Link
                  href={`/missions/${encodeURIComponent(m.id)}/timeline`}
                  className="px-4 py-2 flex items-center gap-3 text-sm hover:bg-white/[0.03] transition-colors"
                >
                  <span className={cn(
                    "w-1.5 h-1.5 rounded-full shrink-0",
                    m.status === "RUNNING" ? "bg-emerald-400" : m.status === "FAILED" ? "bg-red-500" : "bg-zinc-500",
                  )} />
                  <span className="truncate flex-1 text-foreground/85">{m.title}</span>
                  <span className="text-[10px] text-muted-foreground shrink-0 uppercase">
                    {m.status?.replace(/_/g, " ").toLowerCase()}
                  </span>
                  <span className="text-[10px] text-muted-foreground shrink-0">
                    {new Date(m.created_at).toLocaleDateString()}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </section>

      {/* Issues */}
      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <h2 className="text-lg font-semibold">
            Issues
            {crew.issue_prefix && (
              <span className="text-muted-foreground text-sm font-normal ml-2 font-mono uppercase">{crew.issue_prefix}</span>
            )}
          </h2>
          <Link href="/issues" className="text-xs text-blue-300 hover:underline">
            Open in /issues →
          </Link>
        </div>
        <div className="rounded-xl border border-white/8 bg-card grid grid-cols-5 divide-x divide-white/5">
          {(["Backlog", "Todo", "InProgress", "InReview", "Done"] as const).map((bucket) => (
            <div key={bucket} className="px-4 py-3">
              <div className="text-[10px] text-muted-foreground uppercase">{bucket.replace(/([A-Z])/g, " $1").trim()}</div>
              <div className={cn("text-2xl font-semibold mt-1", issues?.[bucket] ? "text-foreground" : "text-muted-foreground")}>
                {issues?.[bucket] ?? "—"}
              </div>
            </div>
          ))}
        </div>
        {recentIssues.length > 0 && (
          <ul className="rounded-xl border border-white/8 bg-card divide-y divide-white/5">
            {recentIssues.map((i) => (
              <li key={i.id}>
                <Link
                  href={i.identifier ? `/issues/${encodeURIComponent(i.identifier)}` : "/issues"}
                  className="px-4 py-2 flex items-center gap-3 text-sm hover:bg-white/[0.03] transition-colors"
                >
                  <span className={cn(
                    "w-1.5 h-1.5 rounded-full shrink-0",
                    issueStatusColor(i.status),
                  )} />
                  {i.identifier && (
                    <code className="text-[11px] text-muted-foreground shrink-0 font-mono">
                      {i.identifier}
                    </code>
                  )}
                  <span className="truncate flex-1 text-foreground/85">{i.title}</span>
                  <span className="text-[10px] text-muted-foreground shrink-0 uppercase">
                    {i.status?.replace(/_/g, " ").toLowerCase()}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}
