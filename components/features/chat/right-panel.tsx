"use client"

import React, { useCallback, useEffect, useState } from "react"
import dynamic from "next/dynamic"
import {
  FileText,
  Zap,
  Users,
  Bookmark,
  Loader2,
  X,
  Save,
  Maximize2,
  Minimize2,
  Clock,
  Globe,
  Copy,
  CheckCircle2,
  XCircle,
  Terminal,
  Shield,
  Cpu,
  Bot,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { toast } from "sonner"

import {
  ChatTreeRow,
  type TreeNode,
  type FileEntry,
  buildTopLevelTree,
  insertTreeChildren,
  findTreeNode,
  getChatFileIcon,
  getEditorLanguage,
} from "./chat-tree-row"
import { useFileEditor } from "./hooks/use-file-editor"

const FileEditor = dynamic(
  () => import("@/components/features/files/file-editor").then((m) => m.FileEditor),
  { ssr: false, loading: () => <div className="flex items-center justify-center h-full"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></div> },
)

const RIGHT_PANEL_TABS = [
  { id: "files", label: "Files", icon: FileText },
  { id: "triggers", label: "Triggers", icon: Zap },
  { id: "team", label: "Team", icon: Users },
  { id: "context", label: "Context", icon: Bookmark },
] as const

// ── Triggers Tab ──

interface AgentScheduleInfo {
  schedule_cron: string | null
  schedule_prompt: string | null
  schedule_enabled: boolean
  schedule_last_run: string | null
  schedule_next_run: string | null
  webhook_secret: string | null
  crew_id: string | null
  slug: string
}

function TriggersTab({ agentId, workspaceId }: { agentId: string; workspaceId: string | null }) {
  const [agent, setAgent] = useState<AgentScheduleInfo | null>(null)
  const [loading, setLoading] = useState(true)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    if (!workspaceId) {
      // Don't leave the tab stuck on a spinner when no workspace is
      // selected — clear loading and fall through to the empty state.
      setLoading(false)
      setAgent(null)
      return
    }
    setLoading(true)
    fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`)
      .then((r) => r.json())
      .then((data) => setAgent(data))
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [agentId, workspaceId])

  if (loading) return <div className="flex items-center justify-center h-full"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></div>
  if (!workspaceId) return <div className="p-4 text-label text-muted-foreground">Select a workspace to view triggers.</div>
  if (!agent) return <div className="p-4 text-label text-muted-foreground">Unable to load agent</div>

  const webhookUrl = agent.crew_id && agent.slug
    ? `/api/v1/webhooks/${agent.crew_id}/${agentId}/trigger`
    : null

  return (
    <div className="p-3 space-y-4 text-sm">
      {/* Cron Schedule */}
      <div className="space-y-2">
        <div className="flex items-center gap-1.5 text-label font-medium text-muted-foreground uppercase tracking-wider">
          <Clock className="h-3 w-3" />
          Schedule
        </div>
        {agent.schedule_cron ? (
          <div className="space-y-1.5">
            <div className="flex items-center gap-2">
              <code className="text-label bg-accent px-1.5 py-0.5 rounded font-mono">{agent.schedule_cron}</code>
              {agent.schedule_enabled ? (
                <span className="flex items-center gap-1 text-micro text-emerald-500"><CheckCircle2 className="h-3 w-3" /> Active</span>
              ) : (
                <span className="flex items-center gap-1 text-micro text-muted-foreground"><XCircle className="h-3 w-3" /> Disabled</span>
              )}
            </div>
            {agent.schedule_prompt && (
              <p className="text-label text-muted-foreground line-clamp-2">{agent.schedule_prompt}</p>
            )}
            {agent.schedule_next_run && (
              <p className="text-micro text-muted-foreground">
                Next run: {new Date(agent.schedule_next_run).toLocaleString()}
              </p>
            )}
            {agent.schedule_last_run && (
              <p className="text-micro text-muted-foreground">
                Last run: {new Date(agent.schedule_last_run).toLocaleString()}
              </p>
            )}
          </div>
        ) : (
          <p className="text-label text-muted-foreground">No schedule configured. Set one in Agent Settings &rarr; Schedule.</p>
        )}
      </div>

      {/* Webhook */}
      <div className="space-y-2">
        <div className="flex items-center gap-1.5 text-label font-medium text-muted-foreground uppercase tracking-wider">
          <Globe className="h-3 w-3" />
          Webhook
        </div>
        {webhookUrl ? (
          <div className="space-y-1.5">
            <div className="flex items-center gap-1">
              <code className="text-micro bg-accent px-1.5 py-0.5 rounded font-mono truncate flex-1">{webhookUrl}</code>
              <button
                type="button"
                aria-label={copied ? "Webhook URL copied" : "Copy webhook URL"}
                onClick={async () => {
                  // Clipboard write can reject if the page loses focus or
                  // the permission is denied — surface a toast instead of
                  // leaving an unhandled promise rejection.
                  try {
                    await navigator.clipboard.writeText(window.location.origin + webhookUrl)
                    setCopied(true)
                    setTimeout(() => setCopied(false), 2000)
                  } catch {
                    toast.error("Failed to copy webhook URL")
                  }
                }}
                className="p-1 rounded hover:bg-accent text-muted-foreground"
              >
                {copied ? <CheckCircle2 className="h-3 w-3 text-emerald-500" /> : <Copy className="h-3 w-3" />}
              </button>
            </div>
            <p className="text-micro text-muted-foreground">
              POST with JSON body. {agent.webhook_secret ? "Secret header required." : "No secret configured."}
            </p>
          </div>
        ) : (
          <p className="text-label text-muted-foreground">Assign agent to a crew to enable webhooks.</p>
        )}
      </div>
    </div>
  )
}

// ── Shared Context Tab ──

interface AgentContextInfo {
  name: string
  slug: string
  agent_role: string
  system_prompt: string | null
  tool_profile: string | null
  cli_adapter: string | null
  llm_provider: string | null
  llm_model: string | null
  crew_id: string | null
  description: string | null
}

function SharedContextTab({ agentId, workspaceId }: { agentId: string; workspaceId: string | null }) {
  const [agent, setAgent] = useState<AgentContextInfo | null>(null)
  const [crew, setCrew] = useState<{ name: string; description: string | null; network_mode: string | null; allowed_domains: string | null } | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    if (!workspaceId) {
      setLoading(false)
      setAgent(null)
      setCrew(null)
      return
    }
    setLoading(true)
    // Chain the crew fetch into the outer promise so the finally()
    // block only fires once BOTH have resolved/rejected — otherwise the
    // crew section "pops in" after the spinner clears.
    fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`)
      .then((r) => r.json())
      .then((data: AgentContextInfo) => {
        setAgent(data)
        if (data.crew_id) {
          return fetch(`/api/v1/crews/${data.crew_id}?workspace_id=${workspaceId}`)
            .then((r) => r.json())
            .then((c) => setCrew(c))
        }
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [agentId, workspaceId])

  if (loading) return <div className="flex items-center justify-center h-full"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></div>
  if (!workspaceId) return <div className="p-4 text-label text-muted-foreground">Select a workspace to view context.</div>
  if (!agent) return <div className="p-4 text-label text-muted-foreground">Unable to load agent</div>

  return (
    <div className="p-3 space-y-4 text-sm">
      {/* Agent Info */}
      <div className="space-y-2">
        <div className="flex items-center gap-1.5 text-label font-medium text-muted-foreground uppercase tracking-wider">
          <Bot className="h-3 w-3" />
          Agent
        </div>
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            <span className="text-label font-medium">{agent.name}</span>
            <span className="text-micro bg-accent px-1.5 py-0.5 rounded">{agent.agent_role}</span>
          </div>
          {agent.description && <p className="text-label text-muted-foreground line-clamp-2">{agent.description}</p>}
          <div className="flex flex-wrap gap-2 text-micro text-muted-foreground">
            {agent.llm_provider && <span className="flex items-center gap-1"><Cpu className="h-3 w-3" />{agent.llm_provider}/{agent.llm_model ?? "default"}</span>}
            {agent.cli_adapter && <span className="flex items-center gap-1"><Terminal className="h-3 w-3" />{agent.cli_adapter}</span>}
            {agent.tool_profile && <span className="flex items-center gap-1"><Shield className="h-3 w-3" />{agent.tool_profile}</span>}
          </div>
        </div>
      </div>

      {/* System Prompt */}
      {agent.system_prompt && (
        <div className="space-y-2">
          <div className="text-label font-medium text-muted-foreground uppercase tracking-wider">System Prompt</div>
          <pre className="text-label text-muted-foreground bg-accent p-2 rounded whitespace-pre-wrap break-words max-h-48 overflow-y-auto font-mono leading-relaxed">
            {agent.system_prompt}
          </pre>
        </div>
      )}

      {/* Crew Context */}
      {crew && (
        <div className="space-y-2">
          <div className="flex items-center gap-1.5 text-label font-medium text-muted-foreground uppercase tracking-wider">
            <Users className="h-3 w-3" />
            Crew
          </div>
          <div className="space-y-1">
            <span className="text-label font-medium">{crew.name}</span>
            {crew.description && <p className="text-label text-muted-foreground line-clamp-2">{crew.description}</p>}
            {crew.network_mode && (
              <p className="text-micro text-muted-foreground">
                Network: {crew.network_mode}
                {crew.allowed_domains && ` (${crew.allowed_domains})`}
              </p>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

// ── Team Chat Tab ──

interface PeerMessage {
  id: string
  from_name: string
  from_slug: string
  to_name: string
  to_slug: string
  question: string
  response: string | null
  status: string
  created_at: string
}

function TeamChatTab({ agentId, workspaceId }: { agentId: string; workspaceId: string | null }) {
  const [messages, setMessages] = useState<PeerMessage[]>([])
  const [agentSlug, setAgentSlug] = useState<string | null>(null)
  const [crewId, setCrewId] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    if (!workspaceId) {
      setLoading(false)
      setMessages([])
      setCrewId(null)
      setAgentSlug(null)
      return
    }
    setLoading(true)
    fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`)
      .then((r) => r.json())
      .then((agent) => {
        setAgentSlug(agent.slug)
        setCrewId(agent.crew_id)
        if (agent.crew_id) {
          return fetch(`/api/v1/crews/${agent.crew_id}/peer-conversations?workspace_id=${workspaceId}`)
            .then((r) => r.json())
            .then((data) => {
              const all = Array.isArray(data) ? data : []
              // Filter to conversations involving this agent
              setMessages(all.filter((m: PeerMessage) => m.from_slug === agent.slug || m.to_slug === agent.slug))
            })
        }
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [agentId, workspaceId])

  if (loading) return <div className="flex items-center justify-center h-full"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></div>

  if (!crewId) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-center p-6">
        <Users className="h-8 w-8 text-muted-foreground/30 mb-2" />
        <p className="text-label text-muted-foreground">Assign agent to a crew to see team conversations.</p>
      </div>
    )
  }

  if (messages.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-center p-6">
        <Users className="h-8 w-8 text-muted-foreground/30 mb-2" />
        <p className="text-body font-medium text-muted-foreground">No conversations yet</p>
        <p className="text-label text-muted-foreground mt-1">Agent-to-agent conversations will appear here.</p>
      </div>
    )
  }

  return (
    <div className="p-2 space-y-2">
      {messages.map((msg) => {
        const isOutgoing = msg.from_slug === agentSlug
        return (
          <div key={msg.id} className="rounded-lg border border-border/50 p-2.5 space-y-1.5">
            <div className="flex items-center gap-1.5 text-micro">
              <span className={cn("font-medium", isOutgoing ? "text-blue-400" : "text-emerald-400")}>
                {msg.from_name}
              </span>
              <span className="text-muted-foreground/50">&rarr;</span>
              <span className="font-medium text-muted-foreground">{msg.to_name}</span>
              <span className="ml-auto text-muted-foreground/40">
                {new Date(msg.created_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
              </span>
            </div>
            <p className="text-xs text-foreground/80 whitespace-pre-wrap line-clamp-3">{msg.question}</p>
            {msg.response && (
              <div className="pl-2 border-l-2 border-emerald-500/30">
                <p className="text-xs text-muted-foreground whitespace-pre-wrap line-clamp-3">{msg.response}</p>
              </div>
            )}
            {msg.status === "RUNNING" && (
              <div className="flex items-center gap-1 text-micro text-blue-400">
                <Loader2 className="h-3 w-3 animate-spin" /> Processing...
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

interface RightPanelProps {
  agentId: string
  workspaceId: string | null
  files: FileEntry[]
  initialTab?: string
  hideTabs?: boolean
  style?: React.CSSProperties
}

export const RightPanel = React.memo(function RightPanel({ agentId, workspaceId, files, initialTab, hideTabs, style }: RightPanelProps) {
  const [activeTab, setActiveTab] = useState<string>(initialTab ?? "files")
  const [tree, setTree] = useState<TreeNode[]>([])
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [loadingDirs, setLoadingDirs] = useState<Set<string>>(new Set())
  const [basePrefix, setBasePrefix] = useState("")

  const {
    editorFile,
    editorContent,
    editorLoading,
    editorDirty,
    editorExpanded,
    editorSaving,
    saveRef,
    setEditorDirty,
    setEditorExpanded,
    openFileEditor,
    closeEditor,
    handleEditorSave,
  } = useFileEditor({ agentId, workspaceId })

  useEffect(() => {
    setTree(buildTopLevelTree(files))
    if (files.length > 0) {
      const first = files[0]
      const idx = first.path.lastIndexOf(first.name)
      setBasePrefix(idx > 0 ? first.path.slice(0, idx) : "")
    }
  }, [files])

  const toggleFolder = useCallback((path: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(path)) { next.delete(path); return next }
      next.add(path)
      const node = tree.reduce<TreeNode | undefined>((found, n) => found ?? findTreeNode(n, path), undefined)
      if (node && !node.childrenLoaded && workspaceId) {
        const relPath = path.startsWith(basePrefix) ? path.slice(basePrefix.length) : path
        setLoadingDirs((p) => new Set(p).add(path))
        fetch(`/api/v1/agents/${agentId}/files?workspace_id=${workspaceId}&subdir=${encodeURIComponent(relPath)}`)
          .then((r) => { if (!r.ok) throw new Error("Failed"); return r.json() })
          .then((data: FileEntry[] | null) => setTree((prev) => insertTreeChildren(prev, path, data ?? [])))
          .catch(() => { toast.error("Failed to load folder") })
          .finally(() => setLoadingDirs((p) => { const n = new Set(p); n.delete(path); return n }))
      }
      return next
    })
  }, [tree, agentId, workspaceId, basePrefix])

  const fileCount = files.filter((f) => !f.is_dir).length
  const editorOpen = editorFile !== null && activeTab === "files"

  return (
    <div className="flex flex-col border-l overflow-hidden" style={style}>
      {!hideTabs && <div className="flex items-end shrink-0 overflow-x-auto scrollbar-none border-b h-[41px]">
        {RIGHT_PANEL_TABS.map((tab) => (
          <button
            key={tab.id}
            onClick={() => setActiveTab(tab.id)}
            className={cn(
              "flex-1 flex items-center justify-center gap-1.5 pb-2.5 text-micro font-medium transition-colors shrink-0 border-b-2 mb-[-1px]",
              tab.id === activeTab
                ? "text-foreground border-primary"
                : "text-muted-foreground hover:text-foreground border-transparent"
            )}
          >
            <tab.icon className="h-3.5 w-3.5" />
            {tab.label}
          </button>
        ))}
      </div>}

      {/* Tree area -- scrolls independently */}
      <div className={cn("overflow-y-auto", editorOpen ? "flex-1 min-h-0" : "flex-1")}>
        {activeTab === "files" && (
          tree.length > 0 ? (
            <div className="py-1">
              {tree.map((node) => (
                <ChatTreeRow
                  key={node.path}
                  node={node}
                  depth={0}
                  expanded={expanded}
                  loadingDirs={loadingDirs}
                  selectedFile={editorFile?.path ?? null}
                  onToggle={toggleFolder}
                  onFileClick={openFileEditor}
                />
              ))}
            </div>
          ) : (
            <div className="flex flex-col items-center justify-center h-full text-muted-foreground">
              <FileText className="h-8 w-8 mb-2 opacity-20" />
              <p className="text-label">No files yet</p>
            </div>
          )
        )}

        {activeTab === "triggers" && (
          <TriggersTab agentId={agentId} workspaceId={workspaceId} />
        )}

        {activeTab === "team" && (
          <TeamChatTab agentId={agentId} workspaceId={workspaceId} />
        )}

        {activeTab === "context" && (
          <SharedContextTab agentId={agentId} workspaceId={workspaceId} />
        )}
      </div>

      {/* Slide-up editor */}
      {editorOpen && (
        <div className={cn(
          "flex flex-col border-t bg-[#1e1e1e] shrink-0 transition-all duration-300 ease-in-out",
          editorExpanded ? "h-[70%]" : "h-[40%]",
        )}>
          {/* Editor header */}
          <div className="flex items-center justify-between px-3 py-1.5 bg-[#252526] border-b border-[#3c3c3c] shrink-0">
            <div className="flex items-center gap-2 min-w-0">
              {getChatFileIcon(editorFile.name, false)}
              <span className="text-label text-[#cccccc] font-medium truncate">{editorFile.name}</span>
              {editorDirty && <span className="w-1.5 h-1.5 rounded-full bg-amber-400 shrink-0" />}
            </div>
            <div className="flex items-center gap-1 shrink-0">
              <button
                onClick={() => { if (saveRef.current) saveRef.current() }}
                disabled={!editorDirty || editorSaving}
                className={cn(
                  "flex items-center gap-1 px-2 py-0.5 rounded text-micro font-medium transition-colors",
                  editorDirty && !editorSaving
                    ? "bg-blue-600 text-white hover:bg-blue-700"
                    : "bg-[#3c3c3c] text-[#666] cursor-default",
                )}
              >
                {editorSaving ? <Loader2 className="h-3 w-3 animate-spin" /> : <Save className="h-3 w-3" />}
                Save
              </button>
              <button onClick={() => setEditorExpanded(!editorExpanded)} className="p-1 rounded hover:bg-[#3c3c3c] text-[#888]">
                {editorExpanded ? <Minimize2 className="h-3 w-3" /> : <Maximize2 className="h-3 w-3" />}
              </button>
              <button onClick={closeEditor} className="p-1 rounded hover:bg-[#3c3c3c] text-[#888]">
                <X className="h-3 w-3" />
              </button>
            </div>
          </div>

          {/* Editor body */}
          <div className="flex-1 min-h-0 overflow-hidden">
            {editorLoading ? (
              <div className="flex items-center justify-center h-full">
                <Loader2 className="h-5 w-5 animate-spin text-[#888]" />
              </div>
            ) : editorContent !== null ? (
              <FileEditor
                code={editorContent}
                language={getEditorLanguage(editorFile.name)}
                onSave={handleEditorSave}
                onDirtyChange={setEditorDirty}
                saveRef={saveRef}
              />
            ) : (
              <div className="flex items-center justify-center h-full text-[#888] text-label">
                Unable to load file
              </div>
            )}
          </div>

          {/* Status bar */}
          <div className="flex items-center justify-between px-3 py-0.5 bg-[#007acc] text-micro text-white shrink-0">
            <span>Ctrl+S to save</span>
            {editorDirty && <span className="font-medium">Modified</span>}
          </div>
        </div>
      )}

      {/* Footer (file count) -- only when no editor */}
      {!editorOpen && activeTab === "files" && tree.length > 0 && (
        <div className="px-3 py-1.5 border-t text-micro text-muted-foreground shrink-0">
          {fileCount} file{fileCount !== 1 ? "s" : ""}
        </div>
      )}
    </div>
  )
})
