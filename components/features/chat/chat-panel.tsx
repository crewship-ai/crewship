"use client"

import { useCallback, useEffect, useState, useRef } from "react"
import dynamic from "next/dynamic"
import {
  Bot,
  AlertCircle,
  Wifi,
  WifiOff,
  Loader2,
  PanelRightOpen,
  PanelRightClose,
  FileText,
  Settings2,
  Wrench,
  Zap,
  Users,
  Bookmark,
  ChevronRight,
  ChevronDown,
  FolderOpen,
  FolderClosed,
  FileCode,
  FileJson,
  File as FileIcon,
  Terminal,
  Box,
  X,
  Save,
  Maximize2,
  Minimize2,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"

import {
  Conversation,
  ConversationContent,
  ConversationScrollButton,
  ConversationEmptyState,
} from "@/components/ai-elements/conversation"
import {
  Message,
  MessageContent,
} from "@/components/ai-elements/message"
import {
  PromptInput,
  PromptInputTextarea,
  PromptInputFooter,
  PromptInputSubmit,
  type PromptInputMessage,
} from "@/components/ai-elements/prompt-input"
import { Suggestion, Suggestions } from "@/components/ai-elements/suggestion"
import { useChat, type ChatTurn } from "@/hooks/use-chat"
import { useWorkspace } from "@/hooks/use-workspace"
import { AssistantTurn } from "./assistant-turn"
import { toast } from "sonner"

const FileEditor = dynamic(
  () => import("@/components/features/files/file-editor").then((m) => m.FileEditor),
  { ssr: false, loading: () => <div className="flex items-center justify-center h-full"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></div> },
)

function getWsUrl(): string {
  if (process.env.NEXT_PUBLIC_WS_URL) return process.env.NEXT_PUBLIC_WS_URL
  if (typeof window === "undefined") return "ws://localhost:8080/ws"
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
  const host = window.location.port === "3001"
    ? window.location.hostname + ":8080"
    : window.location.host
  return `${proto}//${host}/ws`
}

interface ChatPanelProps {
  agentId: string
  sessionId: string
  agentName?: string
  /** Mobile-only: which panel to show full-screen. Undefined = desktop mode. */
  mobilePanel?: "chat" | "files" | "files-only" | "more"
}

interface FileEntry {
  path: string
  name: string
  size: number
  is_dir: boolean
  mod_time: string
}

const defaultSuggestions = [
  "Help me get started",
  "What can you do?",
  "Show me your skills",
  "Run a quick task",
]

function formatFileSize(bytes: number): string {
  if (bytes === 0) return "0 B"
  const units = ["B", "KB", "MB", "GB"]
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  const value = bytes / Math.pow(1024, i)
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[i]}`
}

function formatTimestamp(date: Date): string {
  return date.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })
}

/** Render a single turn (user, assistant, or system). */
function TurnRenderer({ turn, onCopy, onFileClick }: { turn: ChatTurn; onCopy: (s: string) => void; onFileClick: (s: string) => void }) {
  if (turn.role === "user") {
    const textContent = turn.parts.find((p) => p.type === "text")?.content ?? ""
    return (
      <Message from="user">
        <MessageContent>
          <div className="flex items-start gap-2">
            <span>{textContent}</span>
          </div>
        </MessageContent>
        <div className="text-micro text-muted-foreground ml-auto opacity-0 group-hover:opacity-100 transition-opacity">
          {formatTimestamp(turn.timestamp)}
        </div>
      </Message>
    )
  }

  if (turn.role === "system") {
    const part = turn.parts[0]
    const content = part?.content ?? ""
    const isError = part?.type === "error"
    const isInit = part?.type === "system_init"

    if (isInit) {
      const meta = part?.metadata ?? {}
      const model = meta.model as string | undefined
      const tools = meta.tools as string[] | undefined
      return (
        <div className="flex items-center justify-center py-2">
          <div className="flex items-center gap-3 px-4 py-2 bg-muted/40 border rounded-full text-label text-muted-foreground">
            <Settings2 className="h-3 w-3" />
            <span>Session started</span>
            {model && (
              <span className="font-mono text-micro bg-background px-1.5 py-0.5 rounded border">{model}</span>
            )}
            {tools && tools.length > 0 && (
              <span className="flex items-center gap-1">
                <Wrench className="h-3 w-3" />
                {tools.length} tools
              </span>
            )}
          </div>
        </div>
      )
    }

    return (
      <Message from="assistant">
        <MessageContent className={isError ? "border-destructive/50 bg-destructive/5 rounded-lg px-4 py-3" : ""}>
          <div className={`flex items-center gap-2 text-body ${isError ? "text-destructive" : "text-muted-foreground"}`}>
            <AlertCircle className="h-4 w-4 shrink-0" />
            {content}
          </div>
        </MessageContent>
      </Message>
    )
  }

  // Assistant turn - use the new grouped component
  return <AssistantTurn turn={turn} onCopy={onCopy} onFileClick={onFileClick} />
}

const RIGHT_PANEL_TABS = [
  { id: "files", label: "Files", icon: FileText },
  { id: "triggers", label: "Triggers", icon: Zap },
  { id: "team", label: "Team", icon: Users },
  { id: "context", label: "Context", icon: Bookmark },
] as const

interface TreeNode {
  path: string; name: string; size: number; is_dir: boolean
  children: TreeNode[]; childrenLoaded?: boolean
}

function buildTopLevelTree(files: FileEntry[]): TreeNode[] {
  const nodes = files.map((f) => ({ ...f, children: [] as TreeNode[], childrenLoaded: !f.is_dir }))
  nodes.sort((a, b) => { if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1; return a.name.localeCompare(b.name) })
  return nodes
}

function insertTreeChildren(tree: TreeNode[], parentPath: string, children: FileEntry[]): TreeNode[] {
  return tree.map((n) => {
    if (n.path === parentPath) {
      const c = children.map((f) => ({ ...f, children: [] as TreeNode[], childrenLoaded: !f.is_dir }))
      c.sort((a, b) => { if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1; return a.name.localeCompare(b.name) })
      return { ...n, children: c, childrenLoaded: true }
    }
    if (n.is_dir && n.children.length > 0) return { ...n, children: insertTreeChildren(n.children, parentPath, children) }
    return n
  })
}

function getChatFileIcon(name: string, isDir: boolean, isOpen?: boolean) {
  if (isDir) return isOpen ? <FolderOpen className="h-3.5 w-3.5 text-amber-500" /> : <FolderClosed className="h-3.5 w-3.5 text-amber-500" />
  const ext = name.split(".").pop()?.toLowerCase() ?? ""
  switch (ext) {
    case "py": return <FileCode className="h-3.5 w-3.5 text-yellow-500" />
    case "js": case "jsx": case "ts": case "tsx": return <FileCode className="h-3.5 w-3.5 text-blue-500" />
    case "json": return <FileJson className="h-3.5 w-3.5 text-yellow-600" />
    case "yaml": case "yml": return <FileJson className="h-3.5 w-3.5 text-red-400" />
    case "md": return <FileText className="h-3.5 w-3.5 text-blue-300" />
    case "sh": case "bash": return <Terminal className="h-3.5 w-3.5 text-green-500" />
    case "zip": case "tar": case "gz": return <Box className="h-3.5 w-3.5 text-purple-400" />
    default: return <FileIcon className="h-3.5 w-3.5 text-gray-400" />
  }
}

const PREVIEWABLE_EXTS = new Set(["py","js","jsx","ts","tsx","json","yaml","yml","md","txt","sh","bash","html","css","xml","svg","go","rs","toml","sql","cfg","ini","env","log","csv"])

function isPreviewable(name: string): boolean {
  const ext = name.split(".").pop()?.toLowerCase() ?? ""
  return PREVIEWABLE_EXTS.has(ext)
}

function getEditorLanguage(name: string): string {
  const ext = name.split(".").pop()?.toLowerCase() ?? ""
  const map: Record<string, string> = {
    ts: "typescript", tsx: "tsx", js: "javascript", jsx: "jsx",
    py: "python", go: "go", rs: "rust", sh: "bash", bash: "bash",
    json: "json", yaml: "yaml", yml: "yaml", xml: "xml", svg: "xml",
    html: "html", css: "css", scss: "css", less: "css",
    md: "markdown", txt: "text", sql: "sql", toml: "toml",
  }
  return map[ext] ?? "text"
}

function ChatTreeRow({ node, depth, expanded, loadingDirs, selectedFile, onToggle, onFileClick }: {
  node: TreeNode; depth: number; expanded: Set<string>; loadingDirs: Set<string>
  selectedFile: string | null; onToggle: (p: string) => void; onFileClick: (node: TreeNode) => void
}) {
  const isOpen = expanded.has(node.path)
  const isLoading = loadingDirs.has(node.path)
  const isSelected = !node.is_dir && selectedFile === node.path
  const canPreview = !node.is_dir && isPreviewable(node.name)
  return (
    <>
      <button
        className={cn(
          "w-full flex items-center gap-1.5 py-1 pr-3 text-label transition-colors",
          isSelected
            ? "bg-blue-50 text-blue-700 dark:bg-blue-950/30 dark:text-blue-300"
            : canPreview
              ? "text-muted-foreground hover:text-foreground hover:bg-accent/50 cursor-pointer"
              : "text-muted-foreground hover:text-foreground hover:bg-accent/50",
        )}
        style={{ paddingLeft: `${depth * 14 + 8}px` }}
        onClick={() => {
          if (node.is_dir) onToggle(node.path)
          else if (canPreview) onFileClick(node)
        }}
      >
        {node.is_dir ? (
          isLoading ? <Loader2 className="h-3 w-3 shrink-0 animate-spin" /> :
          isOpen ? <ChevronDown className="h-3 w-3 shrink-0" /> : <ChevronRight className="h-3 w-3 shrink-0" />
        ) : <span className="w-3" />}
        {getChatFileIcon(node.name, node.is_dir, isOpen)}
        <span className="truncate">{node.name}</span>
        {!node.is_dir && <span className="ml-auto text-micro text-muted-foreground/50 shrink-0">{formatFileSize(node.size)}</span>}
      </button>
      {node.is_dir && isOpen && node.children.map((child) => (
        <ChatTreeRow key={child.path} node={child} depth={depth + 1} expanded={expanded} loadingDirs={loadingDirs} selectedFile={selectedFile} onToggle={onToggle} onFileClick={onFileClick} />
      ))}
    </>
  )
}

interface RightPanelProps {
  agentId: string
  workspaceId: string | null
  files: FileEntry[]
  initialTab?: string
  style?: React.CSSProperties
}

function RightPanel({ agentId, workspaceId, files, initialTab, style }: RightPanelProps) {
  const [activeTab, setActiveTab] = useState<string>(initialTab ?? "files")
  const [tree, setTree] = useState<TreeNode[]>([])
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [loadingDirs, setLoadingDirs] = useState<Set<string>>(new Set())
  const [basePrefix, setBasePrefix] = useState("")

  // Editor state
  const [editorFile, setEditorFile] = useState<{ path: string; name: string } | null>(null)
  const [editorContent, setEditorContent] = useState<string | null>(null)
  const [editorLoading, setEditorLoading] = useState(false)
  const [editorDirty, setEditorDirty] = useState(false)
  const [editorExpanded, setEditorExpanded] = useState(false)
  const editorAbortRef = useRef<AbortController | null>(null)
  const [editorSaving, setEditorSaving] = useState(false)
  const saveRef = useRef<(() => void) | null>(null)

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
          .then((r) => r.ok ? r.json() : [])
          .then((data: FileEntry[] | null) => setTree((prev) => insertTreeChildren(prev, path, data ?? [])))
          .catch(() => {})
          .finally(() => setLoadingDirs((p) => { const n = new Set(p); n.delete(path); return n }))
      }
      return next
    })
  }, [tree, agentId, workspaceId, basePrefix])

  const openFileEditor = useCallback((node: TreeNode) => {
    if (!workspaceId) return
    editorAbortRef.current?.abort()
    const ac = new AbortController()
    editorAbortRef.current = ac
    setEditorFile({ path: node.path, name: node.name })
    setEditorLoading(true)
    setEditorContent(null)
    setEditorDirty(false)
    setEditorExpanded(false)
    fetch(`/api/v1/agents/${agentId}/files/download?workspace_id=${workspaceId}&path=${encodeURIComponent(node.path)}`, { signal: ac.signal })
      .then((r) => r.ok ? r.text() : null)
      .then((text) => { if (!ac.signal.aborted) setEditorContent(text) })
      .catch((err) => { if (err.name !== "AbortError") { setEditorContent(null); toast.error("Failed to load file") } })
      .finally(() => { if (!ac.signal.aborted) setEditorLoading(false) })
  }, [agentId, workspaceId])

  const closeEditor = useCallback(() => {
    setEditorFile(null)
    setEditorContent(null)
    setEditorDirty(false)
    setEditorExpanded(false)
  }, [])

  const handleEditorSave = useCallback((content: string) => {
    if (!workspaceId || !editorFile) return
    setEditorSaving(true)
    fetch(`/api/v1/agents/${agentId}/files/save?workspace_id=${workspaceId}&path=${encodeURIComponent(editorFile.path)}`, {
      method: "PUT",
      headers: { "Content-Type": "text/plain" },
      body: content,
    })
      .then((r) => {
        if (r.ok) { setEditorDirty(false); toast.success("File saved") }
        else toast.error("Save failed")
      })
      .catch(() => toast.error("Save failed"))
      .finally(() => setEditorSaving(false))
  }, [agentId, workspaceId, editorFile])

  const fileCount = files.filter((f) => !f.is_dir).length
  const editorOpen = editorFile !== null && activeTab === "files"

  return (
    <div className="flex flex-col border-l overflow-hidden" style={style}>
      <div className="flex items-end shrink-0 overflow-x-auto scrollbar-none border-b h-[41px]">
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
      </div>

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
          <div className="flex flex-col items-center justify-center h-full text-center p-6">
            <div className="h-10 w-10 rounded-full bg-accent flex items-center justify-center mb-3">
              <Zap className="h-5 w-5 text-muted-foreground" />
            </div>
            <p className="text-body font-medium">Triggers</p>
            <p className="text-label text-muted-foreground mt-1">Schedule cron jobs, webhooks, and automated triggers for this agent.</p>
            <span className="text-micro text-muted-foreground mt-3 px-2 py-1 bg-accent rounded-full">Coming soon</span>
          </div>
        )}

        {activeTab === "team" && (
          <div className="flex flex-col items-center justify-center h-full text-center p-6">
            <div className="h-10 w-10 rounded-full bg-accent flex items-center justify-center mb-3">
              <Users className="h-5 w-5 text-muted-foreground" />
            </div>
            <p className="text-body font-medium">Team Chat</p>
            <p className="text-label text-muted-foreground mt-1">Real-time communication between agents, leads, and crew members.</p>
            <span className="text-micro text-muted-foreground mt-3 px-2 py-1 bg-accent rounded-full">Coming soon</span>
          </div>
        )}

        {activeTab === "context" && (
          <div className="flex flex-col items-center justify-center h-full text-center p-6">
            <div className="h-10 w-10 rounded-full bg-accent flex items-center justify-center mb-3">
              <Bookmark className="h-5 w-5 text-muted-foreground" />
            </div>
            <p className="text-body font-medium">Shared Context</p>
            <p className="text-label text-muted-foreground mt-1">Shared instructions, knowledge base, and mission context for the agent.</p>
            <span className="text-micro text-muted-foreground mt-3 px-2 py-1 bg-accent rounded-full">Coming soon</span>
          </div>
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
}

function findTreeNode(node: TreeNode, path: string): TreeNode | undefined {
  if (node.path === path) return node
  for (const c of node.children) { const found = findTreeNode(c, path); if (found) return found }
  return undefined
}

/** Chat panel with split view: conversation on the left, tabbed panel on the right. */
export function ChatPanel({ agentId, sessionId, agentName, mobilePanel }: ChatPanelProps) {
  const { workspaceId } = useWorkspace()
  const [token, setToken] = useState<string | null>(null)
  const [authError, setAuthError] = useState(false)
  const [input, setInput] = useState("")
  const [sessionReady, setSessionReady] = useState(false)

  useEffect(() => {
    setSessionReady(false)
  }, [sessionId])

  const [showPreview, setShowPreview] = useState(true)
  const [files, setFiles] = useState<FileEntry[]>([])
  const [splitRatio, setSplitRatio] = useState(60)
  const containerRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    fetch("/api/v1/ws-token", { credentials: "include" })
      .then((r) => {
        if (r.status === 401) { setAuthError(true); return null }
        return r.json()
      })
      .then((data: { token?: string } | null) => {
        if (data?.token) setToken(data.token)
      })
      .catch(() => {})
  }, [])

  const { turns, sendMessage, stopGeneration, loadHistory, isStreaming, connectionStatus } = useChat({
    wsUrl: getWsUrl(),
    token,
    sessionId,
  })

  useEffect(() => {
    if (!sessionId) return
    fetch(`/api/v1/chats/${sessionId}/messages`, { credentials: "include" })
      .then((r) => r.ok ? r.json() : null)
      .then((data: { messages?: { id: string; role: string; content: string; ts: string }[] } | null) => {
        if (!data?.messages?.length) return
        setSessionReady(true)
        loadHistory(data.messages.map((m) => ({
          id: m.id,
          role: m.role as "user" | "assistant" | "system" | "tool",
          content: m.content,
          timestamp: new Date(m.ts),
        })))
      })
      .catch(() => {})
  }, [sessionId, loadHistory])

  const ensureSession = useCallback(async () => {
    if (sessionReady || !workspaceId || !sessionId) return
    try {
      const res = await fetch(
        `/api/v1/agents/${agentId}/chats?workspace_id=${encodeURIComponent(workspaceId)}`,
        { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ session_id: sessionId }) },
      )
      if (res.ok) setSessionReady(true)
    } catch { /* ignore */ }
  }, [agentId, workspaceId, sessionId, sessionReady])

  // Fetch files for the preview panel
  useEffect(() => {
    if (!workspaceId || !showPreview || !sessionId) return
    fetch(`/api/v1/agents/${agentId}/files?workspace_id=${workspaceId}`)
      .then((r) => r.ok ? r.json() : [])
      .then((data: FileEntry[] | null) => setFiles(data ?? []))
      .catch(() => {})
  }, [agentId, workspaceId, showPreview, sessionId])

  const handleSubmit = useCallback(async (message: PromptInputMessage) => {
    const text = message.text?.trim()
    if (!text || isStreaming) return
    await ensureSession()
    sendMessage(text)
    setInput("")
  }, [isStreaming, sendMessage, ensureSession])

  const handleSuggestionClick = useCallback(async (suggestion: string) => {
    if (isStreaming) return
    await ensureSession()
    sendMessage(suggestion)
  }, [isStreaming, sendMessage, ensureSession])

  const handleCopy = useCallback((content: string) => {
    navigator.clipboard.writeText(content).catch(() => {})
  }, [])

  // Drag resize handler
  const handleDragStart = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    const startX = e.clientX
    const startRatio = splitRatio

    const handleMove = (ev: MouseEvent) => {
      if (!containerRef.current) return
      const containerWidth = containerRef.current.offsetWidth
      const dx = ev.clientX - startX
      const newRatio = Math.min(80, Math.max(30, startRatio + (dx / containerWidth) * 100))
      setSplitRatio(newRatio)
    }

    const handleUp = () => {
      document.removeEventListener("mousemove", handleMove)
      document.removeEventListener("mouseup", handleUp)
    }

    document.addEventListener("mousemove", handleMove)
    document.addEventListener("mouseup", handleUp)
  }, [splitRatio])

  const handleResizeKeyDown = useCallback((e: React.KeyboardEvent) => {
    const step = 2
    if (e.key === "ArrowLeft") {
      e.preventDefault()
      setSplitRatio((r) => Math.max(30, r - step))
    } else if (e.key === "ArrowRight") {
      e.preventDefault()
      setSplitRatio((r) => Math.min(80, r + step))
    }
  }, [])

  const chatStatus = isStreaming ? "streaming" as const : "ready" as const

  // Mobile: files-only mode -- just the file tree, no tabs
  if (mobilePanel === "files-only") {
    return (
      <RightPanel
        agentId={agentId}
        workspaceId={workspaceId}
        files={files}
        initialTab="files"
        style={{ width: "100%" }}
      />
    )
  }

  // Mobile: show full RightPanel with all tabs (files + triggers + team + context)
  if (mobilePanel === "files") {
    return (
      <RightPanel
        agentId={agentId}
        workspaceId={workspaceId}
        files={files}
        initialTab="files"
        style={{ width: "100%" }}
      />
    )
  }

  if (mobilePanel === "more") {
    return (
      <RightPanel
        agentId={agentId}
        workspaceId={workspaceId}
        files={files}
        initialTab="triggers"
        style={{ width: "100%" }}
      />
    )
  }

  if (mobilePanel === "chat") {
    return (
      <div className="flex flex-col h-full">
        <div className="flex items-center gap-2 px-4 py-1.5 shrink-0">
          <div className={cn(
            "flex items-center gap-1.5 px-2 py-0.5 rounded-full text-micro font-medium",
            connectionStatus === "connected"
              ? "bg-emerald-50 text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-400"
              : connectionStatus === "connecting"
                ? "bg-amber-50 text-amber-700 dark:bg-amber-950/30 dark:text-amber-400"
                : "bg-red-50 text-red-700 dark:bg-red-950/30 dark:text-red-400"
          )}>
            {connectionStatus === "connected" ? (
              <Wifi className="h-3 w-3" />
            ) : connectionStatus === "connecting" ? (
              <Loader2 className="h-3 w-3 animate-spin" />
            ) : (
              <WifiOff className="h-3 w-3" />
            )}
            <span className="capitalize">{connectionStatus}</span>
          </div>
          <span className="text-micro text-muted-foreground ml-auto font-mono">
            {sessionId.slice(0, 8)}
          </span>
        </div>
        <div className="flex-1 flex flex-col overflow-hidden min-h-0">
          {authError ? (
            <div className="flex flex-col items-center justify-center h-full text-muted-foreground">
              <AlertCircle className="h-12 w-12 mb-3 opacity-30" />
              <p className="text-body">Session expired. Please log in again.</p>
            </div>
          ) : (
            <Conversation>
              <ConversationContent>
                {turns.length === 0 && (
                  <ConversationEmptyState
                    icon={<Bot className="h-12 w-12" />}
                    title="Start a conversation"
                    description={agentName ? `Send a message to ${agentName}` : "Send a message or pick a suggestion below"}
                  />
                )}
                {turns.map((turn) => (
                  <TurnRenderer key={turn.id} turn={turn} onCopy={handleCopy} onFileClick={() => {}} />
                ))}
              </ConversationContent>
              <ConversationScrollButton />
            </Conversation>
          )}
        </div>
        {turns.length === 0 && !authError && (
          <div className="px-4 pb-2 shrink-0">
            <Suggestions>
              {defaultSuggestions.map((s) => (
                <Suggestion key={s} suggestion={s} onClick={() => handleSuggestionClick(s)}>{s}</Suggestion>
              ))}
            </Suggestions>
          </div>
        )}
        <div className="p-3 shrink-0">
          <PromptInput className="rounded-xl border" onSubmit={handleSubmit}>
            <PromptInputTextarea
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder={agentName ? `Message ${agentName}...` : "Send a message..."}
              className="min-h-[44px]"
            />
            <PromptInputFooter className="justify-end p-2">
              <PromptInputSubmit
                disabled={!isStreaming && (!input.trim() || connectionStatus !== "connected")}
                status={chatStatus}
                onStop={stopGeneration}
              />
            </PromptInputFooter>
          </PromptInput>
        </div>
      </div>
    )
  }

  // Desktop: full split layout
  return (
    <div ref={containerRef} className="flex h-full">
      <div className="flex flex-col overflow-hidden" style={{ width: showPreview ? `${splitRatio}%` : "100%" }}>
        <div className="flex items-center gap-2 px-4 md:px-6 h-[41px] border-b shrink-0">
          <div className={cn(
            "flex items-center gap-1.5 px-2 py-0.5 rounded-full text-micro font-medium",
            connectionStatus === "connected"
              ? "bg-emerald-50 text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-400"
              : connectionStatus === "connecting"
                ? "bg-amber-50 text-amber-700 dark:bg-amber-950/30 dark:text-amber-400"
                : "bg-red-50 text-red-700 dark:bg-red-950/30 dark:text-red-400"
          )}>
            {connectionStatus === "connected" ? (
              <Wifi className="h-3 w-3" />
            ) : connectionStatus === "connecting" ? (
              <Loader2 className="h-3 w-3 animate-spin" />
            ) : (
              <WifiOff className="h-3 w-3" />
            )}
            <span className="capitalize">{connectionStatus}</span>
          </div>
          <span className="text-micro text-muted-foreground ml-auto font-mono">
            {sessionId.slice(0, 8)}
          </span>
          <Button
            variant="ghost"
            size="icon"
            className="h-6 w-6"
            onClick={() => setShowPreview(!showPreview)}
            title={showPreview ? "Hide panel" : "Show panel"}
          >
            {showPreview ? <PanelRightClose className="h-3.5 w-3.5" /> : <PanelRightOpen className="h-3.5 w-3.5" />}
          </Button>
        </div>
        <div className="flex-1 flex flex-col overflow-hidden min-h-0">
          {authError ? (
            <div className="flex flex-col items-center justify-center h-full text-muted-foreground">
              <AlertCircle className="h-12 w-12 mb-3 opacity-30" />
              <p className="text-body">Session expired. Please log in again.</p>
            </div>
          ) : (
            <Conversation>
              <ConversationContent>
                {turns.length === 0 && (
                  <ConversationEmptyState
                    icon={<Bot className="h-12 w-12" />}
                    title="Start a conversation"
                    description={agentName ? `Send a message to ${agentName}` : "Send a message or pick a suggestion below"}
                  />
                )}
                {turns.map((turn) => (
                  <TurnRenderer key={turn.id} turn={turn} onCopy={handleCopy} onFileClick={() => {}} />
                ))}
              </ConversationContent>
              <ConversationScrollButton />
            </Conversation>
          )}
        </div>
        {turns.length === 0 && !authError && (
          <div className="px-4 md:px-6 pb-2 shrink-0">
            <Suggestions>
              {defaultSuggestions.map((s) => (
                <Suggestion key={s} suggestion={s} onClick={() => handleSuggestionClick(s)}>{s}</Suggestion>
              ))}
            </Suggestions>
          </div>
        )}
        <div className="p-3 md:px-6 shrink-0">
          <PromptInput className="rounded-xl border" onSubmit={handleSubmit}>
            <PromptInputTextarea
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder={agentName ? `Message ${agentName}...` : "Send a message..."}
              className="min-h-[44px]"
            />
            <PromptInputFooter className="justify-end p-2">
              <PromptInputSubmit
                disabled={!isStreaming && (!input.trim() || connectionStatus !== "connected")}
                status={chatStatus}
                onStop={stopGeneration}
              />
            </PromptInputFooter>
          </PromptInput>
        </div>
      </div>

      {showPreview && (
        <div
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize chat pane"
          tabIndex={0}
          className="w-1 bg-border hover:bg-primary/40 focus-visible:bg-primary/40 cursor-col-resize shrink-0 transition-colors outline-none"
          onMouseDown={handleDragStart}
          onKeyDown={handleResizeKeyDown}
          title="Drag to resize"
        />
      )}

      {showPreview && (
        <RightPanel
          agentId={agentId}
          workspaceId={workspaceId}
          files={files}
          style={{ width: `${100 - splitRatio}%` }}
        />
      )}
    </div>
  )
}
