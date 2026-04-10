import * as React from "react"

import { cn } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { STATUS_BADGE_CLASSES, STATUS_DOT_CLASSES } from "@/lib/colors"

interface StatusBadgeProps extends React.ComponentProps<typeof Badge> {
  status: string
  /** Optional override for the label — defaults to a humanized status string. */
  label?: React.ReactNode
  /** Show a leading colored dot. */
  withDot?: boolean
}

/**
 * StatusBadge — canonical pill for mission/task/agent/credential state.
 * Always routes through STATUS_BADGE_CLASSES so consumers never reinvent
 * the class map inline.
 */
export function StatusBadge({
  status,
  label,
  withDot = false,
  className,
  ...props
}: StatusBadgeProps) {
  const classes = STATUS_BADGE_CLASSES[status] ?? "bg-muted text-muted-foreground"
  const displayLabel = label ?? humanizeStatus(status)
  return (
    <Badge
      variant="outline"
      className={cn("border-transparent gap-1.5", classes, className)}
      {...props}
    >
      {withDot && <StatusDot status={status} className="h-1.5 w-1.5" />}
      {displayLabel}
    </Badge>
  )
}

interface StatusDotProps extends React.ComponentProps<"span"> {
  status: string
  /** Animate with a pulse — use for IN_PROGRESS / live indicators. */
  live?: boolean
}

/**
 * StatusDot — colored dot for inline status indicators (graph nodes,
 * sidebar rails, toolbar strips). Uses STATUS_DOT_CLASSES from lib/colors.
 */
export function StatusDot({ status, live = false, className, ...props }: StatusDotProps) {
  const dotClass = STATUS_DOT_CLASSES[status] ?? "bg-slate-400"
  return (
    <span
      className={cn(
        "inline-block h-2 w-2 rounded-full shrink-0",
        dotClass,
        live && "agent-active-dot",
        className
      )}
      aria-hidden="true"
      {...props}
    />
  )
}

function humanizeStatus(status: string): string {
  return status
    .toLowerCase()
    .split("_")
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(" ")
}
