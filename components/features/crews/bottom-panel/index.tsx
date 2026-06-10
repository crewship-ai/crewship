"use client"

import { useEffect, useRef, useState } from "react"
import dynamic from "next/dynamic"
import {
  ChevronDown, ChevronUp, Container, FileCode2, Files,
  MessageSquare, Terminal,
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

const TABS: Array<{ id: BottomTab; label: string; icon: typeof MessageSquare; soon?: boolean }> = [
  { id: "messages", label: "Messages", icon: MessageSquare },
  { id: "exec", label: "Exec Log", icon: Terminal },
  { id: "yaml", label: "YAML", icon: FileCode2 },
  { id: "docker", label: "Docker", icon: Container },
  { id: "files", label: "Files", icon: Files },
  { id: "terminal", label: "Terminal", icon: Terminal },
]

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
  initialTab = "messages",
  initialOpen = false,
  onOpenChange,
}: BottomPanelProps) {
  const [tab, setTab] = useState<BottomTab>(initialTab)
  const [open, setOpen] = useState(initialOpen)
  const [height, setHeight] = useUserPreference<number>(
    "crews.bottomPanel.height",
    PANEL_HEIGHT_DEFAULT,
  )
  const dragRef = useRef<{ startY: number; startH: number } | null>(null)
  const [dragging, setDragging] = useState(false)

  useEffect(() => {
    setTab(initialTab)
    setOpen(initialOpen)
  }, [initialTab, initialOpen])

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

  const handleTab = (next: BottomTab, soon?: boolean) => {
    if (soon) return
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

      <div className="h-9 shrink-0 flex items-center gap-1 px-2 text-xs">
        {TABS.map((t) => {
          const Icon = t.icon
          const active = tab === t.id && open
          return (
            <button
              key={t.id}
              type="button"
              onClick={() => handleTab(t.id, t.soon)}
              disabled={t.soon}
              className={cn(
                "px-2.5 py-1 rounded flex items-center gap-1.5 transition-colors",
                active && "bg-white/[0.06] text-foreground",
                !active && !t.soon && "text-muted-foreground hover:bg-white/5",
                t.soon && "text-muted-foreground-soft cursor-not-allowed",
              )}
            >
              <Icon className="h-3 w-3" />
              {t.label}
              {t.soon && (
                <span className="text-[9px] px-1.5 py-0.5 rounded bg-zinc-800 text-muted-foreground border border-white/10">
                  soon
                </span>
              )}
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
          {tab === "files" && <FilesTab workspaceId={workspaceId} context={context} />}
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
