"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

// Routine folders (ODCH-RT-01): GET/POST/PATCH/DELETE .../pipeline-folders and
// POST .../pipeline-folders/move. A folder only organises the panel.

export interface RoutineFolder {
  id: string
  name: string
  color?: string
  routine_count: number
}

export interface RoutineFolders {
  folders: RoutineFolder[]
  byId: ReadonlyMap<string, { id: string; name: string }>
  refresh: () => Promise<void>
  /** Resolves to an error message, or null once the folder exists. */
  create: (name: string) => Promise<string | null>
  /** Files routines in a folder, or out of any with folderId null; resolves to an error message or null. */
  move: (slugs: string[], folderId: string | null) => Promise<string | null>
}

async function failure(res: Response, fallback: string): Promise<string> {
  try {
    const body = (await res.json()) as { error?: string; detail?: string }
    return body.error || body.detail || fallback
  } catch {
    return fallback
  }
}

export function useRoutineFolders(workspaceId: string, onMoved?: () => void): RoutineFolders {
  const [folders, setFolders] = useState<RoutineFolder[]>([])
  const base = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-folders`

  const refresh = useCallback(async () => {
    try {
      const res = await apiFetch(base)
      if (!res.ok) return
      const body = await res.json()
      setFolders(Array.isArray(body) ? body : [])
    } catch {
      /* the panel falls back to its other groupings */
    }
  }, [base])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const create = useCallback(
    async (name: string) => {
      const res = await apiFetch(base, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }) })
      if (!res.ok) return failure(res, "Could not create the folder")
      await refresh()
      return null
    },
    [base, refresh],
  )

  const move = useCallback(
    async (slugs: string[], folderId: string | null) => {
      const res = await apiFetch(`${base}/move`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ slugs, folder_id: folderId }),
      })
      if (!res.ok) return failure(res, "Could not move the routines")
      await refresh()
      onMoved?.()
      return null
    },
    [base, refresh, onMoved],
  )

  const byId = useMemo(() => new Map(folders.map((f) => [f.id, { id: f.id, name: f.name }])), [folders])
  return { folders, byId, refresh, create, move }
}
