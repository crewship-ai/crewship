"use client"

import { useEffect, useState } from "react"
import {
  ChevronDown, ChevronUp, Container, FileCode2, Files,
  MessageSquare, Terminal,
} from "lucide-react"
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
  size?: number
  is_dir?: boolean
  modified?: string
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
export function BottomPanel({
  workspaceId,
  context,
  initialTab = "messages",
  initialOpen = false,
  onOpenChange,
}: BottomPanelProps) {
  const [tab, setTab] = useState<BottomTab>(initialTab)
  const [open, setOpen] = useState(initialOpen)

  useEffect(() => {
    setTab(initialTab)
    setOpen(initialOpen)
  }, [initialTab, initialOpen])

  useEffect(() => { onOpenChange?.(open) }, [open, onOpenChange])

  const handleTab = (next: BottomTab, soon?: boolean) => {
    if (soon) return
    setTab(next)
    setOpen(true)
  }

  return (
    <div
      className="shrink-0 border-t border-white/8 bg-card flex flex-col transition-[height] duration-200"
      style={{ height: open ? 320 : 36 }}
    >
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
  useEffect(() => {
    let cancelled = false
    fetch("/api/v1/system/runtime")
      .then((r) => (r.ok ? r.json() : null))
      .then((data) => {
        if (cancelled || !data) return
        const list: ContainerStatus[] = Array.isArray(data?.containers) ? data.containers : []
        setContainers(list)
      })
      .catch(() => { if (!cancelled) setContainers([]) })
    return () => { cancelled = true }
  }, [])

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
 */
function FilesTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelProps["context"] }) {
  const [files, setFiles] = useState<FileEntry[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!context) return
    let cancelled = false
    setFiles(null)
    setError(null)
    const url = context.kind === "agent"
      ? `/api/v1/agents/${context.agentId}/files?workspace_id=${workspaceId}&path=/`
      : `/api/v1/crews/${context.crewId}/files?workspace_id=${workspaceId}`
    fetch(url)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data) => {
        if (cancelled) return
        setFiles(Array.isArray(data?.entries) ? data.entries : Array.isArray(data) ? data : [])
      })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [context, workspaceId])

  if (!context) return <EmptyState>Select an agent or crew to browse files.</EmptyState>
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (files === null) return <EmptyState>Loading…</EmptyState>
  if (files.length === 0) {
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
    <div className="h-full overflow-y-auto p-3 text-xs">
      <div className="text-muted-foreground mb-2 font-mono">{rootPath}</div>
      <ul className="font-mono space-y-0.5">
        {files.map((f) => (
          <li key={f.name} className="flex items-center gap-2 text-foreground/85 hover:bg-white/[0.03] px-2 -mx-2 py-0.5 rounded">
            <span className={f.is_dir ? "text-blue-300" : "text-muted-foreground"}>
              {f.is_dir ? "📁" : "📄"}
            </span>
            <span className="flex-1">{f.name}</span>
            {f.size !== undefined && !f.is_dir && (
              <span className="text-[10px] text-muted-foreground">{formatBytes(f.size)}</span>
            )}
          </li>
        ))}
      </ul>
    </div>
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
