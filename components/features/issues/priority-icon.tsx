"use client"

import { cn } from "@/lib/utils"
import { PRIORITY_COLORS } from "@/lib/colors"
import type { IssuePriority } from "@/lib/types/mission"

export const priorityLabel: Record<IssuePriority, string> = {
  urgent: "Urgent",
  high: "High",
  medium: "Medium",
  low: "Low",
  none: "No priority",
}

export const priorityShortcut: Record<IssuePriority, string> = {
  none: "0",
  urgent: "1",
  high: "2",
  medium: "3",
  low: "4",
}

interface PriorityIconProps {
  priority: IssuePriority
  className?: string
}

export function PriorityIcon({ priority, className }: PriorityIconProps) {
  const size = "h-4 w-4"
  const cls = cn(size, "shrink-0", className)

  switch (priority) {
    case "urgent":
      return (
        <svg viewBox="0 0 16 16" className={cls}>
          <rect x="1" y="1" width="14" height="14" rx="2" fill={PRIORITY_COLORS.urgent} />
          <path
            d="M8 3.5v5M8 11v1"
            stroke="white"
            strokeWidth="1.5"
            strokeLinecap="round"
          />
        </svg>
      )
    case "high":
      return (
        <svg viewBox="0 0 16 16" className={cls}>
          <rect x="1.5" y="8" width="3" height="6" rx="1" fill={PRIORITY_COLORS.urgent} />
          <rect x="6.5" y="5" width="3" height="9" rx="1" fill={PRIORITY_COLORS.urgent} />
          <rect x="11.5" y="2" width="3" height="12" rx="1" fill={PRIORITY_COLORS.urgent} />
        </svg>
      )
    case "medium":
      return (
        <svg viewBox="0 0 16 16" className={cls}>
          <rect x="1.5" y="8" width="3" height="6" rx="1" fill={PRIORITY_COLORS.medium} />
          <rect x="6.5" y="5" width="3" height="9" rx="1" fill={PRIORITY_COLORS.medium} />
          <rect
            x="11.5"
            y="2"
            width="3"
            height="12"
            rx="1"
            fill={PRIORITY_COLORS.medium}
            opacity="0.2"
          />
        </svg>
      )
    case "low":
      return (
        <svg viewBox="0 0 16 16" className={cls}>
          <rect x="1.5" y="8" width="3" height="6" rx="1" fill={PRIORITY_COLORS.low} />
          <rect
            x="6.5"
            y="5"
            width="3"
            height="9"
            rx="1"
            fill={PRIORITY_COLORS.low}
            opacity="0.2"
          />
          <rect
            x="11.5"
            y="2"
            width="3"
            height="12"
            rx="1"
            fill={PRIORITY_COLORS.low}
            opacity="0.2"
          />
        </svg>
      )
    default:
      // No priority — three horizontal dashes
      return (
        <svg viewBox="0 0 16 16" className={cls}>
          <rect x="2" y="3.5" width="12" height="1.5" rx="0.5" fill="currentColor" opacity="0.4" />
          <rect x="2" y="7.25" width="12" height="1.5" rx="0.5" fill="currentColor" opacity="0.4" />
          <rect x="2" y="11" width="12" height="1.5" rx="0.5" fill="currentColor" opacity="0.4" />
        </svg>
      )
  }
}
