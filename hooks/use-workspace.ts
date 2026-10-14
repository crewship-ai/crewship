"use client"

import { useCallback, useEffect, useSyncExternalStore } from "react"

import { navigationAllowed } from "@/hooks/use-navigation-guard"
import { setWorkspaceRequestAvailability, setWorkspaceRequestSession } from "@/lib/workspace-availability"
import { apiFetch } from "@/lib/api-fetch"

export interface WorkspaceData {
  currentUserAccessMode?: "trusted" | "restricted"
  /** Present on a restricted account's rows only (#2861): the session is
   *  behind the restricted allowlist, and these are the screens it may open. */
  restricted_session?: boolean
  restricted_surfaces?: string[]
  pages_theme?: Partial<import("@/lib/pages/theme").PageTheme> | null
  id: string
  name: string
  slug: string
  /** "personal" for the one made at sign-up for its owner alone: shown as
   *  their personal account, apart from the workspaces. Older backends omit it. */
  kind?: "team" | "personal"
  /** Whose personal account it is (kind "personal" only). */
  personal_owner_id?: string | null
  /** The workspace's uploaded logo (#3005); null draws its initial. */
  logo_url?: string | null
  currentUserRole: string | null
  /** Resolved per-membership capability grants (#1034). Optional so
   *  a client talking to an older backend degrades to role-only
   *  gating instead of crashing. */
  currentUserCapabilities?: string[] | null
}

interface UseWorkspaceReturn {
  workspaceId: string | null
  workspaceUnavailable: boolean
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
  setWorkspaceId: (id: string | null) => void
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
  setWorkspaceRequestAvailability(next.currentId, !next.loading && !next.error && (!next.currentId || next.workspaces.some(w => w.id === next.currentId)))
  snapshot = next
  emit()
}

// Account-bound sessions never adopt another account’s browser selection.
function storageKey(): string {
  return sessionIdentity ? `${STORAGE_KEY}:${sessionIdentity}` : STORAGE_KEY
}

function readPersistedId(): string | null {
  if (typeof window === "undefined") return null
  try {
    return window.localStorage.getItem(storageKey())
  } catch {
    return null
  }
}

function persistId(id: string | null) {
  if (typeof window === "undefined") return
  try {
    if (id) window.localStorage.setItem(storageKey(), id)
    else window.localStorage.removeItem(storageKey())
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
  setSnapshot({ workspaces: [], currentId: snapshot.currentId ?? readPersistedId(), loading: false, error: true })
}

let sessionIdentity: string | null | undefined
let storeGeneration = 0

/** A new authenticated identity must load its own directory, never reuse rows. */
export function bindWorkspaceSession(userId: string | null) {
  if (sessionIdentity === userId) return
  sessionIdentity = userId
  storeGeneration++
  inflight = null
  fetched = false
  setWorkspaceRequestSession(userId)
  setSnapshot({ workspaces: [], currentId: userId ? readPersistedId() : null, loading: userId !== null, error: false })
  if (userId) void loadWorkspaces()
}

function loadWorkspaces(): Promise<void> {
  if (inflight) return inflight
  // Background settings refresh must not unmount active Pages/forms.
  if (snapshot.workspaces.length === 0 && snapshot.currentId === null) setSnapshot({ ...snapshot, loading: true })
  const generation = storeGeneration
  inflight = (async () => {
    try {
      const res = await apiFetch("/api/v1/workspaces")
      if (generation !== storeGeneration) return
      if (!res.ok) {
        failLoad()
        return
      }
      const data = (await res.json()) as WorkspaceData[]
      if (generation !== storeGeneration) return
      const list = Array.isArray(data) ? data : []
      const persisted = readPersistedId()
      // A partial directory cannot distinguish outage from removal. Preserve
      // an explicit choice; absence requires retry or a guarded user selection.
      const selected = snapshot.currentId ?? persisted
      // On a fresh login there is no workspace draft to preserve. A stale
      // account preference (e.g. removed membership) may safely recover.
      const next = snapshot.loading && selected && !list.some(w => w.id === selected)
        ? list[0]?.id ?? null
        : selected ?? list[0]?.id ?? null
      if (next && next !== persisted) persistId(next)
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
      if (generation === storeGeneration) failLoad()
    } finally {
      if (generation === storeGeneration) {
        fetched = true
        inflight = null
      }
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

  const setWorkspaceId = useCallback(function setWorkspaceId(id: string | null) {
    if (id !== null && !snapshot.workspaces.some((w) => w.id === id)) return
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
    workspaceUnavailable: state.currentId !== null && workspace === null && !state.loading,
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
  sessionIdentity = undefined
  storeGeneration++
  setWorkspaceRequestSession(null)
  setWorkspaceRequestAvailability(null, true)
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
