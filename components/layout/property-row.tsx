import * as React from "react"

import { cn } from "@/lib/utils"

interface PropertyRowProps extends React.ComponentProps<"div"> {
  label: React.ReactNode
  icon?: React.ElementType
  /** Optional right-aligned action slot (e.g., copy button, chevron). */
  action?: React.ReactNode
}

/**
 * PropertyRow — canonical label/value row for detail panels, settings forms,
 * and drawer content. Replaces hand-rolled `flex justify-between` pairs
 * scattered through issue detail, mission detail, and agent settings.
 *
 * Layout: fixed 120px label column, fluid value column, optional action slot.
 * Hairline bottom border (except last-child) for dense list feel.
 */
export function PropertyRow({
  label,
  icon: Icon,
  action,
  className,
  children,
  ...props
}: PropertyRowProps) {
  return (
    <div
      className={cn(
        "grid grid-cols-[120px_1fr_auto] gap-3 items-center py-2 text-body",
        "border-b border-border/40 last:border-0",
        className
      )}
      {...props}
    >
      <div className="flex items-center gap-2 text-label text-muted-foreground font-medium">
        {Icon && <Icon className="h-3.5 w-3.5" />}
        <span>{label}</span>
      </div>
      <div className="min-w-0 text-body text-foreground">{children}</div>
      {action && <div className="shrink-0">{action}</div>}
    </div>
  )
}
