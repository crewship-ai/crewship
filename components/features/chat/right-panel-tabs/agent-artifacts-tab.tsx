"use client"

import { OutputSheetOffer } from "../artifact-context/sheet/output-sheet-offer"
import { useCallback, useEffect, useRef, useState } from "react"
import { FileCode2, FileImage, FileSpreadsheet, FileText, RefreshCw } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeChannelSafe, useRealtimeEventSafe } from "@/hooks/use-realtime"
import { cn } from "@/lib/utils"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { useArtifactStore } from "@/stores/artifact-store"
import { relativeToAgent } from "../files/file-scope"
import { isClientArtifactPath } from "../artifact/artifact-scope"
import type { FileEntry } from "../chat-tree-row"
import { ChatDocumentAccess } from "./chat-document-access"
import { ChatSheets } from "./chat-sheets"

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
export function AgentArtifactsTab({ agentId, workspaceId, crewId, agentSlug, chatId, viewerId }: {
  agentId: string
  workspaceId: string | null
  crewId?: string | null
  agentSlug?: string | null
  chatId?: string | null
  viewerId?: string | null
}) {
  const key = JSON.stringify([workspaceId, agentId])
  // One result per agent+workspace. A background refresh keeps the current
  // list on screen; a result for a previous agent is never painted.
  const [result, setResult] = useState<{ key: string; files: FileEntry[] | null } | null>(null)
  const [revision, setRevision] = useState(0)
  const debounce = useRef<ReturnType<typeof setTimeout> | null>(null)
  const openArtifact = useArtifactStore((s) => s.openFile)
  const activeId = useArtifactStore((s) => s.activeId)

  useEffect(() => {
    if (!workspaceId) return
    const controller = new AbortController()
    void apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/files?workspace_id=${encodeURIComponent(workspaceId)}&recursive=true`, { signal: controller.signal })
      .then((res) => {
        if (!res.ok) throw new Error(`Files ${res.status}`)
        return res.json() as Promise<FileEntry[]>
      })
      .then((rows) => {
        if (!controller.signal.aborted) setResult({ key, files: Array.isArray(rows) ? rows : [] })
      })
      .catch(() => { if (!controller.signal.aborted) setResult({ key, files: null }) })
    return () => controller.abort()
  }, [agentId, workspaceId, key, revision])

  const reload = useCallback(() => setRevision((n) => n + 1), [])
  const refresh = useCallback(() => {
    if (debounce.current) clearTimeout(debounce.current)
    debounce.current = setTimeout(() => { debounce.current = null; setRevision((n) => n + 1) }, 400)
  }, [])
  useEffect(() => () => { if (debounce.current) clearTimeout(debounce.current) }, [])
  // The watcher publishes file.event on the crew channel; subscribing needs
  // membership in the crew's workspace (internal/ws/channel_auth.go). Only this agent's writes refresh; the listing itself
  // still goes through the files API and its permission check.
  useRealtimeChannelSafe(workspaceId && crewId ? `crew:${crewId}` : null)
  useRealtimeEventSafe("file.event", useCallback((event: { payload: Record<string, unknown> }) => {
    if (agentSlug && event.payload?.agent === agentSlug) refresh()
  }, [agentSlug, refresh]))
  useRealtimeEventSafe("run.completed", useCallback((event: { payload: Record<string, unknown> }) => { if (event.payload?.agent_id === agentId) refresh() }, [agentId, refresh]))
  useRealtimeEventSafe("realtime.reconnected", reload)

  const current = result?.key === key ? result : null
  const loading = !!workspaceId && !current
  const error = !!current && current.files === null
  const files = current?.files ?? []

  const artifacts = [...new Map(files.filter((file) => !file.is_dir
    && isClientArtifactPath(relativeToAgent(file.path, crewId, agentSlug)))
    .map((file) => [file.path, file] as const)).values()]

  return <div className="p-3 text-xs">
    {workspaceId && chatId && viewerId && <ChatSheets workspaceId={workspaceId} chatId={chatId} viewerId={viewerId} />}
    {workspaceId && chatId && viewerId && <ChatDocumentAccess workspaceId={workspaceId} chatId={chatId} viewerId={viewerId} />}
    <div className="mb-2 flex items-center justify-between gap-2">
      <span className="eyebrow">Output files <span className="text-muted-foreground-soft">· {artifacts.length}</span></span>
      <button type="button" onClick={reload} aria-label="Refresh artifacts" className="rounded p-1 hover:bg-accent"><RefreshCw className="size-3" /></button>
    </div>
    {loading ? <p role="status" className="py-3 text-muted-foreground">Loading artifacts…</p>
      : error ? <p role="alert" className="py-3 text-muted-foreground">Artifacts could not be loaded. Try refresh.</p>
      : artifacts.length === 0 ? <InlineEmpty icon={FileText} text="No output files yet." />
      : <ul className="space-y-1.5">{artifacts.map((file) => {
        const ext = extension(file.name)
        const Icon = ext === "csv" || ext === "tsv" || ext === "xlsx" || ext === "xls" ? FileSpreadsheet : ext === "html" || ext === "htm" ? FileCode2 : ["png", "jpg", "jpeg", "webp"].includes(ext) ? FileImage : FileText
        return <li key={file.path}><button type="button" aria-current={activeId === `${agentId}:${file.path}` ? "true" : undefined} className={cn("lift flex w-full items-start gap-2 rounded-xl border border-border bg-card p-2.5 text-left", activeId === `${agentId}:${file.path}` && "border-primary/50 bg-primary/10")} onClick={() => openArtifact({ id: `${agentId}:${file.path}`, agentId, path: file.path, title: file.name })}>
          <Icon className={cn("mt-0.5 size-4 shrink-0", ext === "xlsx" || ext === "xlsm" ? "text-sheet" : "text-primary")} aria-hidden />
          <span className="min-w-0 flex-1"><span className="block truncate font-medium">{file.name}</span><span className="block truncate text-[10px] text-muted-foreground">{fileKind(ext)}{typeof file.size === "number" && ` · ${fileSize(file.size)}`} · Origin unknown</span></span>
          <span className="rounded border border-border px-1 py-0.5 font-mono text-[9px] uppercase text-muted-foreground">{ext}</span>
        </button>{workspaceId && ext === "xlsx" && <div className="mt-1 pl-2"><OutputSheetOffer workspaceId={workspaceId} agentId={agentId} path={file.path} chatId={chatId} viewerId={viewerId} /></div>}</li>
      })}</ul>}
  </div>
}
