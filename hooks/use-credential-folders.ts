"use client"

import * as React from "react"

import { apiFetch } from "@/lib/api-fetch"
import type { CredentialFolder, FolderRevealPolicy } from "@/lib/credentials/folders"

export interface FolderDraft {
  name: string
  parent_id?: string | null
  description?: string
  reveal_policy?: FolderRevealPolicy
  remind_after_days?: number
  min_security_level?: number
  default_security_level?: number | null
}

async function folderRequest(ws: string, path: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers)
  headers.set("X-Workspace-ID", ws)
  if (init.body) headers.set("Content-Type", "application/json")
  const res = await apiFetch(`/api/v1/credential-folders${path}`, { ...init, headers })
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    const msg = typeof body?.error === "string" ? body.error.slice(0, 300) : `The folder request failed (${res.status}).`
    throw new Error(msg)
  }
  return res
}

/**
 * The workspace's Vault folders, plus create / update / delete that refresh
 * them. A failed list is "no folders" — the Vault stays usable without them —
 * and a response for a workspace that is no longer current is dropped.
 */
export function useCredentialFolders(ws: string | null | undefined, enabled = true) {
  const on = enabled && !!ws
  const [folders, setFolders] = React.useState<CredentialFolder[]>([])
  const [loading, setLoading] = React.useState(on)
  const current = React.useRef(ws)
  current.current = ws

  const refresh = React.useCallback(async (signal?: AbortSignal) => {
    if (!on || !ws) return
    try {
      const list = (await (await folderRequest(ws, "", { signal })).json()) as CredentialFolder[]
      if (current.current === ws && Array.isArray(list)) setFolders(list)
    } catch {
      if (!signal?.aborted && current.current === ws) setFolders([])
    } finally {
      if (current.current === ws) setLoading(false)
    }
  }, [on, ws])

  React.useEffect(() => {
    if (!on) { setFolders([]); setLoading(false); return }
    const ctl = new AbortController()
    setLoading(true)
    void refresh(ctl.signal)
    return () => ctl.abort()
  }, [on, refresh])

  return {
    folders,
    loading,
    create: async (draft: FolderDraft): Promise<CredentialFolder> => {
      const folder = (await (await folderRequest(ws!, "", { method: "POST", body: JSON.stringify(draft) })).json()) as CredentialFolder
      await refresh()
      return folder
    },
    update: async (id: string, patch: Partial<FolderDraft>): Promise<CredentialFolder> => {
      const folder = (await (await folderRequest(ws!, `/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify(patch) })).json()) as CredentialFolder
      await refresh()
      return folder
    },
    remove: async (id: string): Promise<void> => {
      await folderRequest(ws!, `/${encodeURIComponent(id)}`, { method: "DELETE" })
      await refresh()
    },
    refresh: () => refresh(),
  }
}
