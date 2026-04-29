"use client"

import { motion } from "motion/react"

import { cn } from "@/lib/utils"

export type PresenceStatus = "online" | "busy" | "blocked" | "offline"

const COLORS: Record<PresenceStatus, string> = {
  online: "bg-emerald-500",
  busy: "bg-amber-500",
  blocked: "bg-red-500",
  offline: "bg-muted-foreground/30",
}

const LABELS: Record<PresenceStatus, string> = {
  online: "Online",
  busy: "Busy",
  blocked: "Blocked",
  offline: "Offline",
}

interface PresenceDotProps {
  status: PresenceStatus
  className?: string
  pulse?: boolean
}

export function PresenceDot({ status, className, pulse = true }: PresenceDotProps) {
  return (
    <span
      className={cn("relative inline-flex h-2.5 w-2.5", className)}
      aria-label={LABELS[status]}
      title={LABELS[status]}
    >
      {pulse && status !== "offline" && (
        <motion.span
          className={cn(
            "absolute inset-0 rounded-full opacity-60",
            COLORS[status],
          )}
          animate={{ scale: [1, 1.8], opacity: [0.6, 0] }}
          transition={{ duration: 2, repeat: Infinity }}
        />
      )}
      <span
        className={cn(
          "relative inline-flex h-2.5 w-2.5 rounded-full ring-2 ring-background",
          COLORS[status],
        )}
      />
    </span>
  )
}
