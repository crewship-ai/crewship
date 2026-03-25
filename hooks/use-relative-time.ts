"use client"

import { useEffect, useState } from "react"
import { timeAgo } from "@/lib/time"

/**
 * Returns a live-updating relative time string (e.g., "5m ago").
 * Re-renders every `intervalMs` (default 60s) to keep the display fresh.
 */
export function useRelativeTime(dateStr: string | null | undefined, intervalMs = 60_000): string {
  const [, setTick] = useState(0)

  useEffect(() => {
    if (!dateStr || intervalMs <= 0) return
    const id = setInterval(() => setTick((t) => t + 1), intervalMs)
    return () => clearInterval(id)
  }, [dateStr, intervalMs])

  if (!dateStr) return ""
  return timeAgo(dateStr)
}
