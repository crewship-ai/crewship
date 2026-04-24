"use client"

import { useRouter } from "next/navigation"
import { useAgentId } from "@/hooks/use-agent-id"
import { useState, useEffect, useCallback, useRef } from "react"
import dynamic from "next/dynamic"
import {
  Download, AlertCircle, Inbox, Copy, Check, RefreshCw,
  ChevronRight, ChevronDown, Search, Home, GitBranch,
  FolderOpen, FolderClosed, FileText, FileCode, FileJson, Terminal,
  Box, Settings, Loader2, Pencil, Save, X,
  File as FileIcon,
} from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusDot } from "@/components/ui/status-badge"
import { CodeBlock } from "@/components/ai-elements/code-block"
import type { BundledLanguage } from "shiki"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAgentDetail } from "@/hooks/use-agent-detail"
import { useRealtimeEvent, useRealtimeChannel } from "@/hooks/use-realtime"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import type { FileEntry, TreeNode } from "@/lib/types/agent"

const FileEditor = dynamic(() => import("@/components/features/files/file-editor").then((m) => ({ default: m.FileEditor })), {
  ssr: false,
  loading: () => <div className="flex items-center justify-center h-full"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></div>,
})

function sortNodes(nodes: TreeNode[]) {
  nodes.sort((a, b) => {
    if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1
    return a.name.localeCompare(b.name)
  })
}

function buildTopLevel(files: FileEntry[]): TreeNode[] {
  const roots: TreeNode[] = files.map((f) => ({
    ...f,
    children: [],
    childrenLoaded: !f.is_dir,
  }))
  sortNodes(roots)
  return roots
}

function mergeTopLevel(existing: TreeNode[], fresh: FileEntry[]): TreeNode[] {
  const oldByPath = new Map(existing.map((n) => [n.path, n]))
  const merged = fresh.map((f) => {
    const prev = oldByPath.get(f.path)
    if (prev && prev.is_dir && prev.childrenLoaded) {
      return { ...prev, size: f.size, mod_time: f.mod_time }
    }
    return { ...f, children: [], childrenLoaded: !f.is_dir }
  })
  sortNodes(merged)
  return merged
}

function insertChildren(tree: TreeNode[], parentPath: string, children: FileEntry[]): TreeNode[] {
  return tree.map((node) => {
    if (node.path === parentPath) {
      const newChildren = children.map((f) => ({
        ...f,
        children: [],
        childrenLoaded: !f.is_dir,
      }))
      sortNodes(newChildren)
      return { ...node, children: newChildren, childrenLoaded: true }
    }
    if (node.is_dir && node.children.length > 0) {
      return { ...node, children: insertChildren(node.children, parentPath, children) }
    }
    return node
  })
}

function fmtSize(bytes: number): string {
  if (!bytes) return "—"
  const units = ["B", "KB", "MB", "GB"]
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  const v = bytes / Math.pow(1024, i)
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}

function fmtTime(modTime: string): string {
  const mins = Math.floor((Date.now() - new Date(modTime).getTime()) / 60000)
  if (mins < 1) return "Just now"
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  const days = Math.floor(hrs / 24)
  if (days === 1) return "Yesterday"
  if (days < 7) return `${days}d ago`
  return new Date(modTime).toLocaleDateString()
}

function getLang(name: string): string {
  const ext = name.split(".").pop()?.toLowerCase() ?? ""
  const map: Record<string, string> = {
    ts: "typescript", tsx: "tsx", js: "javascript", jsx: "jsx",
    py: "python", go: "go", rs: "rust", sh: "bash",
    json: "json", yaml: "yaml", yml: "yaml", xml: "xml",
    html: "html", css: "css", md: "markdown", txt: "text",
    sql: "sql", toml: "toml", env: "bash",
  }
  return map[ext] ?? "text"
}

const PREVIEWABLE_EXTENSIONS = new Set([
  "txt", "md", "mdx", "py", "js", "jsx", "ts", "tsx", "go", "rs", "rb",
  "sh", "bash", "zsh", "fish", "bat", "ps1",
  "json", "yaml", "yml", "toml", "xml", "csv", "ini", "cfg",
  "html", "css", "scss", "less", "svg",
  "sql", "graphql", "prisma",
  "gitignore", "gitattributes", "editorconfig", "prettierrc",
  "dockerfile", "makefile", "cmakelists",
  "c", "cpp", "h", "hpp", "java", "kt", "swift", "dart", "lua", "r",
  "tf", "hcl", "proto",
])

const PREVIEWABLE_FILENAMES = new Set([
  "dockerfile", "makefile", "cmakelists.txt", ".gitignore",
  ".gitattributes", ".editorconfig", ".prettierrc", ".eslintrc",
  "license", "readme", "changelog", "authors",
])

function isPreviewable(name: string): boolean {
  const n = name.toLowerCase()
  if (PREVIEWABLE_FILENAMES.has(n)) return true
  const ext = n.split(".").pop() ?? ""
  return PREVIEWABLE_EXTENSIONS.has(ext)
}

function getFileIcon(name: string, isDir: boolean, isOpen?: boolean) {
  // All file/folder icons render with muted-foreground — semantic distinction
  // comes from the icon glyph (FileCode, FileJson, Terminal, etc.), not color.
  const cls = "h-4 w-4 text-muted-foreground"
  if (isDir) return isOpen ? <FolderOpen className={cls} /> : <FolderClosed className={cls} />
  const ext = name.split(".").pop()?.toLowerCase() ?? ""
  const n = name.toLowerCase()
  if (n === "dockerfile" || n === "docker-compose.yml") return <Box className={cls} />
  if (n.startsWith(".git")) return <GitBranch className={cls} />
  if (n === "makefile") return <Terminal className={cls} />
  switch (ext) {
    case "py": case "js": case "jsx": case "ts": case "tsx":
    case "go": case "rs": case "html": case "css": case "scss":
    case "sql":
      return <FileCode className={cls} />
    case "json": case "yaml": case "yml":
      return <FileJson className={cls} />
    case "md": case "mdx": case "txt":
      return <FileText className={cls} />
    case "sh": case "bash":
      return <Terminal className={cls} />
    case "env":
      return <Settings className={cls} />
    default:
      return <FileIcon className={cls} />
  }
}

function TreeNodeRow({ node, depth, selectedPath, expandedPaths, loadingDirs, onSelect, onToggle }: {
  node: TreeNode; depth: number; selectedPath: string | null
  expandedPaths: Set<string>; loadingDirs: Set<string>
  onSelect: (p: string) => void; onToggle: (p: string) => void
}) {
  const isOpen = expandedPaths.has(node.path)
  const isLoading = loadingDirs.has(node.path)
  return (
    <>
      <button
        className={cn(
          "w-full flex items-center gap-1.5 py-1.5 pr-3 text-label transition-colors hover:bg-accent/50",
          selectedPath === node.path && "bg-accent text-foreground font-medium",
          selectedPath !== node.path && "text-muted-foreground",
        )}
        style={{ paddingLeft: `${depth * 16 + 12}px` }}
        onClick={() => node.is_dir ? onToggle(node.path) : onSelect(node.path)}
      >
        {node.is_dir ? (
          isLoading ? <Loader2 className="h-3 w-3 shrink-0 animate-spin" /> :
          isOpen ? <ChevronDown className="h-3 w-3 shrink-0" /> : <ChevronRight className="h-3 w-3 shrink-0" />
        ) : <span className="w-3" />}
        {getFileIcon(node.name, node.is_dir, isOpen)}
        <span className="truncate">{node.name}</span>
        {!node.is_dir && <span className="ml-auto text-micro text-muted-foreground/60 shrink-0">{fmtSize(node.size)}</span>}
      </button>
      {node.is_dir && isOpen && node.children.map((child) => (
        <TreeNodeRow key={child.path} node={child} depth={depth + 1} selectedPath={selectedPath} expandedPaths={expandedPaths} loadingDirs={loadingDirs} onSelect={onSelect} onToggle={onToggle} />
      ))}
    </>
  )
}

function findNode(nodes: TreeNode[], path: string): TreeNode | undefined {
  for (const n of nodes) {
    if (n.path === path) return n
    if (n.is_dir && n.children.length > 0) {
      const found = findNode(n.children, path)
      if (found) return found
    }
  }
  return undefined
}

function flatCount(nodes: TreeNode[]): { fileCount: number; dirCount: number; totalBytes: number } {
  let fileCount = 0, dirCount = 0, totalBytes = 0
  for (const n of nodes) {
    if (n.is_dir) { dirCount++ } else { fileCount++; totalBytes += n.size }
  }
  return { fileCount, dirCount, totalBytes }
}

export function FilesPageClient() {
  const agentId = useAgentId()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const router = useRouter()
  const { agent } = useAgentDetail()
  const crewId = agent?.crew_id ?? null
  // Both workspaceId and agentId are required to hit any
  // /api/v1/agents/{id}/... endpoint. Centralising the conjunction
  // keeps the four existing guard sites (mount effect, save handler,
  // container tab, git tab) from drifting in future edits.
  const canQueryAgent = Boolean(workspaceId && agentId)
  const [tree, setTree] = useState<TreeNode[]>([])
  const [basePrefix, setBasePrefix] = useState("")
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const [fileContent, setFileContent] = useState<string | null>(null)
  const [fileError, setFileError] = useState<string | null>(null)
  const [loadingContent, setLoadingContent] = useState(false)
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(new Set())
  const [loadingDirs, setLoadingDirs] = useState<Set<string>>(new Set())
  const [activeFileTab, setActiveFileTab] = useState<"home" | "container" | "git">("home")
  const [containerFiles, setContainerFiles] = useState<FileEntry[]>([])
  const [containerLoading, setContainerLoading] = useState(false)
  const [containerError, setContainerError] = useState<string | null>(null)
  const [gitCommits, setGitCommits] = useState<{ hash: string; message: string; author: string; date: string }[]>([])
  const [gitLoading, setGitLoading] = useState(false)
  const [gitError, setGitError] = useState<string | null>(null)
  const [search, setSearch] = useState("")
  const [copied, setCopied] = useState(false)
  const [editMode, setEditMode] = useState(false)
  const [isDirty, setIsDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const editorSaveRef = useRef<(() => void) | null>(null)
  const fileAbortRef = useRef<AbortController | null>(null)
  const containerAbortRef = useRef<AbortController | null>(null)
  const gitAbortRef = useRef<AbortController | null>(null)
  const fetchFilesRef = useRef<(() => void) | null>(null)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useRealtimeChannel(crewId ? `crew:${crewId}` : null)

  useEffect(() => {
    if (wsLoading) return
    if (!workspaceId) { setLoading(false); setError("No workspace selected"); return }
    // Flush the previous agent's tree + editor state before we decide
    // whether to fetch. If we leave state in place while agentId is
    // unresolved, the panel flashes the last agent's files until the
    // new fetch resolves — confusing when the two agents have very
    // different workspaces.
    fileAbortRef.current?.abort()
    setTree([])
    setSelectedPath(null)
    setFileContent(null)
    setEditMode(false)
    setIsDirty(false)
    // Clear errors alongside the tree reset. Without this, an error
    // raised against the previous agent (e.g. "Network error. Is the
    // engine running?") stays visible while the new agent's fetch is
    // in flight, making it look like the new agent is failing too.
    setError(null)
    setFileError(null)
    // Without a resolved agentId the legacy pathname would be
    // `/api/v1/agents//files`, which 404s. Short-circuit while the
    // AgentDetailProvider is still resolving.
    if (!agentId) { setLoading(false); return }
    let cancelled = false
    let isFirstLoad = true
    async function fetchFiles() {
      try {
        const res = await fetch(`/api/v1/agents/${agentId}/files?workspace_id=${workspaceId}`)
        if (!res.ok) { if (!cancelled) setError("Failed to load files"); return }
        const data: FileEntry[] | null = await res.json()
        if (!cancelled) {
          const safeData = data ?? []
          if (isFirstLoad) {
            setTree(buildTopLevel(safeData))
            isFirstLoad = false
          } else {
            setTree((prev) => mergeTopLevel(prev, safeData))
          }
          if (safeData.length > 0) {
            const first = safeData[0]
            const idx = first.path.lastIndexOf(first.name)
            setBasePrefix(idx > 0 ? first.path.slice(0, idx) : "")
          }
          setError(null)
        }
      } catch { if (!cancelled) setError("Network error. Is the engine running?") }
      finally { if (!cancelled) setLoading(false) }
    }
    fetchFilesRef.current = fetchFiles
    fetchFiles()
    const interval = setInterval(fetchFiles, 120000)
    return () => { cancelled = true; clearInterval(interval); fetchFilesRef.current = null }
  }, [agentId, workspaceId, wsLoading])

  useRealtimeEvent("file.event", useCallback(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      fetchFilesRef.current?.()
    }, 500)
  }, []))

  useEffect(() => {
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
      fileAbortRef.current?.abort()
      containerAbortRef.current?.abort()
      gitAbortRef.current?.abort()
    }
  }, [])

  // Reset per-tab caches + abort in-flight fetches on agent/workspace switch
  // so the previous agent's container files or git log can't bleed into the
  // new selection. The onClick handlers below re-fetch lazily.
  useEffect(() => {
    containerAbortRef.current?.abort()
    gitAbortRef.current?.abort()
    setActiveFileTab("home")
    setContainerFiles([])
    setContainerLoading(false)
    setContainerError(null)
    setGitCommits([])
    setGitLoading(false)
    setGitError(null)
  }, [agentId, workspaceId])

  const fetchSubdir = useCallback(async (dirPath: string) => {
    if (!canQueryAgent) return
    setLoadingDirs((prev) => new Set(prev).add(dirPath))
    try {
      const relPath = dirPath.startsWith(basePrefix) ? dirPath.slice(basePrefix.length) : dirPath
      const res = await fetch(`/api/v1/agents/${agentId}/files?workspace_id=${workspaceId}&subdir=${encodeURIComponent(relPath)}`)
      if (!res.ok) return
      const data: FileEntry[] | null = await res.json()
      setTree((prev) => insertChildren(prev, dirPath, data ?? []))
    } catch { /* folder contents unavailable — tree shows empty */ }
    finally { setLoadingDirs((prev) => { const next = new Set(prev); next.delete(dirPath); return next }) }
  }, [agentId, workspaceId, canQueryAgent, basePrefix])

  const openFile = useCallback((path: string) => {
    const file = findNode(tree, path)
    if (!file || file.is_dir) return
    fileAbortRef.current?.abort()
    const ac = new AbortController()
    fileAbortRef.current = ac
    setSelectedPath(path)
    setFileContent(null)
    setFileError(null)
    setEditMode(false)
    setIsDirty(false)
    if (!isPreviewable(file.name)) {
      setLoadingContent(false)
      return
    }
    setLoadingContent(true)
    fetch(`/api/v1/agents/${agentId}/files/download?workspace_id=${workspaceId}&path=${encodeURIComponent(path)}`, { signal: ac.signal })
      .then((r) => { if (!r.ok) throw new Error("Unable to load"); return r.text() })
      .then((text) => { if (!ac.signal.aborted) setFileContent(text) })
      .catch((err) => { if (err.name !== "AbortError") { setFileContent(null); setFileError(err.message ?? "Network error") } })
      .finally(() => { if (!ac.signal.aborted) setLoadingContent(false) })
  }, [agentId, workspaceId, tree])

  const handleSave = useCallback(async (content: string) => {
    if (!selectedPath || !canQueryAgent) return
    setSaving(true)
    try {
      const res = await fetch(
        `/api/v1/agents/${agentId}/files/save?workspace_id=${workspaceId}&path=${encodeURIComponent(selectedPath)}`,
        { method: "PUT", body: content }
      )
      if (res.ok) {
        setFileContent(content)
        setEditMode(false)
        setIsDirty(false)
        toast.success("File saved")
      } else {
        toast.error("Failed to save file")
      }
    } catch {
      toast.error("Network error saving file")
    } finally {
      setSaving(false)
    }
  }, [agentId, workspaceId, canQueryAgent, selectedPath])

  const handleDiscard = useCallback(() => {
    setEditMode(false)
    setIsDirty(false)
  }, [])

  const toggleFolder = useCallback((path: string) => {
    setExpandedPaths((prev) => {
      const next = new Set(prev)
      if (next.has(path)) { next.delete(path) } else {
        next.add(path)
        const node = findNode(tree, path)
        if (node && node.is_dir && !node.childrenLoaded) {
          fetchSubdir(path)
        }
      }
      return next
    })
  }, [tree, fetchSubdir])

  const handleDownload = useCallback(() => {
    if (!selectedPath) return
    const file = findNode(tree, selectedPath)
    if (!file) return
    const url = `/api/v1/agents/${agentId}/files/download?workspace_id=${workspaceId}&path=${encodeURIComponent(selectedPath)}`
    const a = document.createElement("a")
    a.href = url; a.download = file.name; a.click()
  }, [agentId, workspaceId, selectedPath, tree])

  const handleCopy = useCallback(() => {
    if (!selectedPath) return
    navigator.clipboard.writeText(selectedPath).catch(() => {})
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }, [selectedPath])

  if (wsLoading || loading) return <FilesSkeleton />

  if (error) {
    return (
      <div className="flex items-center justify-center h-full">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" /><p className="text-body">{error}</p>
        </div>
      </div>
    )
  }

  const { fileCount, dirCount, totalBytes } = flatCount(tree)
  const selectedFile = findNode(tree, selectedPath ?? "")

  const filterTree = (nodes: TreeNode[], q: string): TreeNode[] => {
    if (!q.trim()) return nodes
    const lq = q.toLowerCase()
    return nodes.reduce<TreeNode[]>((acc, n) => {
      if (n.is_dir) {
        const filtered = filterTree(n.children, q)
        if (filtered.length > 0) acc.push({ ...n, children: filtered })
      } else if (n.name.toLowerCase().includes(lq)) {
        acc.push(n)
      }
      return acc
    }, [])
  }
  const filteredTree = filterTree(tree, search)

  return (
    <div className="flex h-full">
      {/* ── Left: File tree (always visible, fixed width) ── */}
      <div className="flex flex-col w-64 shrink-0 border-r overflow-hidden">
        <div className="flex items-center justify-between h-[41px] px-4 border-b shrink-0">
          <div className="flex items-center gap-2">
            <Home className="h-3.5 w-3.5 text-muted-foreground" />
            <span className="text-label font-semibold">Agent Files</span>
            <span className="text-micro text-muted-foreground bg-muted rounded-full px-1.5">{fileCount}</span>
          </div>
          <div className="flex items-center gap-2">
            <StatusDot status="IN_PROGRESS" live className="h-1.5 w-1.5" />
            <span className="text-micro text-muted-foreground">Live</span>
            <RefreshCw className="h-3 w-3 text-muted-foreground cursor-pointer hover:text-foreground ml-1" />
          </div>
        </div>

        <div className="flex items-center gap-1 border-b shrink-0 px-3 py-1.5">
          <button
            className={cn("px-2.5 py-1 text-micro font-medium rounded-md", activeFileTab === "home" ? "bg-accent text-foreground" : "text-muted-foreground hover:bg-accent")}
            onClick={() => setActiveFileTab("home")}
          >Agent Home</button>
          <button
            className={cn("px-2.5 py-1 text-micro rounded-md", activeFileTab === "container" ? "bg-accent text-foreground font-medium" : "text-muted-foreground hover:bg-accent")}
            onClick={() => {
              setActiveFileTab("container")
              if (containerFiles.length === 0 && !containerLoading && canQueryAgent) {
                containerAbortRef.current?.abort()
                const ac = new AbortController()
                containerAbortRef.current = ac
                setContainerError(null)
                setContainerLoading(true)
                fetch(`/api/v1/agents/${agentId}/container-files?workspace_id=${workspaceId}`, {
                  signal: ac.signal,
                })
                  .then((r) => {
                    if (!r.ok) throw new Error(`HTTP ${r.status}`)
                    return r.json()
                  })
                  .then((data) => {
                    if (ac.signal.aborted) return
                    setContainerFiles(Array.isArray(data) ? data : [])
                  })
                  .catch((err) => {
                    if (err instanceof DOMException && err.name === "AbortError") return
                    console.error("failed to load container files", err)
                    setContainerFiles([])
                    setContainerError("Failed to load container files")
                  })
                  .finally(() => {
                    if (!ac.signal.aborted) setContainerLoading(false)
                  })
              }
            }}
          >Container</button>
          <button
            className={cn(
              "px-2.5 py-1 text-micro rounded-md",
              crewId ? "text-muted-foreground hover:bg-accent cursor-pointer" : "text-muted-foreground/40 cursor-not-allowed",
            )}
            title={crewId ? "Browse all crew files" : "No crew assigned"}
            onClick={() => crewId && router.push(`/crews/${crewId}/files`)}
          >Crew</button>
          <button
            className={cn("px-2.5 py-1 text-micro rounded-md flex items-center gap-1", activeFileTab === "git" ? "bg-accent text-foreground font-medium" : "text-muted-foreground hover:bg-accent")}
            onClick={() => {
              setActiveFileTab("git")
              if (gitCommits.length === 0 && !gitLoading && canQueryAgent) {
                gitAbortRef.current?.abort()
                const ac = new AbortController()
                gitAbortRef.current = ac
                setGitError(null)
                setGitLoading(true)
                fetch(`/api/v1/agents/${agentId}/git-log?workspace_id=${workspaceId}`, {
                  signal: ac.signal,
                })
                  .then((r) => {
                    if (!r.ok) throw new Error(`HTTP ${r.status}`)
                    return r.json()
                  })
                  .then((data) => {
                    if (ac.signal.aborted) return
                    setGitCommits(Array.isArray(data) ? data : [])
                  })
                  .catch((err) => {
                    if (err instanceof DOMException && err.name === "AbortError") return
                    console.error("failed to load git log", err)
                    setGitCommits([])
                    setGitError("Failed to load git log")
                  })
                  .finally(() => {
                    if (!ac.signal.aborted) setGitLoading(false)
                  })
              }
            }}
          ><GitBranch className="h-3 w-3" /> Git</button>
        </div>

        {activeFileTab === "home" && (
          <>
            <div className="px-3 py-2 shrink-0">
              <div className="relative">
                <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
                <input
                  className="w-full h-7 rounded-md border border-border bg-card pl-8 pr-3 text-label outline-none placeholder:text-muted-foreground focus:ring-1 focus:ring-ring"
                  placeholder="Filter files..."
                  aria-label="Filter files"
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                />
              </div>
            </div>

            {tree.length === 0 ? (
              <div className="flex-1 flex flex-col items-center justify-center text-center px-4">
                <Inbox className="h-10 w-10 text-muted-foreground/40 mb-3" />
                <p className="text-body font-medium text-muted-foreground">No files yet</p>
                <p className="text-label text-muted-foreground mt-1">Files created by the agent will appear here.</p>
              </div>
            ) : (
              <div className="flex-1 overflow-y-auto">
                {filteredTree.map((node) => (
                  <TreeNodeRow key={node.path} node={node} depth={0} selectedPath={selectedPath} expandedPaths={expandedPaths} loadingDirs={loadingDirs} onSelect={openFile} onToggle={toggleFolder} />
                ))}
                {filteredTree.length === 0 && search && (
                  <p className="px-4 py-8 text-label text-muted-foreground text-center">No files matching &ldquo;{search}&rdquo;</p>
                )}
              </div>
            )}

            <div className="px-3 py-2 border-t text-micro text-muted-foreground flex items-center justify-between shrink-0">
              <span>{fileCount} file{fileCount !== 1 ? "s" : ""}, {dirCount} folder{dirCount !== 1 ? "s" : ""} · {fmtSize(totalBytes)}</span>
              <span>/output/</span>
            </div>
          </>
        )}

        {activeFileTab === "container" && (
          <div className="flex-1 overflow-y-auto">
            {containerLoading ? (
              <div className="flex items-center justify-center py-12"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></div>
            ) : containerError ? (
              <div className="flex-1 flex flex-col items-center justify-center text-center px-4 py-12">
                <AlertCircle className="h-10 w-10 text-destructive/60 mb-3" />
                <p className="text-body font-medium text-destructive">{containerError}</p>
                <p className="text-label text-muted-foreground mt-1">Check that the container is running and try again.</p>
              </div>
            ) : containerFiles.length === 0 ? (
              <div className="flex-1 flex flex-col items-center justify-center text-center px-4 py-12">
                <Inbox className="h-10 w-10 text-muted-foreground/40 mb-3" />
                <p className="text-body font-medium text-muted-foreground">No container files</p>
                <p className="text-label text-muted-foreground mt-1">Start the container to browse its filesystem.</p>
              </div>
            ) : (
              <div className="p-2 space-y-0.5">
                {containerFiles.map((f) => (
                  <div key={f.path} className="flex items-center gap-2 px-2 py-1 rounded text-label hover:bg-accent">
                    {f.is_dir ? <FolderClosed className="h-3.5 w-3.5 text-muted-foreground" /> : <FileText className="h-3.5 w-3.5 text-muted-foreground" />}
                    <span className="truncate flex-1">{f.path}</span>
                    {!f.is_dir && <span className="text-micro text-muted-foreground shrink-0">{fmtSize(f.size)}</span>}
                  </div>
                ))}
              </div>
            )}
          </div>
        )}

        {activeFileTab === "git" && (
          <div className="flex-1 overflow-y-auto">
            {gitLoading ? (
              <div className="flex items-center justify-center py-12"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></div>
            ) : gitError ? (
              <div className="flex-1 flex flex-col items-center justify-center text-center px-4 py-12">
                <AlertCircle className="h-10 w-10 text-destructive/60 mb-3" />
                <p className="text-body font-medium text-destructive">{gitError}</p>
                <p className="text-label text-muted-foreground mt-1">The agent workspace may be unreachable.</p>
              </div>
            ) : gitCommits.length === 0 ? (
              <div className="flex-1 flex flex-col items-center justify-center text-center px-4 py-12">
                <GitBranch className="h-10 w-10 text-muted-foreground/40 mb-3" />
                <p className="text-body font-medium text-muted-foreground">No git history</p>
                <p className="text-label text-muted-foreground mt-1">No git repository found in this agent&apos;s workspace.</p>
              </div>
            ) : (
              <div className="p-2 space-y-1">
                {gitCommits.map((c) => (
                  <div key={c.hash} className="px-2 py-1.5 rounded hover:bg-accent">
                    <p className="text-label font-medium truncate">{c.message}</p>
                    <div className="flex items-center gap-2 text-micro text-muted-foreground mt-0.5">
                      <code className="font-mono">{c.hash.slice(0, 7)}</code>
                      <span>{c.author}</span>
                      <span>{new Date(c.date).toLocaleDateString()}</span>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>

      {/* ── Right: File preview/editor or empty state ── */}
      {selectedPath && selectedFile ? (
        <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
          <div className="flex items-center gap-3 h-[41px] px-4 border-b shrink-0">
            <div className="flex items-center gap-1.5 min-w-0">
              {getFileIcon(selectedFile.name, false)}
              <div className="flex items-center gap-1 text-label min-w-0">
                {selectedPath.split("/").map((seg, i, arr) => (
                  <span key={i} className="flex items-center gap-1 shrink-0">
                    {i > 0 && <ChevronRight className="h-3 w-3 text-muted-foreground" />}
                    <span className={cn(i === arr.length - 1 ? "text-foreground font-medium" : "text-muted-foreground")}>{seg}</span>
                  </span>
                ))}
                {isDirty && <span className="h-2 w-2 rounded-full bg-amber-500 shrink-0" title="Unsaved changes" />}
              </div>
            </div>
            <div className="ml-auto flex items-center gap-2 shrink-0">
              {editMode ? (
                <>
                  <button
                    onClick={handleDiscard}
                    className="h-6 px-2 flex items-center gap-1 rounded text-label text-muted-foreground hover:bg-accent"
                  >
                    <X className="h-3 w-3" /> Discard
                  </button>
                  <button
                    onClick={() => editorSaveRef.current?.()}
                    disabled={saving}
                    className="h-6 px-2 flex items-center gap-1 rounded text-label bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
                  >
                    {saving ? <Loader2 className="h-3 w-3 animate-spin" /> : <Save className="h-3 w-3" />} Save
                  </button>
                </>
              ) : (
                <>
                  <span className="text-micro text-muted-foreground">{fmtSize(selectedFile.size)} · {fmtTime(selectedFile.mod_time)}</span>
                  {isPreviewable(selectedFile.name) && fileContent !== null && (
                    <button onClick={() => setEditMode(true)} className="h-6 w-6 flex items-center justify-center rounded hover:bg-accent" title="Edit file" aria-label="Edit file">
                      <Pencil className="h-3 w-3 text-muted-foreground" />
                    </button>
                  )}
                  <button onClick={handleCopy} className="h-6 w-6 flex items-center justify-center rounded hover:bg-accent" title="Copy path" aria-label="Copy file path">
                    {copied ? <Check className="h-3 w-3 text-primary" /> : <Copy className="h-3 w-3 text-muted-foreground" />}
                  </button>
                  <button onClick={handleDownload} className="h-6 w-6 flex items-center justify-center rounded hover:bg-accent" title="Download" aria-label="Download file">
                    <Download className="h-3 w-3 text-muted-foreground" />
                  </button>
                </>
              )}
            </div>
          </div>

          <div className="flex-1 overflow-hidden">
            {loadingContent ? (
              <div className="flex items-center justify-center h-full">
                <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
              </div>
            ) : !isPreviewable(selectedFile.name) ? (
              <div className="flex flex-col items-center justify-center h-full text-muted-foreground">
                <FileIcon className="h-10 w-10 mb-3 opacity-30" />
                <p className="text-body font-medium">Cannot preview this file</p>
                <p className="text-label mt-1">Binary or unsupported file format.</p>
                <button onClick={handleDownload} className="mt-3 text-label text-primary hover:underline flex items-center gap-1">
                  <Download className="h-3 w-3" /> Download instead
                </button>
              </div>
            ) : editMode && fileContent !== null ? (
              <FileEditor
                code={fileContent}
                language={getLang(selectedFile.name)}
                onSave={handleSave}
                onDirtyChange={setIsDirty}
                saveRef={editorSaveRef}
              />
            ) : fileContent !== null ? (
              <div className="h-full overflow-y-auto dark bg-surface-subtle">
                <CodeBlock code={fileContent} language={getLang(selectedFile.name) as BundledLanguage} showLineNumbers />
              </div>
            ) : fileError ? (
              <div className="flex flex-col items-center justify-center h-full text-muted-foreground">
                <FileIcon className="h-10 w-10 mb-3 opacity-30" />
                <p className="text-body font-medium">{fileError}</p>
              </div>
            ) : null}
          </div>
        </div>
      ) : (
        <div className="flex-1 flex flex-col items-center justify-center text-muted-foreground">
          <FileText className="h-12 w-12 mb-4 opacity-20" />
          <p className="text-body font-medium">Select a file to preview</p>
          <p className="text-label mt-1.5 text-muted-foreground/60">Click any file in the tree to view its contents here.</p>
        </div>
      )}
    </div>
  )
}

function FilesSkeleton() {
  return (
    <div className="flex flex-col h-full">
      <div className="flex items-center justify-between h-[41px] px-4 border-b shrink-0">
        <Skeleton className="h-4 w-32" />
        <Skeleton className="h-4 w-16" />
      </div>
      <div className="px-4 py-2 shrink-0"><Skeleton className="h-8 w-full rounded-lg" /></div>
      <div className="flex-1 px-4 space-y-2 py-2">
        {Array.from({ length: 8 }).map((_, i) => <Skeleton key={i} className="h-7 w-full" />)}
      </div>
    </div>
  )
}
