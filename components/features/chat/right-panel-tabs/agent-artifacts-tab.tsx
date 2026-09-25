"use client"

import { useEffect, useState } from "react"
import { FileCode2, FileSpreadsheet, FileText, RefreshCw } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { useArtifactStore } from "@/stores/artifact-store"
import { classifyAgentFile, relativeToAgent } from "../files/file-scope"
import type { FileEntry } from "../chat-tree-row"

const artifactExtensions = new Set(["pdf", "html", "htm", "csv", "tsv", "xlsx", "xls"])

function extension(name: string) {
  return name.split(".").pop()?.toLowerCase() ?? ""
}

/** Agent-scoped deliverables. The current files API has no creation provenance. */
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
    && artifactExtensions.has(extension(file.name))
    && classifyAgentFile(relativeToAgent(file.path, crewId, agentSlug)) !== "plumbing")
    .map((file) => [file.path, file] as const)).values()]

  return <div className="p-3 text-xs">
    <div className="mb-2 flex items-center justify-between gap-2 text-[10px] uppercase tracking-wider text-muted-foreground">
      <span>Previewable agent files · {artifacts.length}</span>
      <button type="button" onClick={() => setRevision((n) => n + 1)} aria-label="Refresh artifacts" className="rounded p-1 hover:bg-accent"><RefreshCw className="size-3" /></button>
    </div>
    {loading ? <p role="status" className="py-3 text-muted-foreground">Loading artifacts…</p>
      : error ? <p role="alert" className="py-3 text-muted-foreground">Artifacts could not be loaded. Try refresh.</p>
      : artifacts.length === 0 ? <p className="rounded-md border border-dashed p-3 text-muted-foreground">No previewable documents found in this agent&apos;s files yet.</p>
      : <ul className="space-y-1.5">{artifacts.map((file) => {
        const ext = extension(file.name)
        const Icon = ext === "csv" || ext === "tsv" || ext === "xlsx" || ext === "xls" ? FileSpreadsheet : ext === "html" || ext === "htm" ? FileCode2 : FileText
        return <li key={file.path}><button type="button" className="flex w-full items-start gap-2 rounded-md border bg-muted/20 p-2 text-left hover:border-primary/40 hover:bg-accent" onClick={() => openArtifact({ id: `${agentId}:${file.path}`, agentId, path: file.path, title: file.name })}>
          <Icon className="mt-0.5 size-4 shrink-0 text-primary" aria-hidden />
          <span className="min-w-0 flex-1"><span className="block truncate font-medium">{file.name}</span><span className="block truncate text-[10px] text-muted-foreground">{relativeToAgent(file.path, crewId, agentSlug)}</span></span>
          <span className="rounded border px-1 py-0.5 text-[9px] uppercase text-muted-foreground">{ext}</span>
        </button></li>
      })}</ul>}
    <p className="mt-3 leading-relaxed text-muted-foreground">This view lists previewable files from the selected agent. File creation history is not available yet.</p>
  </div>
}
