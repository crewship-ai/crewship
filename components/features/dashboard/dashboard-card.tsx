"use client"

import * as React from "react"
import type { LucideIcon } from "lucide-react"
import { cn } from "@/lib/utils"

interface DashboardCardProps extends Omit<React.ComponentProps<"div">, "title"> {
  title: React.ReactNode
  icon?: LucideIcon
  hint?: React.ReactNode
  action?: React.ReactNode
}

/**
 * Shared card shell for every dashboard tile.
 * More generous padding than the orchestration cards — the dashboard
 * prioritises breathing room over density.
 */
export function DashboardCard({ title, icon: Icon, hint, action, className, children, ...rest }: DashboardCardProps) {
  return (
    <div
      className={cn(
        "rounded-xl border border-border/60 bg-card p-4",
        className,
      )}
      {...rest}
    >
      <div className="flex items-center justify-between mb-3.5">
        <div className="inline-flex items-center gap-1.5 text-[11px] font-semibold text-foreground/70 uppercase tracking-wider">
          {Icon && <Icon className="h-3.5 w-3.5 text-foreground/40" />}
          <span>{title}</span>
        </div>
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
