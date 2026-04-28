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
 *
 * Default: collapsed (36 px, just tabs visible). Click any tab → expand
 * to 320 px and switch. Click toggle chevron → collapse without losing
 * tab selection.
 *
 * Content is selection-aware: agent-scoped when an agent is selected,
 * crew-scoped when a crew is selected, workspace-wide otherwise.
 */
export function BottomPanel({
  workspaceId: _workspaceId,
  context,
  initialTab = "messages",
  initialOpen = false,
  onOpenChange,
}: BottomPanelProps) {
  const [tab, setTab] = useState<BottomTab>(initialTab)
  const [open, setOpen] = useState(initialOpen)

  // Allow parent to programmatically switch tab + open (e.g. Files button).
  useEffect(() => {
    setTab(initialTab)
    setOpen(initialOpen)
  }, [initialTab, initialOpen])

  useEffect(() => { onOpenChange?.(open) }, [open, onOpenChange])

  const handleTab = (next: BottomTab, soon?: boolean) => {
    if (soon) return // Terminal is "coming soon" — non-clickable
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
          {tab === "messages" && <MessagesTab context={context} />}
          {tab === "exec" && <ExecTab context={context} />}
          {tab === "yaml" && <YamlTab context={context} />}
          {tab === "docker" && <DockerTab />}
          {tab === "files" && <FilesTab context={context} />}
        </div>
      )}
    </div>
  )
}

// =============================================================================
// Tab content — kept small here. Each tab can be lifted into its own file
// later if it grows; for now they're inlined to keep the wiring obvious.
// =============================================================================

function EmptyState({ children }: { children: React.ReactNode }) {
  return (
    <div className="h-full flex items-center justify-center text-xs text-muted-foreground p-4 text-center">
      {children}
    </div>
  )
}

function MessagesTab({ context }: { context: BottomPanelProps["context"] }) {
  if (!context) return <EmptyState>Select an agent or crew to see live messages.</EmptyState>
  if (context.kind !== "agent")
    return <EmptyState>Select an agent — messages are per-agent.</EmptyState>
  return (
    <div className="h-full overflow-y-auto p-4 text-xs font-mono text-muted-foreground">
      <div className="text-foreground/80">
        Live messages for <span className="text-foreground">{context.agentName}</span> stream here.
      </div>
      <div className="mt-2">
        Hooked up to <code className="text-blue-300">/api/v1/agents/{context.agentId}/inbox</code> and the
        WebSocket <code className="text-blue-300">message.received</code> channel.
      </div>
    </div>
  )
}

function ExecTab({ context }: { context: BottomPanelProps["context"] }) {
  if (!context) return <EmptyState>Select an agent or crew to see exec logs.</EmptyState>
  if (context.kind !== "agent")
    return <EmptyState>Select an agent — exec logs are per-agent.</EmptyState>
  return (
    <div className="h-full overflow-y-auto p-4 text-xs font-mono text-muted-foreground">
      Tails <code className="text-blue-300">/api/v1/agents/{context.agentId}/logs</code>.
    </div>
  )
}

function YamlTab({ context }: { context: BottomPanelProps["context"] }) {
  if (!context) return <EmptyState>Select an agent or crew to see its YAML config.</EmptyState>
  return (
    <div className="h-full overflow-y-auto p-4 text-xs font-mono text-muted-foreground leading-relaxed">
      Read-only YAML representation of the {context.kind}{" "}
      configuration. Edit via canvas fields or the CLI{" "}
      <code className="text-blue-300">crewship {context.kind} edit</code>.
    </div>
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

function FilesTab({ context }: { context: BottomPanelProps["context"] }) {
  if (!context) return <EmptyState>Select an agent or crew to browse files.</EmptyState>
  if (context.kind === "agent") {
    return (
      <div className="h-full overflow-y-auto p-4 text-xs">
        <div className="text-muted-foreground mb-2">
          /crew/agents/{context.agentSlug}/
        </div>
        <div className="text-muted-foreground">
          Hooked up to <code className="text-blue-300">/api/v1/agents/{context.agentId}/files</code>.
        </div>
      </div>
    )
  }
  return (
    <div className="h-full overflow-y-auto p-4 text-xs">
      <div className="text-muted-foreground mb-2">/crew/shared/</div>
      <div className="text-muted-foreground">
        Hooked up to crew shared volume listing.
      </div>
    </div>
  )
}

export type { BottomTab }
