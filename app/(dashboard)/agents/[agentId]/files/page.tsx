"use client"

import { use, useState, useEffect, useCallback } from "react"
import { Download, AlertCircle, Inbox, FolderOpen, Copy, Check, FileIcon } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Artifact,
  ArtifactHeader,
  ArtifactTitle,
  ArtifactActions,
  ArtifactContent,
  ArtifactClose,
} from "@/components/ai-elements/artifact"
import { CodeBlock } from "@/components/ai-elements/code-block"
import type { BundledLanguage } from "shiki"
import { useWorkspace } from "@/hooks/use-workspace"

interface FileEntry {
  path: string
  name: string
  size: number
  is_dir: boolean
  mod_time: string
}

function formatFileSize(bytes: number): string {
  if (bytes === 0) return "0 B"
  const units = ["B", "KB", "MB", "GB"]
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  const value = bytes / Math.pow(1024, i)
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[i]}`
}

function getLanguage(name: string): string {
  const ext = name.split(".").pop()?.toLowerCase() ?? ""
  const map: Record<string, string> = {
    ts: "typescript", tsx: "tsx", js: "javascript", jsx: "jsx",
    py: "python", go: "go", rs: "rust", sh: "bash",
    json: "json", yaml: "yaml", yml: "yaml", xml: "xml",
    html: "html", css: "css", md: "markdown", txt: "text",
    sql: "sql", toml: "toml",
  }
  return map[ext] ?? "text"
}

function totalSize(files: FileEntry[]): string {
  const total = files.reduce((sum, f) => sum + (f.is_dir ? 0 : f.size), 0)
  return formatFileSize(total)
}

function isRecent(modTime: string): boolean {
  return Date.now() - new Date(modTime).getTime() < 5 * 60 * 1000
}

function formatModTime(modTime: string): string {
  const minutes = Math.floor((Date.now() - new Date(modTime).getTime()) / 60000)
  if (minutes < 1) return "Just now"
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  return "Yesterday"
}

export default function FilesPage({ params }: { params: Promise<{ agentId: string }> }) {
  const { agentId } = use(params)
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [files, setFiles] = useState<FileEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const [fileContent, setFileContent] = useState<string | null>(null)
  const [loadingContent, setLoadingContent] = useState(false)
  const [copiedPath, setCopiedPath] = useState<string | null>(null)

  useEffect(() => {
    if (!workspaceId) return
    let cancelled = false
    async function fetchFiles() {
      try {
        const res = await fetch(`/api/v1/agents/${agentId}/files?workspace_id=${workspaceId}`)
        if (!res.ok) { if (!cancelled) setError("Failed to load files"); return }
        const data: FileEntry[] = await res.json()
        if (!cancelled) setFiles(data)
      } catch { if (!cancelled) setError("Network error. Is crewshipd running?") }
      finally { if (!cancelled) setLoading(false) }
    }
    fetchFiles()
    const interval = setInterval(fetchFiles, 10000)
    return () => { cancelled = true; clearInterval(interval) }
  }, [agentId, workspaceId])

  const handleFileSelect = useCallback((path: string) => {
    const file = files.find((f) => f.path === path)
    if (!file || file.is_dir) return
    setSelectedPath(path)
    setLoadingContent(true)
    setFileContent(null)
    fetch(`/api/v1/agents/${agentId}/files/download?workspace_id=${workspaceId}&path=${encodeURIComponent(path)}`)
      .then((res) => res.ok ? res.text() : "(Unable to load)")
      .then((text) => setFileContent(text))
      .catch(() => setFileContent("(Network error)"))
      .finally(() => setLoadingContent(false))
  }, [agentId, workspaceId, files])

  const handleDownload = useCallback((path: string, name: string) => {
    const url = `/api/v1/agents/${agentId}/files/download?workspace_id=${workspaceId}&path=${encodeURIComponent(path)}`
    const a = document.createElement("a")
    a.href = url; a.download = name; a.click()
  }, [agentId, workspaceId])

  const handleCopyPath = useCallback((path: string) => {
    navigator.clipboard.writeText(path).catch(() => {})
    setCopiedPath(path)
    setTimeout(() => setCopiedPath(null), 2000)
  }, [])

  if (wsLoading || loading) return <FilesSkeleton />

  if (error) {
    return (
      <div className="p-4 sm:p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" /><p className="text-sm">{error}</p>
        </div>
      </div>
    )
  }

  const fileCount = files.filter((f) => !f.is_dir).length
  const dirCount = files.filter((f) => f.is_dir).length
  const dirs = files.filter((f) => f.is_dir)
  const plainFiles = files.filter((f) => !f.is_dir)
  const selectedFile = files.find((f) => f.path === selectedPath)

  return (
    <div className="p-4 sm:p-6 space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-sm">
          <FolderOpen className="h-4 w-4 text-muted-foreground" />
          <span className="text-muted-foreground">/output/</span>
          <span className="font-medium">{agentId.slice(0, 12)}</span>
        </div>
        <div className="flex items-center gap-2">
          <div className="flex items-center gap-1.5 text-xs text-[#4ECDC4] font-medium">
            <span className="h-1.5 w-1.5 rounded-full bg-[#4ECDC4] animate-pulse" />
            Real-time (fsnotify)
          </div>

        </div>
      </div>

      {files.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <Inbox className="h-10 w-10 text-muted-foreground/50 mb-3" />
          <p className="text-sm font-medium text-muted-foreground">No files yet</p>
          <p className="text-xs text-muted-foreground mt-1">Files created by the agent will appear here.</p>
        </div>
      ) : (
        <div className="border rounded-lg overflow-hidden">
          {dirs.map((d) => (
            <div key={d.path} className="px-6 py-3 bg-muted/30 border-b flex items-center gap-3 cursor-pointer hover:bg-muted/50">
              <FolderOpen className="h-4 w-4 text-amber-500" />
              <span className="text-sm font-medium">{d.name}/</span>
              <span className="text-xs text-muted-foreground">
                {files.filter((f) => !f.is_dir && f.path.startsWith(d.path + "/")).length} files
              </span>
            </div>
          ))}
          <div className="divide-y">
            {plainFiles.map((f) => (
              <div
                key={f.path}
                className={`px-6 py-3 flex items-center justify-between hover:bg-muted/30 cursor-pointer ${
                  selectedPath === f.path ? "bg-primary/5 border-l-2 border-l-primary" : ""
                }`}
                onClick={() => handleFileSelect(f.path)}
              >
                <div className="flex items-center gap-3 min-w-0">
                  <FileIcon className="h-4 w-4 text-muted-foreground shrink-0" />
                  <div className="min-w-0">
                    <div className="text-sm font-medium flex items-center gap-2">
                      <span className="truncate">{f.name}</span>
                      {isRecent(f.mod_time) && (
                        <Badge className="bg-primary/10 text-primary text-[9px] font-semibold px-1.5 py-0">NEW</Badge>
                      )}
                    </div>
                    <div className="text-xs text-muted-foreground">
                      {formatFileSize(f.size)} · Modified {formatModTime(f.mod_time)}
                    </div>
                  </div>
                </div>
                <div className="flex items-center gap-2 shrink-0 ml-4">
                  <button className="text-xs text-primary hover:underline" onClick={(e) => { e.stopPropagation(); handleDownload(f.path, f.name) }}>Download</button>
                  <button
                    className="text-xs text-muted-foreground hover:text-foreground flex items-center gap-1"
                    onClick={(e) => { e.stopPropagation(); handleCopyPath(f.path) }}
                  >
                    {copiedPath === f.path ? <Check className="h-3 w-3 text-emerald-500" /> : <Copy className="h-3 w-3" />}
                  </button>
                </div>
              </div>
            ))}
          </div>
          <div className="px-6 py-3 border-t bg-muted/30 flex items-center justify-between text-xs text-muted-foreground">
            <span>{fileCount} file{fileCount !== 1 ? "s" : ""}{dirCount > 0 ? `, ${dirCount} folder${dirCount !== 1 ? "s" : ""}` : ""} · Total: {totalSize(files)}</span>
            <span>Storage: /output/ (persistent)</span>
          </div>
        </div>
      )}

      {selectedFile && !selectedFile.is_dir && (
        <Artifact>
          <ArtifactHeader>
            <ArtifactTitle>{selectedFile.name}</ArtifactTitle>
            <div className="flex items-center gap-2">
              <Badge variant="outline" className="text-xs">{formatFileSize(selectedFile.size)}</Badge>
              <ArtifactActions>
                <Button variant="ghost" size="icon" className="h-7 w-7" onClick={() => handleDownload(selectedFile.path, selectedFile.name)}>
                  <Download className="h-3.5 w-3.5" />
                </Button>
              </ArtifactActions>
              <ArtifactClose onClick={() => setSelectedPath(null)} />
            </div>
          </ArtifactHeader>
          <ArtifactContent>
            {loadingContent ? (
              <div className="p-4"><Skeleton className="h-40 w-full" /></div>
            ) : fileContent !== null ? (
              <CodeBlock code={fileContent} language={getLanguage(selectedFile.name) as BundledLanguage} showLineNumbers />
            ) : null}
          </ArtifactContent>
        </Artifact>
      )}
    </div>
  )
}

function FilesSkeleton() {
  return (
    <div className="p-4 sm:p-6 space-y-4">
      <div className="flex items-center justify-between">
        <Skeleton className="h-5 w-48" /><Skeleton className="h-8 w-36" />
      </div>
      <div className="border rounded-lg">
        {Array.from({ length: 6 }).map((_, i) => (
          <div key={i} className="px-6 py-3 border-b last:border-b-0"><Skeleton className="h-10 w-full" /></div>
        ))}
      </div>
    </div>
  )
}
