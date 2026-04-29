"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import dynamic from "next/dynamic"
import { getEditorLanguage } from "@/components/features/chat/chat-tree-row"
import { useUserPreference } from "@/hooks/use-user-preference"

const FileEditor = dynamic(
  () => import("@/components/features/files/file-editor").then((m) => m.FileEditor),
  {
    ssr: false,
    loading: () => (
      <div className="h-full grid place-items-center text-xs text-muted-foreground">
        Loading editor…
      </div>
    ),
  },
)
import {
  ChevronDown, ChevronUp, Container, File, FileCode2, Files, Folder,
  MessageSquare, Terminal, Pencil, Save, Loader2,
} from "lucide-react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"

type BottomTab = "messages" | "exec" | "yaml" | "docker" | "files" | "terminal"

const TABS: Array<{ id: BottomTab; label: string; icon: typeof MessageSquare; soon?: boolean }> = [
  { id: "messages", label: "Messages", icon: MessageSquare },
  { id: "exec", label: "Exec Log", icon: Terminal },
  { id: "yaml", label: "YAML", icon: FileCode2 },
  { id: "docker", label: "Docker", icon: Container },
  { id: "files", label: "Files", icon: Files },
  { id: "terminal", label: "Terminal", icon: Terminal, soon: true },
]

interface ContainerStatus {
  name: string
  image: string
  status: string
  cpu_percent?: number | null
  memory_mb?: number | null
  agent_count?: number | null
}

// Real API shape from internal/api/agent_inbox.go (verified 2026-04-28):
// peer_messages: { id, from_agent_name, from_agent_slug, to_agent_name?,
//                  question, status, created_at, direction }
// escalations are NOT in the response — only escalations_open (count).
interface PeerMessage {
  id: string
  from_agent_name: string
  from_agent_slug: string
  to_agent_name?: string | null
  question: string
  status: string
  created_at: string
  direction: "incoming" | "outgoing"
}

interface FileEntry {
  name: string
  /** Full storage-rooted path returned by the list endpoint —
   *  `<crewID>/<slug>/<rest>`. Use this verbatim when issuing
   *  download / save / subdir queries; the IPC layer expects the
   *  full path (prefix-checks against crewID). */
  path?: string
  size?: number
  is_dir?: boolean
  modified?: string
  mod_time?: string
}

export interface BottomPanelProps {
  workspaceId: string
  /** Currently selected entity context. Null when no selection — panel
   *  shows workspace-wide data. */
  context: { kind: "agent"; agentId: string; agentSlug: string; agentName: string } | { kind: "crew"; crewId: string; crewSlug: string } | null
  /** Optional initial tab + open state — lets parent (e.g. crew Files
   *  button) jump directly to a tab and expand. */
  initialTab?: BottomTab
  initialOpen?: boolean
  /** Notified when panel open state changes so parent can persist if desired. */
  onOpenChange?: (open: boolean) => void
}

/**
 * Collapsible bottom panel matching /orchestration's drawer pattern.
 * Six tabs: Messages, Exec Log, YAML, Docker, Files, Terminal (soon).
 * Default: collapsed (36 px). Click any tab → expand to 320 px.
 *
 * Tab content is selection-aware: agent-scoped when an agent is
 * selected, crew-scoped when a crew is selected, workspace-wide
 * otherwise.
 */
// Sensible bounds for the resize gesture. The min keeps something
// useful visible (headers + a few rows); the max stops the panel
// from eating the whole viewport on a tall monitor. 320 is the same
// default the panel used before being resizable.
const PANEL_HEIGHT_MIN = 160
const PANEL_HEIGHT_MAX = 900
const PANEL_HEIGHT_DEFAULT = 320

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
                t.soon && "text-muted-foreground/50 cursor-not-allowed",
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
        </div>
      )}
    </div>
  )
}

// =============================================================================
// Tab content
// =============================================================================

function EmptyState({ children }: { children: React.ReactNode }) {
  return (
    <div className="h-full flex items-center justify-center text-xs text-muted-foreground p-4 text-center">
      {children}
    </div>
  )
}

function formatTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })
}

/**
 * Messages — pulls peer messages from the agent inbox. The inbox response
 * also includes escalation/assignment/approval COUNTS (not arrays); those
 * surface in the canvas Inbox banner instead of here.
 */
function MessagesTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelProps["context"] }) {
  const [messages, setMessages] = useState<PeerMessage[] | null>(null)
  const [counters, setCounters] = useState<{ escalations: number; assignments: number; approvals: number } | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!context || context.kind !== "agent") return
    let cancelled = false
    setMessages(null)
    setCounters(null)
    setError(null)
    fetch(`/api/v1/agents/${context.agentId}/inbox?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data) => {
        if (cancelled) return
        setMessages(Array.isArray(data?.peer_messages) ? data.peer_messages : [])
        setCounters({
          escalations: Number(data?.escalations_open ?? 0),
          assignments: Number(data?.assignments_open ?? 0),
          approvals: Number(data?.approvals_pending ?? 0),
        })
      })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [context, workspaceId])

  if (!context) return <EmptyState>Select an agent to see its inbox messages.</EmptyState>
  if (context.kind !== "agent") return <EmptyState>Messages are per-agent — select one in the explorer.</EmptyState>
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (messages === null || counters === null) return <EmptyState>Loading…</EmptyState>

  const totalCounters = counters.escalations + counters.assignments + counters.approvals
  if (messages.length === 0 && totalCounters === 0) {
    return <EmptyState>No messages or escalations for {context.agentName}.</EmptyState>
  }

  return (
    <div className="h-full overflow-y-auto p-3 space-y-1.5 text-xs">
      {totalCounters > 0 && (
        <div className="rounded border border-amber-500/30 bg-amber-500/5 px-3 py-2 flex items-center gap-2">
          <span className="text-amber-300 font-medium">Pending:</span>
          {counters.escalations > 0 && <span className="text-amber-200">{counters.escalations} escalation{counters.escalations === 1 ? "" : "s"}</span>}
          {counters.assignments > 0 && <span className="text-amber-200">{counters.assignments} assignment{counters.assignments === 1 ? "" : "s"}</span>}
          {counters.approvals > 0 && <span className="text-amber-200">{counters.approvals} approval{counters.approvals === 1 ? "" : "s"}</span>}
        </div>
      )}
      {messages.map((m) => (
        <div key={m.id} className="rounded border border-white/10 bg-zinc-900/40 px-3 py-2">
          <div className="flex items-center justify-between mb-0.5">
            <span className="text-blue-300 font-medium">
              {m.direction === "outgoing" ? "→" : "←"} {m.from_agent_name}
            </span>
            <span className="text-[10px] text-muted-foreground">{formatTime(m.created_at)}</span>
          </div>
          <div className="text-foreground/85 whitespace-pre-wrap">{m.question}</div>
        </div>
      ))}
    </div>
  )
}

interface LogEntry {
  // The actual shape is sidecar-defined; we render whatever string fields
  // we recognise. Most rows will have at minimum a timestamp + message.
  ts?: string
  timestamp?: string
  level?: string
  message?: string
  msg?: string
  text?: string
  [k: string]: unknown
}

/**
 * Exec Log — proxy returns a JSON ARRAY of log entries (verified
 * 2026-04-28 in internal/api/proxy.go AgentLogs). No `tail=` param;
 * proxy uses `limit/offset`, default 100. We render whatever recognisable
 * timestamp + message pair each row has.
 */
function ExecTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelProps["context"] }) {
  const [logs, setLogs] = useState<LogEntry[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!context || context.kind !== "agent") return
    let cancelled = false
    setLogs(null)
    setError(null)
    fetch(`/api/v1/agents/${context.agentId}/logs?workspace_id=${workspaceId}&limit=200`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data) => {
        if (cancelled) return
        setLogs(Array.isArray(data) ? data : [])
      })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [context, workspaceId])

  if (!context) return <EmptyState>Select an agent to see its exec log.</EmptyState>
  if (context.kind !== "agent") return <EmptyState>Exec logs are per-agent — select one in the explorer.</EmptyState>
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (logs === null) return <EmptyState>Loading…</EmptyState>
  if (logs.length === 0) return <EmptyState>No log output yet for {context.agentName}.</EmptyState>

  return (
    <div className="h-full overflow-y-auto p-3 text-[11px] leading-relaxed font-mono text-foreground/80">
      {logs.map((l, i) => {
        const ts = l.ts ?? l.timestamp ?? ""
        const msg = l.message ?? l.msg ?? l.text ?? JSON.stringify(l)
        const level = String(l.level ?? "").toLowerCase()
        const levelColor =
          level.includes("error") || level.includes("fatal") ? "text-red-300" :
          level.includes("warn") ? "text-amber-300" :
          level.includes("info") ? "text-blue-300" :
          "text-muted-foreground"
        return (
          <div key={i} className="flex gap-2 hover:bg-white/[0.03] px-1 -mx-1 rounded">
            {ts && <span className="text-muted-foreground shrink-0">{formatTime(String(ts))}</span>}
            {level && <span className={cn("shrink-0 uppercase", levelColor)}>{level}</span>}
            <span className="break-all">{String(msg)}</span>
          </div>
        )
      })}
    </div>
  )
}

/**
 * YAML — fetches the entity's record and renders a read-only YAML-ish
 * projection of the user-relevant fields. Not full YAML — agents/crews
 * have many fields that are noisy (timestamps, _count, container hashes).
 */
function YamlTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelProps["context"] }) {
  const [data, setData] = useState<Record<string, unknown> | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!context) return
    let cancelled = false
    setData(null)
    setError(null)
    const url = context.kind === "agent"
      ? `/api/v1/agents/${context.agentId}?workspace_id=${workspaceId}`
      : `/api/v1/crews/${context.crewId}?workspace_id=${workspaceId}`
    fetch(url)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((rec) => { if (!cancelled) setData(rec) })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [context, workspaceId])

  if (!context) return <EmptyState>Select an agent or crew to see its YAML config.</EmptyState>
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (data === null) return <EmptyState>Loading…</EmptyState>

  const yaml = toYaml(filterNoise(data))

  return (
    <pre className="h-full overflow-y-auto p-3 text-[11px] leading-relaxed font-mono text-foreground/80 whitespace-pre">
      {yaml}
    </pre>
  )
}

function DockerTab() {
  const [containers, setContainers] = useState<ContainerStatus[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    let cancelled = false
    fetch("/api/v1/system/runtime")
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then((data) => {
        if (cancelled) return
        const list: ContainerStatus[] = Array.isArray(data?.containers) ? data.containers : []
        setContainers(list)
      })
      .catch((err) => {
        if (cancelled) return
        setError(err instanceof Error ? err.message : String(err))
        setContainers([])
      })
    return () => { cancelled = true }
  }, [])

  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (containers === null) return <EmptyState>Loading container status…</EmptyState>
  if (containers.length === 0) return <EmptyState>No containers running.</EmptyState>

  return (
    <div className="h-full overflow-y-auto">
      <div className="grid grid-cols-[1fr_180px_120px_80px_80px_70px] gap-3 px-4 py-2 border-b border-white/8 text-[10px] uppercase tracking-wide text-muted-foreground">
        <span>Container</span>
        <span>Image</span>
        <span>Status</span>
        <span>CPU</span>
        <span>RAM</span>
        <span>Agents</span>
      </div>
      <div className="divide-y divide-white/5 text-sm">
        {containers.map((c) => (
          <div
            key={c.name}
            className="grid grid-cols-[1fr_180px_120px_80px_80px_70px] gap-3 px-4 py-2 items-center"
          >
            <span className="flex items-center gap-2">
              <span
                className={cn(
                  "w-1.5 h-1.5 rounded-full",
                  c.status?.toLowerCase().includes("running") ? "bg-emerald-400" : "bg-zinc-500",
                )}
              />
              <code className="text-xs">{c.name}</code>
            </span>
            <code className="text-xs text-muted-foreground">{c.image}</code>
            <span
              className={cn(
                "text-xs",
                c.status?.toLowerCase().includes("running") ? "text-emerald-400" : "text-muted-foreground",
              )}
            >
              {c.status}
            </span>
            <span className="text-xs">
              {c.cpu_percent !== null && c.cpu_percent !== undefined ? `${c.cpu_percent}%` : "—"}
            </span>
            <span className="text-xs">
              {c.memory_mb !== null && c.memory_mb !== undefined ? `${c.memory_mb} MB` : "—"}
            </span>
            <span className="text-xs">{c.agent_count ?? "—"}</span>
          </div>
        ))}
      </div>
    </div>
  )
}

/**
 * Files — uses /api/v1/agents/{agentId}/files for an agent and
 * /api/v1/crews/{crewId}/files for a crew. The crew variant lists the
 * shared crew tree (/crew/shared) via the sidecar proxy.
 *
 * Now supports lazy directory expansion (click a folder to fetch its
 * children inline) + inline file preview pane on the right (click a
 * file to read its contents via /agents/{id}/files/download).
 */
function FilesTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelProps["context"] }) {
  const [tree, setTree] = useState<FileEntry[] | null>(null)
  const [expanded, setExpanded] = useState<Record<string, FileEntry[] | "loading" | "error">>({})
  const [error, setError] = useState<string | null>(null)
  const [previewPath, setPreviewPath] = useState<string | null>(null)
  const [previewContent, setPreviewContent] = useState<string | null>(null)
  const [previewError, setPreviewError] = useState<string | null>(null)
  // Edit mode state — false means read-only (default). Toggle via the
  // top-right button. dirty is tracked by the FileEditor's onDirtyChange
  // callback so Save only enables when there are real changes.
  const [editing, setEditing] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const editorSaveRef = useRef<(() => void) | null>(null)

  useEffect(() => {
    if (!context) return
    let cancelled = false
    setTree(null)
    setError(null)
    setExpanded({})
    setPreviewPath(null)
    setPreviewContent(null)
    setEditing(false)
    setDirty(false)
    const url = context.kind === "agent"
      ? `/api/v1/agents/${context.agentId}/files?workspace_id=${workspaceId}&path=/`
      : `/api/v1/crews/${context.crewId}/files?workspace_id=${workspaceId}`
    fetch(url)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data) => {
        if (cancelled) return
        setTree(Array.isArray(data?.entries) ? data.entries : Array.isArray(data) ? data : [])
      })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [context, workspaceId])

  const fetchDir = useCallback(async (subdir: string) => {
    if (!context || context.kind !== "agent") return
    setExpanded((p) => ({ ...p, [subdir]: "loading" }))
    try {
      const url = `/api/v1/agents/${context.agentId}/files?workspace_id=${workspaceId}&subdir=${encodeURIComponent(subdir)}`
      const r = await fetch(url)
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      const data = await r.json()
      const entries = Array.isArray(data?.entries) ? data.entries : Array.isArray(data) ? data : []
      setExpanded((p) => ({ ...p, [subdir]: entries as FileEntry[] }))
    } catch {
      setExpanded((p) => ({ ...p, [subdir]: "error" }))
    }
  }, [context, workspaceId])

  const toggleFolder = useCallback((path: string) => {
    setExpanded((p) => {
      if (p[path] && p[path] !== "loading" && p[path] !== "error") {
        const next = { ...p }
        delete next[path]
        return next
      }
      return p
    })
    if (!expanded[path] || expanded[path] === "error") {
      void fetchDir(path)
    }
  }, [expanded, fetchDir])

  const openFile = useCallback(async (filePath: string, _fileName: string) => {
    if (!context || context.kind !== "agent") return
    setPreviewPath(filePath)
    setPreviewContent(null)
    setPreviewError(null)
    setEditing(false)
    setDirty(false)
    try {
      const url = `/api/v1/agents/${context.agentId}/files/download?workspace_id=${workspaceId}&path=${encodeURIComponent(filePath)}`
      const r = await fetch(url)
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      const text = await r.text()
      // Cap at 256 KB to avoid hammering the panel with pathological files.
      const MAX = 256 * 1024
      setPreviewContent(text.length > MAX ? text.slice(0, MAX) + `\n\n... [truncated · file is ${text.length.toLocaleString()} bytes total]` : text)
    } catch (err) {
      setPreviewError(err instanceof Error ? err.message : String(err))
    }
  }, [context, workspaceId])

  const handleSave = useCallback(async (next: string) => {
    if (!context || context.kind !== "agent" || !previewPath) return
    setSaving(true)
    try {
      const url = `/api/v1/agents/${context.agentId}/files/save?workspace_id=${workspaceId}&path=${encodeURIComponent(previewPath)}`
      const r = await fetch(url, {
        method: "PUT",
        headers: { "Content-Type": "text/plain" },
        body: next,
      })
      if (!r.ok) {
        const data = await r.json().catch(() => ({ error: `HTTP ${r.status}` }))
        toast.error(typeof data.error === "string" ? data.error : "Save failed")
        return
      }
      // Persist the new content as the new baseline so re-opening the
      // file or hitting Cancel doesn't revert to the pre-save text.
      setPreviewContent(next)
      setDirty(false)
      setEditing(false)
      toast.success(`Saved · ${previewPath.split("/").pop()}`)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Save failed")
    } finally {
      setSaving(false)
    }
  }, [context, previewPath, workspaceId])

  if (!context) return <EmptyState>Select an agent or crew to browse files.</EmptyState>
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (tree === null) return <EmptyState>Loading…</EmptyState>
  if (tree.length === 0) {
    return (
      <EmptyState>
        {context.kind === "agent"
          ? `No files in ${context.agentName}'s home dir.`
          : "No shared files in this crew yet."}
      </EmptyState>
    )
  }

  const rootPath = context.kind === "agent"
    ? `/crew/agents/${context.agentSlug}/`
    : `/crew/shared/`

  return (
    <div className="h-full grid grid-cols-1 md:grid-cols-[minmax(220px,40%)_1fr] gap-0">
      {/* Tree */}
      <div className="overflow-y-auto p-3 text-xs border-r border-white/8">
        <div className="text-muted-foreground mb-2 font-mono">{rootPath}</div>
        <ul className="font-mono space-y-0.5">
          {tree.map((f) => (
            <FileRow
              key={f.name}
              entry={f}
              parentPath=""
              depth={0}
              expanded={expanded}
              onToggleFolder={toggleFolder}
              onOpenFile={openFile}
              activePath={previewPath}
              isAgent={context.kind === "agent"}
            />
          ))}
        </ul>
      </div>
      {/* Preview pane */}
      <div className="overflow-hidden flex flex-col min-h-0">
        {previewPath ? (
          <>
            <div className="flex items-center gap-2 px-3 py-1.5 border-b border-white/8 text-xs text-muted-foreground">
              <File className="h-3 w-3 shrink-0" />
              <span className="font-mono truncate flex-1">{previewPath}</span>
              {dirty && (
                <span className="text-[10px] text-amber-300 inline-flex items-center gap-1">
                  <span className="h-1.5 w-1.5 rounded-full bg-amber-400" />
                  Unsaved
                </span>
              )}
              {previewContent !== null && !dirty && (
                <span className="text-[10px]">
                  {previewContent.length.toLocaleString()} chars
                </span>
              )}
              {/* Edit / Save / Cancel — primary actions for the pane */}
              {previewContent !== null && previewError === null && (
                <>
                  {!editing ? (
                    <button
                      type="button"
                      onClick={() => setEditing(true)}
                      className="flex items-center gap-1 text-xs px-2 py-0.5 rounded bg-blue-500/15 hover:bg-blue-500/25 text-blue-300 border border-blue-500/30 ml-1"
                    >
                      <Pencil className="h-3 w-3" />
                      Edit
                    </button>
                  ) : (
                    <div className="flex items-center gap-1 ml-1">
                      <button
                        type="button"
                        onClick={() => editorSaveRef.current?.()}
                        disabled={!dirty || saving}
                        className={cn(
                          "flex items-center gap-1 text-xs px-2 py-0.5 rounded border transition-colors",
                          dirty && !saving
                            ? "bg-blue-500 hover:bg-blue-400 text-white border-blue-400"
                            : "bg-zinc-800 text-muted-foreground border-white/10 cursor-default",
                        )}
                      >
                        {saving
                          ? <Loader2 className="h-3 w-3 animate-spin" />
                          : <Save className="h-3 w-3" />}
                        Save
                      </button>
                      <button
                        type="button"
                        onClick={() => {
                          setEditing(false)
                          setDirty(false)
                          // Re-fetch to drop in-flight CodeMirror edits
                          if (previewPath) void openFile(previewPath, previewPath.split("/").pop() ?? "")
                        }}
                        disabled={saving}
                        className="flex items-center gap-1 text-xs px-2 py-0.5 rounded border border-white/10 hover:bg-white/5 text-muted-foreground"
                      >
                        Cancel
                      </button>
                    </div>
                  )}
                </>
              )}
            </div>
            {previewError ? (
              <div className="p-4 text-xs text-red-300">Failed: {previewError}</div>
            ) : previewContent === null ? (
              <div className="p-4 text-xs text-muted-foreground">Loading…</div>
            ) : (
              <div className={cn("flex-1 min-h-0 overflow-hidden", !editing && "pointer-events-none")}>
                <FileEditor
                  // CodeMirror remounts when the doc string ref changes,
                  // so the key includes the editing flag — switching
                  // between read-only and editable modes builds a fresh
                  // editor with the current baseline content (otherwise
                  // dirty state can leak across mode toggles).
                  key={`${previewPath}::${editing ? "edit" : "read"}`}
                  code={previewContent}
                  language={getEditorLanguage(previewPath.split("/").pop() ?? "")}
                  onSave={handleSave}
                  onDirtyChange={setDirty}
                  saveRef={editorSaveRef}
                />
              </div>
            )}
          </>
        ) : (
          <div className="flex items-center justify-center h-full text-xs text-muted-foreground/60 px-6 text-center">
            Click a file in the tree to preview its contents.
          </div>
        )}
      </div>
    </div>
  )
}

interface FileRowProps {
  entry: FileEntry
  parentPath: string
  depth: number
  expanded: Record<string, FileEntry[] | "loading" | "error">
  onToggleFolder: (path: string) => void
  onOpenFile: (path: string, name: string) => void
  activePath: string | null
  isAgent: boolean
}

function FileRow({ entry, parentPath, depth, expanded, onToggleFolder, onOpenFile, activePath, isAgent }: FileRowProps) {
  const path = parentPath ? `${parentPath}/${entry.name}` : entry.name
  const state = expanded[path]
  const isOpen = state && state !== "loading" && state !== "error"
  const children = isOpen ? (state as FileEntry[]) : []
  const isActive = activePath === path

  return (
    <>
      <li>
        <button
          type="button"
          onClick={() => {
            if (entry.is_dir) {
              if (!isAgent) return // crew tree expansion not yet supported
              onToggleFolder(path)
            } else if (isAgent) {
              onOpenFile(path, entry.name)
            }
          }}
          className={cn(
            "w-full flex items-center gap-2 px-2 -mx-2 py-0.5 rounded text-left transition-colors",
            isActive ? "bg-blue-500/15 text-blue-200" : "text-foreground/85 hover:bg-white/[0.03]",
          )}
          style={{ paddingLeft: `${depth * 12 + 8}px` }}
        >
          {entry.is_dir ? (
            <span className="inline-flex items-center w-3 shrink-0">
              <ChevronDown className={cn("h-3 w-3 text-muted-foreground transition-transform", !isOpen && "-rotate-90")} />
            </span>
          ) : (
            <span className="inline-block w-3 shrink-0" />
          )}
          {entry.is_dir
            ? <Folder className="h-3 w-3 shrink-0 text-blue-300" />
            : <File className="h-3 w-3 shrink-0 text-muted-foreground" />}
          <span className="flex-1 truncate">{entry.name}</span>
          {entry.size !== undefined && !entry.is_dir && (
            <span className="text-[10px] text-muted-foreground">{formatBytes(entry.size)}</span>
          )}
          {state === "loading" && <span className="text-[10px] text-muted-foreground italic">…</span>}
          {state === "error" && <span className="text-[10px] text-red-400">!</span>}
        </button>
      </li>
      {isOpen && children.map((child) => (
        <FileRow
          key={child.name}
          entry={child}
          parentPath={path}
          depth={depth + 1}
          expanded={expanded}
          onToggleFolder={onToggleFolder}
          onOpenFile={onOpenFile}
          activePath={activePath}
          isAgent={isAgent}
        />
      ))}
    </>
  )
}

// =============================================================================
// Internal helpers
// =============================================================================

const NOISE_KEYS = new Set([
  "_count", "config_hash", "cached_image", "cached_requirements",
  "deleted_at", "webhook_secret", "mcp_config_json",
  "schedule_last_run", "schedule_next_run",
])

function filterNoise(rec: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(rec)) {
    if (NOISE_KEYS.has(k)) continue
    if (v === null || v === undefined || v === "") continue
    out[k] = v
  }
  return out
}

function toYaml(rec: Record<string, unknown>, indent = 0): string {
  const pad = "  ".repeat(indent)
  let out = ""
  for (const [k, v] of Object.entries(rec)) {
    if (v === null || v === undefined) continue
    if (typeof v === "object" && !Array.isArray(v)) {
      out += `${pad}${k}:\n${toYaml(v as Record<string, unknown>, indent + 1)}`
    } else if (Array.isArray(v)) {
      out += `${pad}${k}:\n`
      for (const item of v) {
        if (typeof item === "object" && item !== null) {
          out += `${pad}  - ${toYaml(item as Record<string, unknown>, indent + 2).replace(/^\s+/, "")}`
        } else {
          out += `${pad}  - ${item}\n`
        }
      }
    } else {
      const s = typeof v === "string" && (v.includes("\n") || v.includes(":")) ? `"${v.replace(/"/g, '\\"')}"` : String(v)
      out += `${pad}${k}: ${s}\n`
    }
  }
  return out
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`
}

export type { BottomTab }
