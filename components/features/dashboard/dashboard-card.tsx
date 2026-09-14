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
 * Shared card shell for every dashboard tile. Compact by design: the
 * dashboard is scanned, not read, and the same eight tiles used to need two
 * screens (#2539).
 */
export function DashboardCard({ title, icon: Icon, hint, action, className, children, ...rest }: DashboardCardProps) {
  return (
    <div
      className={cn(
        "rounded-xl border border-border/60 bg-card p-3",
        className,
      )}
      {...rest}
    >
      <div className="mb-2.5 flex items-center justify-between">
        <div className="inline-flex items-center gap-1.5 text-[11px] font-semibold text-foreground/70 uppercase tracking-wider">
          {Icon && <Icon className="h-3.5 w-3.5 text-muted-foreground-soft" />}
          <span>{title}</span>
        </div>
        {(hint || action) && (
          <div className="flex items-center gap-2 text-[10px] font-mono text-muted-foreground">
            {hint}
            {action}
          </div>
        )}
      </div>
      {children}
    </div>
  )
}
