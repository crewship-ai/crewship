"use client"

import { cn } from "@/lib/utils"
import type { MissionStatus } from "@/lib/types/mission"

export const statusLabel: Record<string, string> = {
  BACKLOG: "Backlog",
  TODO: "Todo",
  PLANNING: "Planning",
  IN_PROGRESS: "In Progress",
  REVIEW: "In Review",
  COMPLETED: "Done",
  DONE: "Done",
  FAILED: "Failed",
  CANCELLED: "Cancelled",
  DUPLICATE: "Duplicate",
}

export const statusColor: Record<string, string> = {
  BACKLOG: "#8C8C8C",
  TODO: "#8C8C8C",
  PLANNING: "#8C8C8C",
  IN_PROGRESS: "#F2C94C",
  REVIEW: "#F2994A",
  COMPLETED: "#5E6AD2",
  DONE: "#5E6AD2",
  FAILED: "#EF4444",
  CANCELLED: "#95959F",
  DUPLICATE: "#95959F",
}

interface StatusIconProps {
  status: MissionStatus | string
  className?: string
}

export function StatusIcon({ status, className }: StatusIconProps) {
  const size = "h-4 w-4"
  const cls = cn(size, "shrink-0", className)
  const color = statusColor[status] || "#8C8C8C"

  switch (status) {
    case "BACKLOG":
      // Dashed circle
      return (
        <svg viewBox="0 0 16 16" className={cls}>
          <circle
            cx="8"
            cy="8"
            r="5.5"
            stroke={color}
            strokeWidth="1.5"
            strokeDasharray="3 2"
            fill="none"
          />
        </svg>
      )

    case "TODO":
    case "PLANNING":
      // Empty solid circle
      return (
        <svg viewBox="0 0 16 16" className={cls}>
          <circle
            cx="8"
            cy="8"
            r="5.5"
            stroke={color}
            strokeWidth="1.5"
            fill="none"
          />
        </svg>
      )

    case "IN_PROGRESS":
      // Half-filled circle (left half filled)
      return (
        <svg viewBox="0 0 16 16" className={cls}>
          <circle
            cx="8"
            cy="8"
            r="5.5"
            stroke={color}
            strokeWidth="1.5"
            fill="none"
          />
          <path d="M8 2.5A5.5 5.5 0 0 0 8 13.5V2.5z" fill={color} />
        </svg>
      )

    case "REVIEW":
      // Three-quarter filled circle
      return (
        <svg viewBox="0 0 16 16" className={cls}>
          <circle
            cx="8"
            cy="8"
            r="5.5"
            stroke={color}
            strokeWidth="1.5"
            fill="none"
          />
          <path
            d="M8 2.5A5.5 5.5 0 0 0 8 13.5V2.5z"
            fill={color}
          />
          <path
            d="M8 2.5A5.5 5.5 0 0 1 13.5 8H8V2.5z"
            fill={color}
          />
        </svg>
      )

    case "COMPLETED":
    case "DONE":
      // Filled circle with checkmark
      return (
        <svg viewBox="0 0 16 16" className={cls}>
          <circle cx="8" cy="8" r="6" fill={color} />
          <path
            d="M5 8.5l2 2 4-4.5"
            stroke="white"
            strokeWidth="1.5"
            strokeLinecap="round"
            strokeLinejoin="round"
            fill="none"
          />
        </svg>
      )

    case "FAILED":
      // Filled red circle with exclamation
      return (
        <svg viewBox="0 0 16 16" className={cls}>
          <circle cx="8" cy="8" r="6" fill={color} />
          <path
            d="M8 4.5v4M8 11v1"
            stroke="white"
            strokeWidth="1.5"
            strokeLinecap="round"
          />
        </svg>
      )

    case "CANCELLED":
    case "DUPLICATE":
      // Filled gray circle with X
      return (
        <svg viewBox="0 0 16 16" className={cls}>
          <circle cx="8" cy="8" r="6" fill={color} />
          <path
            d="M5.5 5.5l5 5M10.5 5.5l-5 5"
            stroke="white"
            strokeWidth="1.5"
            strokeLinecap="round"
          />
        </svg>
      )

    default:
      return (
        <svg viewBox="0 0 16 16" className={cls}>
          <circle
            cx="8"
            cy="8"
            r="5.5"
            stroke="#8C8C8C"
            strokeWidth="1.5"
            fill="none"
          />
        </svg>
      )
  }
}
