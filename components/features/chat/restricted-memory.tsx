"use client"

import { useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

type Entry = { id: string; kind: string; content: string; created_at: string; created_by?: string; sources?: string[] }

export function RestrictedMemory({ chatId, workspaceId, userId, refreshKey }: { chatId: string; workspaceId: string; userId?: string; refreshKey: number }) {
  const [entries, setEntries] = useState<Entry[]>([])
  const [search, setSearch] = useState("")
  const [draft, setDraft] = useState("")
  const [error, setError] = useState("")
  const [busy, setBusy] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const base = `/api/v1/chats/${encodeURIComponent(chatId)}`
  const query = `?workspace_id=${encodeURIComponent(workspaceId)}`

  useEffect(() => {
    const controller = new AbortController()
    void apiFetch(`${base}/restricted-context${query}&kind=memory&search=${encodeURIComponent(search)}`, { signal: controller.signal }).then(async response => {
      if (!response.ok) throw new Error("Memory is unavailable. Your access may have changed.")
      const result = await response.json() as { entries: Entry[] }
      if (!controller.signal.aborted) { setEntries(result.entries); setError("") }
    }).catch(cause => {
      if (!controller.signal.aborted) { setEntries([]); setError(cause instanceof Error ? cause.message : "Memory is unavailable.") }
    })
    return () => controller.abort()
  }, [base, query, search, refresh, refreshKey])

  async function change(id?: string) {
    setBusy(true)
    try {
      const response = await apiFetch(`${base}/restricted-memory${id ? `/${encodeURIComponent(id)}` : ""}${query}`, {
        method: id ? "DELETE" : "POST",
        ...(id ? {} : { headers: { "Content-Type": "application/json" }, body: JSON.stringify({ content: draft }) }),
      })
      if (!response.ok) throw new Error("Memory could not be changed. Refresh to check your access.")
      if (!id) setDraft("")
      setRefresh(value => value + 1)
    } catch (cause) { setEntries([]); setError(cause instanceof Error ? cause.message : "Memory could not be changed.") }
    finally { setBusy(false) }
  }

  async function exportContext() {
    setBusy(true)
    try {
      // Export performs a fresh authorization; the UI cache is not authority.
      const response = await apiFetch(`${base}/restricted-context${query}`)
      if (!response.ok) throw new Error("Context export is unavailable. Your access may have changed.")
      const result = await response.json() as { entries: Entry[]; limit: number }
      const url = URL.createObjectURL(new Blob([JSON.stringify(result, null, 2)], { type: "application/json" }))
      const link = document.createElement("a"); link.href = url; link.download = "conversation-context.json"; link.click(); URL.revokeObjectURL(url)
    } catch (cause) { setEntries([]); setError(cause instanceof Error ? cause.message : "Context export is unavailable.") }
    finally { setBusy(false) }
  }

  return <details className="shrink-0 border-t px-4 py-2 text-sm">
    <summary className="cursor-pointer coarse:min-h-12">Conversation memory</summary>
    <p className="py-2 text-xs text-muted-foreground">Notes are used in this conversation. Exports include the latest 256 authorized context versions.</p>
    <input aria-label="Search conversation memory" className="w-full rounded border bg-background p-2 coarse:min-h-12" value={search} maxLength={256} onChange={event => setSearch(event.target.value)} />
    {error && <p role="alert" className="py-2 text-destructive">{error}</p>}
    <ul className="max-h-40 overflow-auto">{entries.map(entry => <li key={entry.id} className="border-b py-2">
      <p className="whitespace-pre-wrap break-words">{entry.content}</p>
      <span className="text-xs text-muted-foreground">{entry.created_at}</span>
      {entry.created_by === userId && <button type="button" className="ml-2 rounded border px-3 py-1 coarse:min-h-12" disabled={busy} onClick={() => void change(entry.id)}>Remove note</button>}
    </li>)}</ul>
    <textarea aria-label="New conversation memory" className="mt-2 w-full rounded border bg-background p-2" value={draft} maxLength={8192} onChange={event => setDraft(event.target.value)} />
    <div className="flex flex-wrap gap-2 py-2">
      <button type="button" className="rounded border px-3 py-1 coarse:min-h-12" disabled={busy || !draft.trim()} onClick={() => void change()}>Save note</button>
      <button type="button" className="rounded border px-3 py-1 coarse:min-h-12" disabled={busy} onClick={() => setRefresh(value => value + 1)}>Refresh memory</button>
      <button type="button" className="rounded border px-3 py-1 coarse:min-h-12" disabled={busy} onClick={() => void exportContext()}>Export context versions</button>
    </div>
  </details>
}
