"use client"

import { useCallback, useEffect, useSyncExternalStore } from "react"
import { apiFetch } from "@/lib/api-fetch"

export interface WorkspaceData {
  id: string
  name: string
  slug: string
  currentUserRole: string | null
}

interface UseWorkspaceReturn {
  workspaceId: string | null
  workspace: WorkspaceData | null
  workspaces: WorkspaceData[]
  role: string | null
  loading: boolean
  setWorkspaceId: (id: string) => void
  refresh: () => Promise<void>
}

const STORAGE_KEY = "crewship.workspaceId"

interface Snapshot {
  workspaces: WorkspaceData[]
  currentId: string | null
  loading: boolean
}

const INITIAL: Snapshot = { workspaces: [], currentId: null, loading: true }
let snapshot: Snapshot = INITIAL
let fetched = false
let inflight: Promise<void> | null = null

const ssrSnapshot: Snapshot = INITIAL

const listeners = new Set<() => void>()

function emit() {
  for (const l of listeners) l()
}

function setSnapshot(next: Snapshot) {
  snapshot = next
  emit()
}

function readPersistedId(): string | null {
  if (typeof window === "undefined") return null
  try {
    return window.localStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}

function persistId(id: string | null) {
  if (typeof window === "undefined") return
  try {
    if (id) window.localStorage.setItem(STORAGE_KEY, id)
    else window.localStorage.removeItem(STORAGE_KEY)
  } catch {
    /* swallow quota / disabled-storage errors */
  }
}

function loadWorkspaces(): Promise<void> {
  if (inflight) return inflight
  setSnapshot({ ...snapshot, loading: true })
  inflight = (async () => {
    try {
      const res = await apiFetch("/api/v1/workspaces")
      if (!res.ok) {
        setSnapshot({ workspaces: [], currentId: null, loading: false })
        return
      }
      const data = (await res.json()) as WorkspaceData[]
      const list = Array.isArray(data) ? data : []
      const persisted = readPersistedId()
      const persistedValid = !!persisted && list.some((w) => w.id === persisted)
      const next = persistedValid ? persisted! : list[0]?.id ?? null
      if (next && !persistedValid) persistId(next)
      if (!next) persistId(null)
      setSnapshot({ workspaces: list, currentId: next, loading: false })
    } catch {
      setSnapshot({ workspaces: [], currentId: null, loading: false })
    } finally {
      fetched = true
      inflight = null
    }
  })()
  return inflight
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

function getSnapshot() {
  return snapshot
}

function getServerSnapshot() {
  return ssrSnapshot
}

export function useWorkspace(): UseWorkspaceReturn {
  const state = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot)

  useEffect(() => {
    if (!fetched && !inflight) {
      void loadWorkspaces()
    }
  }, [])

  const setWorkspaceId = useCallback((id: string) => {
    if (!snapshot.workspaces.some((w) => w.id === id)) return
    if (snapshot.currentId === id) return
    persistId(id)
    setSnapshot({ ...snapshot, currentId: id })
  }, [])

  const refresh = useCallback(() => {
    fetched = false
    return loadWorkspaces()
  }, [])

  const workspace = state.workspaces.find((w) => w.id === state.currentId) ?? null

  return {
    workspaceId: state.currentId,
    workspace,
    workspaces: state.workspaces,
    role: workspace?.currentUserRole ?? null,
    loading: state.loading,
    setWorkspaceId,
    refresh,
  }
}

export function _resetWorkspaceStoreForTests() {
  snapshot = INITIAL
  fetched = false
  inflight = null
  listeners.clear()
}
