import React from "react"
import { Card, CardContent } from "@/components/ui/card"
import { cn } from "@/lib/utils"
import type { Stats } from "../types"

interface OverviewTabProps {
  stats: Stats | null
  runtimeAvailable: boolean | null
  runtimeInfo: { runtime: string; version: string; socket: string } | null
}

export const OverviewTab = React.memo(function OverviewTab({ stats, runtimeAvailable, runtimeInfo }: OverviewTabProps) {
  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
        {[
          { label: "Workspaces", value: stats?.workspaces ?? 0 },
          { label: "Total Users", value: stats?.users ?? 0 },
          { label: "Total Agents", value: stats?.agents ?? 0 },
          { label: "Running", value: stats?.running ?? 0, color: "text-emerald-600" },
        ].map((s) => (
          <Card key={s.label}>
            <CardContent className="p-4">
              <div className="text-micro text-muted-foreground uppercase font-medium">{s.label}</div>
              <div className={cn("text-2xl font-bold mt-1", s.color)}>{s.value}</div>
            </CardContent>
          </Card>
        ))}
      </div>
      <Card>
        <CardContent className="p-5 space-y-4">
          <div className="text-xs font-medium">System Status</div>
          <div className="space-y-3">
            {[
              { name: "Database", status: true, desc: "SQLite (connected)" },
              { name: "Engine", status: true, desc: "Running" },
              {
                name: "Container Runtime",
                status: runtimeAvailable === true,
                desc: runtimeAvailable === null ? "Checking..."
                  : runtimeAvailable ? `${runtimeInfo?.runtime === "apple" ? "Apple Containers" : (runtimeInfo?.runtime ?? "Unknown").charAt(0).toUpperCase() + (runtimeInfo?.runtime ?? "").slice(1)} ${runtimeInfo?.version ?? ""}`
                  : "Not detected",
              },
            ].map((s) => (
              <div key={s.name} className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <span className={cn("w-2 h-2 rounded-full", s.status ? "bg-emerald-500" : "bg-amber-400")} />
                  <span className="text-xs">{s.name}</span>
                </div>
                <span className="text-xs text-muted-foreground">{s.desc}</span>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
    </div>
  )
})
