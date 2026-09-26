"use client"

import { useEffect, useState } from "react"
import { FileCode2, FileImage, FileSpreadsheet, FileText, RefreshCw } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"
import { useArtifactStore } from "@/stores/artifact-store"
import { relativeToAgent } from "../files/file-scope"
import { isClientArtifactPath } from "../artifact/artifact-scope"
import type { FileEntry } from "../chat-tree-row"

function extension(name: string) {
  return name.split(".").pop()?.toLowerCase() ?? ""
}

function fileKind(ext: string) {
  if (["csv", "tsv", "xlsx", "xls"].includes(ext)) return "Spreadsheet"
  if (["html", "htm"].includes(ext)) return "Prototype"
  if (["png", "jpg", "jpeg", "webp"].includes(ext)) return "Image"
  return "Document"
}

function fileSize(bytes: number) {
  if (!Number.isFinite(bytes) || bytes < 0) return ""
  return bytes < 1024 ? `${bytes} B` : `${Math.round(bytes / 1024)} KB`
}

/** Agent-scoped deliverables. The files API does not yet record provenance. */
export function AgentArtifactsTab({ agentId, workspaceId, crewId, agentSlug }: {
  agentId: string
  workspaceId: string | null
  crewId?: string | null
  agentSlug?: string | null
}) {
  const [files, setFiles] = useState<FileEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)
  const [revision, setRevision] = useState(0)
  const openArtifact = useArtifactStore((s) => s.openFile)
  const activeId = useArtifactStore((s) => s.activeId)

  useEffect(() => {
    if (!workspaceId) return
    const controller = new AbortController()
    setFiles([])
    setLoading(true)
    setError(false)
    void apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/files?workspace_id=${encodeURIComponent(workspaceId)}&recursive=true`, { signal: controller.signal })
      .then((res) => {
        if (!res.ok) throw new Error(`Files ${res.status}`)
        return res.json() as Promise<FileEntry[]>
      })
      .then((rows) => {
        if (!controller.signal.aborted) setFiles(Array.isArray(rows) ? rows : [])
      })
      .catch(() => { if (!controller.signal.aborted) setError(true) })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [agentId, workspaceId, revision])

  const artifacts = [...new Map(files.filter((file) => !file.is_dir
    && isClientArtifactPath(relativeToAgent(file.path, crewId, agentSlug)))
    .map((file) => [file.path, file] as const)).values()]

  return <div className="p-3 text-xs">
    <div className="mb-2 flex items-center justify-between gap-2 text-[10px] uppercase tracking-wider text-muted-foreground">
      <span>Documents and outputs · {artifacts.length}</span>
      <button type="button" onClick={() => setRevision((n) => n + 1)} aria-label="Refresh artifacts" className="rounded p-1 hover:bg-accent"><RefreshCw className="size-3" /></button>
    </div>
    {loading ? <p role="status" className="py-3 text-muted-foreground">Loading artifacts…</p>
      : error ? <p role="alert" className="py-3 text-muted-foreground">Artifacts could not be loaded. Try refresh.</p>
      : artifacts.length === 0 ? <p className="rounded-md border border-dashed p-3 text-muted-foreground">No artifacts yet.</p>
      : <ul className="space-y-1.5">{artifacts.map((file) => {
        const ext = extension(file.name)
        const Icon = ext === "csv" || ext === "tsv" || ext === "xlsx" || ext === "xls" ? FileSpreadsheet : ext === "html" || ext === "htm" ? FileCode2 : ["png", "jpg", "jpeg", "webp"].includes(ext) ? FileImage : FileText
        return <li key={file.path}><button type="button" aria-current={activeId === `${agentId}:${file.path}` ? "true" : undefined} className={cn("flex w-full items-start gap-2 rounded-md border bg-muted/20 p-2 text-left hover:border-primary/40 hover:bg-accent", activeId === `${agentId}:${file.path}` && "border-primary/50 bg-primary/10")} onClick={() => openArtifact({ id: `${agentId}:${file.path}`, agentId, path: file.path, title: file.name })}>
          <Icon className="mt-0.5 size-4 shrink-0 text-primary" aria-hidden />
          <span className="min-w-0 flex-1"><span className="block truncate font-medium">{file.name}</span><span className="block truncate text-[10px] text-muted-foreground">{fileKind(ext)}{typeof file.size === "number" && ` · ${fileSize(file.size)}`}</span></span>
          <span className="rounded border px-1 py-0.5 text-[9px] uppercase text-muted-foreground">{ext}</span>
        </button></li>
      })}</ul>}
  </div>
}
