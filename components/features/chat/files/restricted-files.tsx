"use client"

import { useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

type FileVersion = { id: string; name: string; size_bytes: number; sha256: string; created_at: string }

export function RestrictedFiles({ chatId, workspaceId, refreshKey }: { chatId: string; workspaceId: string; refreshKey: number }) {
  const [files, setFiles] = useState<FileVersion[]>([])
  const [error, setError] = useState("")
  const [refresh, setRefresh] = useState(0)
  const [busy, setBusy] = useState<string | null>(null)
  const base = `/api/v1/chats/${encodeURIComponent(chatId)}/restricted-files`
  const query = `?workspace_id=${encodeURIComponent(workspaceId)}`

  useEffect(() => {
    const controller = new AbortController()
    void apiFetch(base + query, { signal: controller.signal }).then(async response => {
      if (!response.ok) throw new Error("Files are unavailable. Your access may have changed.")
      const result = await response.json() as { files: FileVersion[] }
      if (!controller.signal.aborted) { setFiles(result.files); setError("") }
    }).catch(cause => {
      if (!controller.signal.aborted) { setFiles([]); setError(cause instanceof Error ? cause.message : "Files are unavailable.") }
    })
    return () => controller.abort()
  }, [base, query, refreshKey, refresh])

  async function download(file: FileVersion) {
    setBusy(file.id)
    try {
      const response = await apiFetch(`${base}/${encodeURIComponent(file.id)}/download${query}`)
      if (!response.ok) throw new Error("File is unavailable. Your access may have changed.")
      const content = await response.blob()
      if (content.size !== file.size_bytes) throw new Error("Download was interrupted. Refresh and try again.")
      const checksum = await crypto.subtle.digest("SHA-256", await content.arrayBuffer())
      const hash = Array.from(new Uint8Array(checksum), byte => byte.toString(16).padStart(2, "0")).join("")
      if (hash !== file.sha256) throw new Error("Download was interrupted. Refresh and try again.")
      const url = URL.createObjectURL(content)
      const link = document.createElement("a")
      link.href = url
      link.download = file.name.split("/").at(-1) ?? "output"
      link.click()
      URL.revokeObjectURL(url)
      setError("")
    } catch (cause) {
      setFiles([])
      setError(cause instanceof Error ? cause.message : "File is unavailable.")
    } finally { setBusy(null) }
  }

  return <details className="shrink-0 border-t px-4 py-2 text-sm">
    <summary className="cursor-pointer coarse:min-h-12">Output files</summary>
    <button type="button" className="mt-2 rounded border px-3 py-1 coarse:min-h-12" onClick={() => setRefresh(value => value + 1)}>Refresh files</button>
    {error && <p role="alert" className="py-2 text-destructive">{error}</p>}
    {!error && files.length === 0 && <p className="py-2 text-muted-foreground">No output files in this conversation.</p>}
    <ul className="max-h-40 overflow-auto">{files.map(file => <li key={file.id} className="flex items-center gap-2 py-1">
      <span className="min-w-0 flex-1 truncate" title={file.name}>{file.name}</span>
      <span className="text-xs text-muted-foreground">{file.size_bytes} bytes</span>
      <button type="button" disabled={busy !== null} className="rounded border px-3 py-1 coarse:min-h-12" onClick={() => void download(file)} aria-label={`Download ${file.name}`}>{busy === file.id ? "Downloading…" : "Download"}</button>
    </li>)}</ul>
  </details>
}
