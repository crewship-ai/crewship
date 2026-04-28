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

interface PeerMessage {
  id: string
  from_agent_id: string
  from_agent_name?: string
  content: string
  created_at: string
}

interface Escalation {
  id: string
  reason: string
  created_at: string
  status: string
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
 * Messages — pulls peer messages and escalations from the agent inbox.
 * Wired to GET /api/v1/agents/{agentId}/inbox; refreshes on tab open.
 */
function MessagesTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelProps["context"] }) {
  const [messages, setMessages] = useState<PeerMessage[] | null>(null)
  const [escalations, setEscalations] = useState<Escalation[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!context || context.kind !== "agent") return
    let cancelled = false
    setMessages(null)
    setEscalations(null)
    setError(null)
    fetch(`/api/v1/agents/${context.agentId}/inbox?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data) => {
        if (cancelled) return
        setMessages(Array.isArray(data?.peer_messages) ? data.peer_messages : [])
        setEscalations(Array.isArray(data?.escalations) ? data.escalations : [])
      })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [context, workspaceId])

  if (!context) return <EmptyState>Select an agent to see its inbox messages.</EmptyState>
  if (context.kind !== "agent") return <EmptyState>Messages are per-agent — select one in the explorer.</EmptyState>
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (messages === null) return <EmptyState>Loading…</EmptyState>
  if (messages.length === 0 && (escalations?.length ?? 0) === 0) {
    return <EmptyState>No messages or escalations for {context.agentName}.</EmptyState>
  }

  return (
    <div className="h-full overflow-y-auto p-3 space-y-1.5 text-xs">
      {(escalations ?? []).map((e) => (
        <div key={e.id} className="rounded border border-amber-500/30 bg-amber-500/5 px-3 py-2">
          <div className="flex items-center justify-between mb-0.5">
            <span className="text-amber-300 font-medium">Escalation</span>
            <span className="text-[10px] text-muted-foreground">{formatTime(e.created_at)}</span>
          </div>
          <div className="text-foreground/85">{e.reason}</div>
        </div>
      ))}
      {messages.map((m) => (
        <div key={m.id} className="rounded border border-white/10 bg-zinc-900/40 px-3 py-2">
          <div className="flex items-center justify-between mb-0.5">
            <span className="text-blue-300 font-medium">{m.from_agent_name ?? m.from_agent_id}</span>
            <span className="text-[10px] text-muted-foreground">{formatTime(m.created_at)}</span>
          </div>
          <div className="text-foreground/85 whitespace-pre-wrap">{m.content}</div>
        </div>
      ))}
    </div>
  )
}

/**
 * Exec Log — fetches /api/v1/agents/{agentId}/logs and renders raw lines.
 * No auto-refresh yet (keeps the implementation simple); user expands tab
 * = pulls fresh log.
 */
function ExecTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelProps["context"] }) {
  const [lines, setLines] = useState<string[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!context || context.kind !== "agent") return
    let cancelled = false
    setLines(null)
    setError(null)
    fetch(`/api/v1/agents/${context.agentId}/logs?workspace_id=${workspaceId}&tail=200`)
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((text) => {
        if (cancelled) return
        setLines(text.split(/\r?\n/).filter(Boolean))
      })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [context, workspaceId])

  if (!context) return <EmptyState>Select an agent to see its exec log.</EmptyState>
  if (context.kind !== "agent") return <EmptyState>Exec logs are per-agent — select one in the explorer.</EmptyState>
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (lines === null) return <EmptyState>Loading…</EmptyState>
  if (lines.length === 0) return <EmptyState>No log output yet for {context.agentName}.</EmptyState>

  return (
    <pre className="h-full overflow-y-auto p-3 text-[11px] leading-relaxed font-mono text-foreground/80 whitespace-pre">
      {lines.join("\n")}
    </pre>
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
 * Files — for an agent, lists files in its container home dir via
 * /api/v1/agents/{agentId}/files. For a crew, no per-crew file API
 * exists yet, so we surface an explanation rather than an empty list.
 */
function FilesTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelProps["context"] }) {
  const [files, setFiles] = useState<FileEntry[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!context || context.kind !== "agent") return
    let cancelled = false
    setFiles(null)
    setError(null)
    fetch(`/api/v1/agents/${context.agentId}/files?workspace_id=${workspaceId}&path=/`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data) => {
        if (cancelled) return
        setFiles(Array.isArray(data?.entries) ? data.entries : Array.isArray(data) ? data : [])
      })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [context, workspaceId])

  if (!context) return <EmptyState>Select an agent or crew to browse files.</EmptyState>
  if (context.kind === "crew") {
    return (
      <EmptyState>
        Crew-level shared files are not exposed via API yet. Pick an
        individual agent in the explorer to browse its files.
      </EmptyState>
    )
  }
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (files === null) return <EmptyState>Loading…</EmptyState>
  if (files.length === 0) return <EmptyState>No files in this agent&apos;s home dir.</EmptyState>

  return (
    <div className="h-full overflow-y-auto p-3 text-xs">
      <div className="text-muted-foreground mb-2 font-mono">
        /crew/agents/{context.agentSlug}/
      </div>
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
