"use client"

import * as React from "react"
import { useCallback, useRef, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { cn } from "@/lib/utils"

type FeedKind = "run" | "task" | "mission" | "issue" | "escalation"

interface FeedEntry {
  id: string
  ts: number
  kind: FeedKind
  label: string
  tag: string
  title: string
  actor?: string
}

const MAX_ENTRIES = 30

const TAG_STYLES: Record<string, string> = {
  IN_PROGRESS: "text-blue-400 border-blue-500/30 bg-blue-500/10",
  RUNNING: "text-blue-400 border-blue-500/30 bg-blue-500/10",
  COMPLETED: "text-emerald-400 border-emerald-500/30 bg-emerald-500/10",
  DONE: "text-emerald-400 border-emerald-500/30 bg-emerald-500/10",
  REVIEW: "text-amber-400 border-amber-500/30 bg-amber-500/10",
  REVIEWING: "text-amber-400 border-amber-500/30 bg-amber-500/10",
  FAILED: "text-red-400 border-red-500/30 bg-red-500/10",
  ERROR: "text-red-400 border-red-500/30 bg-red-500/10",
  ESCALATED: "text-red-400 border-red-500/30 bg-red-500/10",
  CANCELLED: "text-muted-foreground border-border bg-muted/20",
  STARTED: "text-blue-400 border-blue-500/30 bg-blue-500/10",
}

function tagStyle(label: string): string {
  return TAG_STYLES[label] ?? "text-muted-foreground border-border bg-muted/20"
}

function formatTime(ts: number): string {
  const d = new Date(ts)
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}:${String(d.getSeconds()).padStart(2, "0")}`
}

/**
 * Live streaming feed of workspace activity. Fed by the shared WebSocket
 * realtime layer; subscribes to mission/task/run/issue/escalation events
 * and keeps the last 30 frames visible.
 */
export function ActivityFeed() {
  const [entries, setEntries] = useState<FeedEntry[]>([])
  const counterRef = useRef(0)

  const push = useCallback((e: Omit<FeedEntry, "id" | "ts">) => {
    setEntries((prev) => {
      const next: FeedEntry = { ...e, id: `${Date.now()}-${counterRef.current++}`, ts: Date.now() }
      const result = [next, ...prev]
      if (result.length > MAX_ENTRIES) result.length = MAX_ENTRIES
      return result
    })
  }, [])

  useRealtimeEvent("run.started", useCallback((ev: RealtimeEvent) => {
    const p = ev.payload
    push({
      kind: "run",
      label: "STARTED",
      tag: (p.agent_slug ?? p.agent_id ?? "run") as string,
      title: `Run started${p.agent_name ? ` by ${p.agent_name}` : ""}`,
      actor: p.agent_slug as string | undefined,
    })
  }, [push]))

  useRealtimeEvent("run.completed", useCallback((ev: RealtimeEvent) => {
    const p = ev.payload
    push({
      kind: "run",
      label: "COMPLETED",
      tag: (p.agent_slug ?? p.agent_id ?? "run") as string,
      title: `Run completed${p.duration_ms ? ` in ${Math.round(Number(p.duration_ms) / 1000)}s` : ""}`,
      actor: p.agent_slug as string | undefined,
    })
  }, [push]))

  useRealtimeEvent("run.failed", useCallback((ev: RealtimeEvent) => {
    const p = ev.payload
    push({
      kind: "run",
      label: "FAILED",
      tag: (p.agent_slug ?? p.agent_id ?? "run") as string,
      title: (p.error_message as string) || "Run failed",
      actor: p.agent_slug as string | undefined,
    })
  }, [push]))

  useRealtimeEvent("task.updated", useCallback((ev: RealtimeEvent) => {
    const p = ev.payload
    const status = (p.status ?? "") as string
    if (!status || !p.id) return
    push({
      kind: "task",
      label: status,
      tag: (p.mission_identifier ?? (p.mission_id as string | undefined)?.slice(0, 6) ?? "task") as string,
      title: (p.title as string) || `Task ${p.id}`,
      actor: p.agent_slug as string | undefined,
    })
  }, [push]))

  useRealtimeEvent("mission.updated", useCallback((ev: RealtimeEvent) => {
    const p = ev.payload
    const status = (p.status ?? "") as string
    if (!status) return
    push({
      kind: "mission",
      label: status,
      tag: (p.identifier ?? (p.id as string | undefined)?.slice(0, 6) ?? "mission") as string,
      title: (p.title as string) || "Mission update",
    })
  }, [push]))

  useRealtimeEvent("escalation.created", useCallback((ev: RealtimeEvent) => {
    const p = ev.payload
    push({
      kind: "escalation",
      label: "ESCALATED",
      tag: (p.from_slug ?? "esc") as string,
      title: (p.reason as string) || "Escalation raised",
      actor: p.from_slug as string | undefined,
    })
  }, [push]))

  if (entries.length === 0) {
    return (
      <div className="flex items-center justify-center h-[240px] text-[11px] text-muted-foreground/50">
        Waiting for activity…
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-0.5 max-h-[300px] overflow-y-auto pr-1 -mr-1">
      <AnimatePresence initial={false}>
        {entries.map((e) => (
          <motion.div
            key={e.id}
            layout
            initial={{ opacity: 0, y: -6 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0 }}
            transition={{ type: "spring", stiffness: 380, damping: 32 }}
            className="flex items-center gap-2 py-1 px-1 rounded hover:bg-white/[0.02]"
          >
            <span className="text-[10px] font-mono text-muted-foreground/40 w-[56px] shrink-0">{formatTime(e.ts)}</span>
            <span className={cn("text-[9px] font-semibold uppercase tracking-wide px-1.5 py-0.5 rounded border shrink-0", tagStyle(e.label))}>
              {e.label}
            </span>
            <span className="text-[10px] font-mono text-muted-foreground shrink-0 w-[56px] truncate">{e.tag}</span>
            <span className="text-[11px] text-foreground/80 flex-1 truncate">{e.title}</span>
            {e.actor && (
              <span className="text-[10px] text-muted-foreground/60 shrink-0">@{e.actor}</span>
            )}
          </motion.div>
        ))}
      </AnimatePresence>
    </div>
  )
}
