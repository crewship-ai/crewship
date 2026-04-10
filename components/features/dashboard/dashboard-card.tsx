"use client"

import * as React from "react"
import { cn } from "@/lib/utils"

interface DashboardCardProps extends Omit<React.ComponentProps<"div">, "title"> {
  title: React.ReactNode
  hint?: React.ReactNode
  action?: React.ReactNode
}

/**
 * Shared card shell for every dashboard tile. Matches the orchestration
 * card aesthetic — tight padding, 10 px uppercase title, optional hint
 * right-side, border-60, rounded-lg.
 */
export function DashboardCard({ title, hint, action, className, children, ...rest }: DashboardCardProps) {
  return (
    <div className={cn("rounded-lg border border-border/60 bg-card p-3", className)} {...rest}>
      <div className="flex items-center justify-between mb-2.5">
        <div className="text-[10px] font-semibold text-foreground/60 uppercase tracking-wider">{title}</div>
        {(hint || action) && (
          <div className="flex items-center gap-2 text-[10px] font-mono text-muted-foreground/60">
            {hint}
            {action}
          </div>
        )}
      </div>
      {children}
    </div>
  )
}
