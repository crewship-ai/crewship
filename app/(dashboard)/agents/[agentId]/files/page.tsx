"use client"

import { use, useState, useEffect, useCallback } from "react"
import { Download, AlertCircle, Inbox, FolderOpen } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import {
  FileTree,
  FileTreeFolder,
  FileTreeFile,
} from "@/components/ai-elements/file-tree"
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
import { useOrg } from "@/hooks/use-org"

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
    sql: "sql", toml: "toml", dockerfile: "dockerfile",
  }
  return map[ext] ?? "text"
}

function totalSize(files: FileEntry[]): string {
  const total = files.reduce((sum, f) => sum + (f.is_dir ? 0 : f.size), 0)
  return formatFileSize(total)
}

export default function FilesPage({ params }: { params: Promise<{ agentId: string }> }) {
  const { agentId } = use(params)
  const { orgId, loading: orgLoading } = useOrg()
  const [files, setFiles] = useState<FileEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const [fileContent, setFileContent] = useState<string | null>(null)
  const [loadingContent, setLoadingContent] = useState(false)

  useEffect(() => {
    if (!orgId) return
    let cancelled = false

    async function fetchFiles() {
      try {
        const res = await fetch(`/api/v1/agents/${agentId}/files?org_id=${orgId}`)
        if (!res.ok) {
          if (!cancelled) setError("Failed to load files")
          return
        }
        const data: FileEntry[] = await res.json()
        if (!cancelled) setFiles(data)
      } catch {
        if (!cancelled) setError("Network error. Is crewshipd running?")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchFiles()
    return () => { cancelled = true }
  }, [agentId, orgId])

  const handleFileSelect = useCallback((path: string) => {
    const file = files.find((f) => f.path === path)
    if (!file || file.is_dir) return

    setSelectedPath(path)
    setLoadingContent(true)
    setFileContent(null)

    fetch(`/api/v1/agents/${agentId}/files/download?org_id=${orgId}&path=${encodeURIComponent(path)}`)
      .then((res) => res.ok ? res.text() : "(Unable to load file content)")
      .then((text) => setFileContent(text))
      .catch(() => setFileContent("(Network error loading file)"))
      .finally(() => setLoadingContent(false))
  }, [agentId, orgId, files])

  const handleDownload = useCallback((path: string, name: string) => {
    const url = `/api/v1/agents/${agentId}/files/download?org_id=${orgId}&path=${encodeURIComponent(path)}`
    const a = document.createElement("a")
    a.href = url
    a.download = name
    a.click()
  }, [agentId, orgId])

  if (orgLoading || loading) {
    return <FilesSkeleton />
  }

  if (error) {
    return (
      <div className="p-4 sm:p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-sm">{error}</p>
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
    <div className="flex h-full">
      {/* Left: File tree */}
      <div className="w-64 sm:w-72 border-r flex flex-col">
        <div className="flex items-center gap-1.5 text-sm text-muted-foreground px-4 py-3 border-b">
          <FolderOpen className="h-4 w-4" />
          <span>/output/</span>
        </div>

        {files.length === 0 ? (
          <div className="flex flex-col items-center justify-center flex-1 py-16 text-center px-4">
            <Inbox className="h-10 w-10 text-muted-foreground/50 mb-3" />
            <p className="text-sm font-medium text-muted-foreground">No files yet</p>
            <p className="text-xs text-muted-foreground mt-1">Files created by the agent will appear here.</p>
          </div>
        ) : (
          <div className="flex-1 overflow-y-auto p-2">
            <FileTree
              selectedPath={selectedPath ?? undefined}
              onSelect={handleFileSelect}
            >
              {dirs.map((d) => (
                <FileTreeFolder key={d.path} name={d.name} path={d.path} />
              ))}
              {plainFiles.map((f) => (
                <FileTreeFile key={f.path} name={f.name} path={f.path} />
              ))}
            </FileTree>
          </div>
        )}

        {files.length > 0 && (
          <div className="border-t px-4 py-2">
            <p className="text-xs text-muted-foreground">
              {fileCount} file{fileCount !== 1 ? "s" : ""}
              {dirCount > 0 ? `, ${dirCount} dir${dirCount !== 1 ? "s" : ""}` : ""}
              {" · "}{totalSize(files)}
            </p>
          </div>
        )}
      </div>

      {/* Right: File preview using Artifact */}
      <div className="flex-1 overflow-hidden">
        {selectedFile && !selectedFile.is_dir ? (
          <Artifact>
            <ArtifactHeader>
              <ArtifactTitle>{selectedFile.name}</ArtifactTitle>
              <div className="flex items-center gap-2">
                <Badge variant="outline" className="text-xs">
                  {formatFileSize(selectedFile.size)}
                </Badge>
                <ArtifactActions>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    onClick={() => handleDownload(selectedFile.path, selectedFile.name)}
                  >
                    <Download className="h-3.5 w-3.5" />
                  </Button>
                </ArtifactActions>
                <ArtifactClose onClick={() => setSelectedPath(null)} />
              </div>
            </ArtifactHeader>
            <ArtifactContent>
              {loadingContent ? (
                <div className="p-4">
                  <Skeleton className="h-40 w-full" />
                </div>
              ) : fileContent !== null ? (
                <CodeBlock
                  code={fileContent}
                  language={getLanguage(selectedFile.name) as BundledLanguage}
                  showLineNumbers
                />
              ) : null}
            </ArtifactContent>
          </Artifact>
        ) : (
          <div className="flex items-center justify-center h-full text-sm text-muted-foreground">
            Select a file to preview
          </div>
        )}
      </div>
    </div>
  )
}

function FilesSkeleton() {
  return (
    <div className="flex h-full">
      <div className="w-64 sm:w-72 border-r p-4 space-y-3">
        <Skeleton className="h-5 w-24" />
        {Array.from({ length: 6 }).map((_, i) => (
          <Skeleton key={i} className="h-6 w-full" />
        ))}
      </div>
      <div className="flex-1 flex items-center justify-center">
        <p className="text-sm text-muted-foreground">Select a file to preview</p>
      </div>
    </div>
  )
}
