"use client"

import { useEffect, useRef, useState } from "react"
import dynamic from "next/dynamic"
import {
  Activity, CalendarClock, ChevronDown, ChevronUp, Container, FileCode2,
  Files, GitCompareArrows, MessageCircle, MessageSquare, Play, ScrollText,
  Terminal, Workflow,
} from "lucide-react"
import { cn } from "@/lib/utils"

import { useUserPreference } from "@/hooks/use-user-preference"

import type { BottomPanelProps, BottomTab } from "./types"
import { EmptyState } from "./shared"
import { MessagesTab } from "./messages-tab"
import { ExecTab } from "./exec-tab"
import { YamlTab } from "./yaml-tab"
import { DockerTab } from "./docker-tab"
import { FilesTab } from "./files-tab"
import { ActivityTab } from "./activity-tab"
import { RunsTab } from "./runs-tab"
import { CommentsTab } from "./comments-tab"
import { ChangesTab } from "./changes-tab"
import { ScheduleTab } from "./schedule-tab"
import { LogsTab } from "./logs-tab"
import { TraceTab } from "./trace-tab"

export type { BottomTab, BottomPanelProps } from "./types"

// Terminal tab is heavy (xterm.js + addons + CSS) — load only when the
// user opens the tab, not eagerly on canvas mount. ssr:false because
// xterm needs the real DOM.
const BottomPanelTerminal = dynamic(
  () => import("@/components/features/crews/bottom-panel-terminal").then((m) => m.BottomPanelTerminal),
  {
    ssr: false,
    loading: () => (
      <div className="h-full grid place-items-center text-xs text-muted-foreground">
        Loading terminal…
      </div>
    ),
  },
)

// Registry of every tab the dock can render. A page selects which subset
// to show via BottomPanelProps.tabs; the entries here are the single source
// of truth for label + icon so all pages stay visually consistent.
type TabMeta = { label: string; icon: typeof MessageSquare }
const TAB_META: Record<BottomTab, TabMeta> = {
  // crew / agent
  messages: { label: "Messages", icon: MessageSquare },
  exec: { label: "Exec Log", icon: Terminal },
  yaml: { label: "YAML", icon: FileCode2 },
  docker: { label: "Docker", icon: Container },
  files: { label: "Files", icon: Files },
  terminal: { label: "Terminal", icon: Terminal },
  // issue / mission
  activity: { label: "Activity", icon: Activity },
  runs: { label: "Runs", icon: Play },
  changes: { label: "Changes", icon: GitCompareArrows },
  comments: { label: "Comments", icon: MessageCircle },
  // routine
  schedule: { label: "Schedule", icon: CalendarClock },
  // run / activity
  logs: { label: "Logs", icon: ScrollText },
  trace: { label: "Trace", icon: Workflow },
}

// Default tab set — the original crew/agent dock. Pages that don't pass a
// `tabs` prop (i.e. the Crews page) get exactly this, unchanged.
const DEFAULT_TABS: BottomTab[] = ["messages", "exec", "yaml", "docker", "files", "terminal"]

// Sensible bounds for the resize gesture. The min keeps something
// useful visible (headers + a few rows); the max stops the panel
// from eating the whole viewport on a tall monitor. 320 is the same
// default the panel used before being resizable.
const PANEL_HEIGHT_MIN = 160
const PANEL_HEIGHT_MAX = 900
const PANEL_HEIGHT_DEFAULT = 320

/**
 * Collapsible bottom panel matching /orchestration's drawer pattern.
 * Six tabs: Messages, Exec Log, YAML, Docker, Files, Terminal.
 * Default: collapsed (36 px). Click any tab → expand to 320 px.
 *
 * Tab content is selection-aware: agent-scoped when an agent is
 * selected, crew-scoped when a crew is selected, workspace-wide
 * otherwise. Each tab lives in its own file under ./*-tab.tsx — this
 * component is the chrome (tab strip, resize handle, expand toggle)
 * + the router that picks which tab to render.
 */
export function BottomPanel({
  workspaceId,
  context,
  tabs,
  initialTab,
  initialOpen = false,
  onOpenChange,
}: BottomPanelProps) {
  const tabIds = tabs && tabs.length > 0 ? tabs : DEFAULT_TABS
  // Fall back to the first tab in the page's own set rather than a hardcoded
  // "messages" (which a non-crew page may not even show).
  const firstTab = initialTab && tabIds.includes(initialTab) ? initialTab : tabIds[0]
  const [tab, setTab] = useState<BottomTab>(firstTab)
  const [open, setOpen] = useState(initialOpen)
  const [height, setHeight] = useUserPreference<number>(
    "crews.bottomPanel.height",
    PANEL_HEIGHT_DEFAULT,
  )
  const dragRef = useRef<{ startY: number; startH: number } | null>(null)
  const [dragging, setDragging] = useState(false)

  useEffect(() => {
    setTab(firstTab)
    setOpen(initialOpen)
  }, [firstTab, initialOpen])

  useEffect(() => { onOpenChange?.(open) }, [open, onOpenChange])

  // Mouse-driven resize. We track on document so the gesture survives
  // even if the cursor leaves the handle hitbox (typical desktop drag
  // behaviour). Touchstart hooks the same flow so tablets get it too.
  useEffect(() => {
    if (!dragging) return
    const onMove = (clientY: number) => {
      if (!dragRef.current) return
      const delta = dragRef.current.startY - clientY
      const next = Math.min(
        PANEL_HEIGHT_MAX,
        Math.max(PANEL_HEIGHT_MIN, dragRef.current.startH + delta),
      )
      setHeight(next)
    }
    const onMouseMove = (e: MouseEvent) => onMove(e.clientY)
    const onTouchMove = (e: TouchEvent) => {
      if (e.touches.length > 0) onMove(e.touches[0].clientY)
    }
    const onUp = () => {
      dragRef.current = null
      setDragging(false)
      document.body.style.userSelect = ""
      document.body.style.cursor = ""
    }
    document.addEventListener("mousemove", onMouseMove)
    document.addEventListener("mouseup", onUp)
    document.addEventListener("touchmove", onTouchMove, { passive: true })
    document.addEventListener("touchend", onUp)
    return () => {
      document.removeEventListener("mousemove", onMouseMove)
      document.removeEventListener("mouseup", onUp)
      document.removeEventListener("touchmove", onTouchMove)
      document.removeEventListener("touchend", onUp)
    }
  }, [dragging, setHeight])

  const startDrag = (clientY: number) => {
    if (!open) return
    dragRef.current = { startY: clientY, startH: height }
    setDragging(true)
    document.body.style.userSelect = "none"
    document.body.style.cursor = "ns-resize"
  }

  const handleTab = (next: BottomTab) => {
    setTab(next)
    setOpen(true)
  }

  return (
    <div
      className={cn(
        "shrink-0 border-t border-white/8 bg-card flex flex-col relative",
        // Disable height transitions during a drag so the gesture
        // tracks the cursor 1:1 instead of lerping behind it.
        !dragging && "transition-[height] duration-200",
      )}
      style={{ height: open ? `${height}px` : "36px" }}
    >
      {/* Resize handle — sits at the very top edge, hovers a thin grab
          target. Pointer-events only when the panel is open (it'd be
          confusing to drag a collapsed strip). */}
      {open && (
        <div
          role="separator"
          aria-orientation="horizontal"
          aria-label="Resize bottom panel"
          aria-valuenow={height}
          aria-valuemin={PANEL_HEIGHT_MIN}
          aria-valuemax={PANEL_HEIGHT_MAX}
          tabIndex={0}
          onMouseDown={(e) => {
            e.preventDefault()
            startDrag(e.clientY)
          }}
          onTouchStart={(e) => {
            if (e.touches.length > 0) startDrag(e.touches[0].clientY)
          }}
          onKeyDown={(e) => {
            // Keyboard nudge for accessibility — 16 px steps with
            // arrow keys, 64 px with PageUp/Down.
            const step = e.key === "PageUp" || e.key === "PageDown" ? 64 : 16
            if (e.key === "ArrowUp" || e.key === "PageUp") {
              e.preventDefault()
              setHeight(Math.min(PANEL_HEIGHT_MAX, height + step))
            } else if (e.key === "ArrowDown" || e.key === "PageDown") {
              e.preventDefault()
              setHeight(Math.max(PANEL_HEIGHT_MIN, height - step))
            }
          }}
          className={cn(
            "absolute -top-[3px] left-0 right-0 h-[6px] z-10 cursor-ns-resize",
            "before:absolute before:inset-x-0 before:top-[2px] before:h-[2px] before:transition-colors",
            dragging
              ? "before:bg-blue-400/50"
              : "before:bg-transparent hover:before:bg-blue-400/30",
          )}
        />
      )}

      <div role="tablist" aria-label="Bottom panel" className="h-9 shrink-0 flex items-center gap-1 px-2 text-xs overflow-x-auto">
        {tabIds.map((id) => {
          const meta = TAB_META[id]
          const Icon = meta.icon
          const active = tab === id && open
          return (
            <button
              key={id}
              type="button"
              role="tab"
              aria-selected={active}
              onClick={() => handleTab(id)}
              className={cn(
                "px-2.5 py-1 rounded flex items-center gap-1.5 transition-colors whitespace-nowrap",
                active && "bg-white/[0.06] text-foreground",
                !active && "text-muted-foreground hover:bg-white/5",
              )}
            >
              <Icon className="h-3 w-3" />
              {meta.label}
            </button>
          )
        })}
        <div className="ml-auto flex items-center gap-2">
          <button
            type="button"
            onClick={() => setOpen(!open)}
            className="p-1 rounded hover:bg-white/5 text-muted-foreground"
            title={open ? "Collapse" : "Expand"}
          >
            {open ? <ChevronDown className="h-3 w-3" /> : <ChevronUp className="h-3 w-3" />}
          </button>
        </div>
      </div>

      {open && (
        <div className="flex-1 min-h-0 overflow-hidden border-t border-white/5">
          {tab === "messages" && <MessagesTab workspaceId={workspaceId} context={context} />}
          {tab === "exec" && <ExecTab workspaceId={workspaceId} context={context} />}
          {tab === "yaml" && <YamlTab workspaceId={workspaceId} context={context} />}
          {tab === "docker" && <DockerTab />}
          {tab === "files" && (context === null || context.kind === "agent" || context.kind === "crew") && (
            <FilesTab workspaceId={workspaceId} context={context} />
          )}
          {tab === "activity" && <ActivityTab workspaceId={workspaceId} context={context} />}
          {tab === "runs" && <RunsTab workspaceId={workspaceId} context={context} />}
          {tab === "changes" && <ChangesTab workspaceId={workspaceId} context={context} />}
          {tab === "comments" && <CommentsTab workspaceId={workspaceId} context={context} />}
          {tab === "schedule" && <ScheduleTab workspaceId={workspaceId} context={context} />}
          {tab === "logs" && <LogsTab workspaceId={workspaceId} context={context} />}
          {tab === "trace" && <TraceTab workspaceId={workspaceId} context={context} />}
          {tab === "terminal" && (
            context?.kind === "agent" && context.crewId && context.crewSlug ? (
              <BottomPanelTerminal
                agentName={context.agentName}
                agentSlug={context.agentSlug}
                crewId={context.crewId}
                crewSlug={context.crewSlug}
              />
            ) : (
              <EmptyState>
                {context?.kind === "agent"
                  ? "Agent has no crew assigned — terminal needs a crew container."
                  : "Select an agent to open a shell."}
              </EmptyState>
            )
          )}
        </div>
      )}
    </div>
  )
}
