"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

// Routine folders (ODCH-RT-01): GET/POST/PATCH/DELETE .../pipeline-folders and
// POST .../pipeline-folders/move. A folder only organises the panel. A folder
// may carry an icon and sit inside another (#3768, the folder's own menu).

export interface RoutineFolder {
  id: string
  name: string
  color?: string
  icon?: string
  parent_id?: string
  routine_count: number
}

/** PATCH body: an omitted field is kept; "" clears icon or colour; parent_id null moves to the top. */
export interface RoutineFolderPatch {
  name?: string
  icon?: string
  color?: string
  parent_id?: string | null
}

export interface RoutineFolders {
  folders: RoutineFolder[]
  byId: ReadonlyMap<string, { id: string; name: string }>
  refresh: () => Promise<void>
  /** Resolves to an error message, or null once the folder exists. A parent makes it a subfolder. */
  create: (name: string, parentId?: string | null) => Promise<string | null>
  /** Rename, recolour, change the icon or move a folder; resolves to an error message or null. */
  update: (id: string, patch: RoutineFolderPatch) => Promise<string | null>
  /** Delete a folder (its routines become unfiled, its subfolders move up); resolves to an error message or null. */
  remove: (id: string) => Promise<string | null>
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
    async (name: string, parentId?: string | null) => {
      const body = parentId ? { name, parent_id: parentId } : { name }
      const res = await apiFetch(base, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
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

  const update = useCallback(
    async (id: string, patch: RoutineFolderPatch) => {
      const res = await apiFetch(`${base}/${encodeURIComponent(id)}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(patch),
      })
      if (!res.ok) return failure(res, "Could not save the folder")
      await refresh()
      return null
    },
    [base, refresh],
  )

  const remove = useCallback(
    async (id: string) => {
      const res = await apiFetch(`${base}/${encodeURIComponent(id)}`, { method: "DELETE" })
      if (!res.ok) return failure(res, "Could not delete the folder")
      await refresh()
      // Its routines are unfiled now: the routine list carries folder_id.
      onMoved?.()
      return null
    },
    [base, refresh, onMoved],
  )

  const byId = useMemo(() => new Map(folders.map((f) => [f.id, { id: f.id, name: f.name }])), [folders])
  return { folders, byId, refresh, create, move, update, remove }
}
