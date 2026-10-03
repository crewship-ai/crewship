"use client"

import { useCallback, useEffect, useSyncExternalStore } from "react"

import { navigationAllowed } from "@/hooks/use-navigation-guard"
import { apiFetch } from "@/lib/api-fetch"

export interface WorkspaceData {
  currentUserAccessMode?: "trusted" | "restricted"
  pages_theme?: Partial<import("@/lib/pages/theme").PageTheme> | null
  id: string
  name: string
  slug: string
  currentUserRole: string | null
  /** Resolved per-membership capability grants (#1034). Optional so
   *  a client talking to an older backend degrades to role-only
   *  gating instead of crashing. */
  currentUserCapabilities?: string[] | null
}

interface UseWorkspaceReturn {
  workspaceId: string | null
  workspace: WorkspaceData | null
  workspaces: WorkspaceData[]
  role: string | null
  /** The caller's capability grants in the selected workspace, or
   *  null when unknown (older backend / not loaded yet). */
  capabilities: string[] | null
  loading: boolean
  /** The last load failed and no list is held: what the caller may reach is
   *  unknown. `useAccessMode` reads this as "still loading" (fail closed). */
  error: boolean
  setWorkspaceId: (id: string) => void
  refresh: () => Promise<void>
}

const STORAGE_KEY = "crewship.workspaceId"

interface Snapshot {
  workspaces: WorkspaceData[]
  currentId: string | null
  loading: boolean
  error: boolean
}

const INITIAL: Snapshot = { workspaces: [], currentId: null, loading: true, error: false }
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

// A failed re-read keeps the list it already has: the session is the same
// session, and wiping the list would read as "no memberships" (and so as a
// trusted session) on a single 5xx. Only a cold load with nothing to keep
// settles on an empty, errored snapshot.
function failLoad() {
  if (snapshot.workspaces.length > 0) {
    if (snapshot.loading) setSnapshot({ ...snapshot, loading: false })
    return
  }
  setSnapshot({ workspaces: [], currentId: null, loading: false, error: true })
}

function loadWorkspaces(): Promise<void> {
  if (inflight) return inflight
  // Background settings refresh must not unmount active Pages/forms.
  if (snapshot.workspaces.length === 0) setSnapshot({ ...snapshot, loading: true })
  inflight = (async () => {
    try {
      const res = await apiFetch("/api/v1/workspaces")
      if (!res.ok) {
        failLoad()
        return
      }
      const data = (await res.json()) as WorkspaceData[]
      const list = Array.isArray(data) ? data : []
      const persisted = readPersistedId()
      const persistedValid = !!persisted && list.some((w) => w.id === persisted)
      const next = persistedValid ? persisted! : list[0]?.id ?? null
      if (next && !persistedValid) persistId(next)
      if (!next) persistId(null)
      // A periodic re-check (useAccessModeWatcher) usually returns exactly
      // what is held. Keeping the old snapshot then keeps every consumer's
      // `workspace` / `workspaces` identity stable, so nothing re-fetches
      // because a list was re-read.
      if (
        !snapshot.loading &&
        !snapshot.error &&
        snapshot.currentId === next &&
        JSON.stringify(snapshot.workspaces) === JSON.stringify(list)
      ) return
      setSnapshot({ workspaces: list, currentId: next, loading: false, error: false })
    } catch {
      failLoad()
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

/**
 * The selected workspace id, or null while nothing is selected yet.
 *
 * Read-only and, unlike {@link useWorkspace}, it does **not** trigger the
 * workspace load — it only subscribes, so it re-renders when whoever does own
 * the load finishes. That distinction matters for leaf presentational
 * components: `AgentAvatar` renders in ~30 places and needs the id purely to
 * scope a background write (#2196), and calling `useWorkspace()` there would
 * turn every avatar on the page into something that can fire
 * `GET /api/v1/workspaces`.
 *
 * Because it does not load, it reports null on any route that never mounts
 * the store. Every dashboard route does — `app/(dashboard)/layout.tsx` calls
 * `useWorkspace()` — but `/onboarding` does not, and it renders a real
 * agent's avatar. A caller in that position passes its own id through
 * `AgentAvatar`'s `workspaceId` prop rather than relying on this.
 */
export function useCurrentWorkspaceId(): string | null {
  return useSyncExternalStore(
    subscribe,
    () => snapshot.currentId,
    () => ssrSnapshot.currentId,
  )
}

export function useWorkspace(): UseWorkspaceReturn {
  const state = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot)

  useEffect(() => {
    if (!fetched && !inflight) {
      void loadWorkspaces()
    }
  }, [])

  const setWorkspaceId = useCallback(function setWorkspaceId(id: string) {
    if (!snapshot.workspaces.some((w) => w.id === id)) return
    if (snapshot.currentId === id) return
    // A surface holding unsaved work refuses here and asks the person itself,
    // then calls this again once they agree. Switching workspaces is not a
    // navigation and does not reload, so without this the editor was simply
    // re-keyed and typed edits vanished with no prompt.
    if (!navigationAllowed(() => setWorkspaceId(id))) return
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
    capabilities: workspace?.currentUserCapabilities ?? null,
    loading: state.loading,
    error: state.error,
    setWorkspaceId,
    refresh,
  }
}

export function _resetWorkspaceStoreForTests() {
  snapshot = INITIAL
  fetched = false
  inflight = null
  settingsRefresh = null
  settingsRefreshWanted = false
  listeners.clear()
}

/** Read only: Pages reuse the workspace list already loaded by Studio. */
export function useWorkspacePagesTheme(workspaceId?: string) {
  return useSyncExternalStore(subscribe,
    () => snapshot.workspaces.find(w => w.id === workspaceId)?.pages_theme,
    () => undefined)
}
let settingsRefresh: Promise<void> | null = null
let settingsRefreshWanted = false
export function refreshWorkspaceSettings(): Promise<void> {
  settingsRefreshWanted = true
  if (settingsRefresh) return settingsRefresh
  settingsRefresh = (async () => {
    // An older request may have started before the mutation committed.
    if (inflight) await inflight
    // Coalesce bursts while guaranteeing a read after the latest invalidation.
    while (settingsRefreshWanted) {
      settingsRefreshWanted = false
      await loadWorkspaces()
    }
  })().finally(() => { settingsRefresh = null })
  return settingsRefresh
}
