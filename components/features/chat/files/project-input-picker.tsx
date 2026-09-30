"use client"

import { useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

export type ProjectInputOption = {
  version_id: string
  name: string
  project_name: string
  size_bytes: number
}

type Props = {
  chatId: string
  workspaceId: string
  userId: string
  selected: string[]
  onChange: (ids: string[]) => void
  disabled?: boolean
  refreshKey: number
}

/** Files are selected for one turn. Bytes and authority remain on the server. */
export function ProjectInputPicker({ chatId, workspaceId, userId, selected, onChange, disabled, refreshKey }: Props) {
  const scope = JSON.stringify([userId, workspaceId, chatId])
  const [snapshot, setSnapshot] = useState<{ scope: string; files: ProjectInputOption[]; available: boolean; more: boolean }>({ scope: "", files: [], available: false, more: false })
  const [search, setSearch] = useState("")
  const [refresh, setRefresh] = useState(0)
  const [error, setError] = useState("")
  const [loading, setLoading] = useState(false)
  const current = snapshot.scope === scope ? snapshot : null

  useEffect(() => { onChange([]) }, [scope, onChange])

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    const query = new URLSearchParams({ workspace_id: workspaceId, search })
    void apiFetch(`/api/v1/chats/${encodeURIComponent(chatId)}/project-input-options?${query}`, { signal: controller.signal }).then(async response => {
      if (!response.ok) {
        if (response.status === 404 || response.status === 403) {
          if (!controller.signal.aborted) { setSnapshot({ scope, files: [], available: false, more: false }); onChange([]); setError("") }
          return
        }
        throw new Error("Project files are unavailable. Refresh and try again.")
      }
      const result = await response.json() as { files: ProjectInputOption[]; has_more: boolean }
      if (!Array.isArray(result.files)) throw new Error("Project files are unavailable.")
      if (!controller.signal.aborted) {
        setSnapshot({ scope, files: result.files, available: true, more: result.has_more }); setError("")
      }
    }).catch(cause => {
      if (!controller.signal.aborted) { setSnapshot({ scope, files: [], available: false, more: false }); onChange([]); setError(cause instanceof Error ? cause.message : "Project files are unavailable.") }
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [scope, chatId, workspaceId, search, refresh, refreshKey, onChange])

  if (!current?.available && !error) return null
  const files = current?.files ?? []
  return <details className="shrink-0 border-t px-4 py-2 text-sm">
    <summary className="cursor-pointer coarse:min-h-12">Project inputs{selected.length ? ` (${selected.length} selected)` : ""}</summary>
    <p className="py-2 text-muted-foreground">Choose up to 16 files for your next message. The agent can read these copies but cannot change them.</p>
    <div className="flex gap-2">
      <input aria-label="Search project files" value={search} onChange={event => setSearch(event.target.value.slice(0, 128))} className="min-w-0 flex-1 rounded border px-2 py-1 coarse:min-h-12" />
      <button type="button" disabled={loading} onClick={() => { onChange([]); setRefresh(value => value + 1) }} className="rounded border px-3 py-1 coarse:min-h-12">Refresh</button>
      {selected.length > 0 && <button type="button" disabled={disabled} onClick={() => onChange([])} className="rounded border px-3 py-1 coarse:min-h-12">Clear selection</button>}
    </div>
    {error && <p role="alert" className="py-2 text-destructive">{error}</p>}
    {!error && files.length === 0 && <p className="py-2 text-muted-foreground">No authorized project files match.</p>}
    <ul className="max-h-40 overflow-auto">{files.map(file => <li key={file.version_id}>
      <label className="flex cursor-pointer items-center gap-2 py-2 coarse:min-h-12">
        <input type="checkbox" checked={selected.includes(file.version_id)} disabled={disabled || loading || selected.length >= 16 && !selected.includes(file.version_id)} onChange={event => onChange(event.target.checked ? [...selected, file.version_id] : selected.filter(id => id !== file.version_id))} />
        <span className="min-w-0 flex-1"><span className="block truncate" title={file.name}>{file.name}</span><span className="text-xs text-muted-foreground">{file.project_name} · {file.size_bytes} bytes</span></span>
      </label>
    </li>)}</ul>
    {current?.more && <p className="py-2 text-muted-foreground">Showing the first 100 matches. Search to narrow the list.</p>}
  </details>
}
