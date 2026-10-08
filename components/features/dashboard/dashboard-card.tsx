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
        "rounded-card border border-border bg-card p-4",
        className,
      )}
      {...rest}
    >
      <div className="mb-3 flex items-center justify-between gap-3">
        {/* Harbor eyebrow: mono, wide-tracked, in the brand ink. A long hint
            wraps; the title never breaks over two lines. */}
        <div className="eyebrow inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap">
          {Icon && <Icon className="h-3.5 w-3.5" />}
          <span>{title}</span>
        </div>
        {(hint || action) && (
          <div data-slot="dashboard-card-hint" className="flex min-w-0 items-center justify-end gap-2 text-right text-[11px] font-mono text-muted-foreground">
            {hint}
            {action}
          </div>
        )}
      </div>
      {children}
    </div>
  )
}
