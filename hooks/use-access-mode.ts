"use client"

import { useEffect, useRef } from "react"

import { useWorkspace, type WorkspaceData } from "@/hooks/use-workspace"
import { restrictedSurfacesOf, type RestrictedSurface } from "@/lib/restricted-surfaces"

/**
 * What the signed-in ACCOUNT may reach, for the whole session.
 *
 * - `"loading"` — not known yet (the workspace list has not arrived, or the
 *   first load failed). Nothing outside the restricted allowlist may be sent
 *   in this state: every shell data hook starts only once the mode is
 *   `"trusted"`.
 * - `"restricted"` — at least one membership is restricted. The server applies
 *   the restricted allowlist (`internal/api/restricted_access.go`) to EVERY
 *   authenticated request from this account, whichever workspace it names
 *   (`access.Store.HasRestrictedMembership`), so the frontend has to as well.
 *   Being trusted in the selected workspace changes nothing.
 * - `"trusted"` — no restricted membership; the full product.
 *
 * Only the restricted directory projection of `GET /api/v1/workspaces`
 * (`internal/api/restricted_directory.go`) carries `currentUserAccessMode`, and
 * it carries it on every row once the account has a restricted membership. A
 * trusted account gets the normal projection, where the field is absent.
 */
export type AccessMode = "loading" | "trusted" | "restricted"

/** Pure derivation, exported for tests. */
export function deriveAccessMode(
  workspaces: readonly Pick<WorkspaceData, "currentUserAccessMode">[] | null | undefined,
  loading: boolean | undefined,
  error?: boolean,
): AccessMode {
  const list = workspaces ?? []
  if (list.length === 0 && (loading || error)) return "loading"
  return list.some((w) => w.currentUserAccessMode === "restricted") ? "restricted" : "trusted"
}

export function useAccessMode(): AccessMode {
  const { workspaces, loading, error } = useWorkspace()
  return deriveAccessMode(workspaces, loading, error)
}

/**
 * The screens a restricted session may open (#2861), or null for a trusted or
 * still-loading session. Read from the server's `restricted_surfaces`, which
 * it derives from the route allowlist itself.
 */
export function useRestrictedSurfaces(): RestrictedSurface[] | null {
  const { workspaces } = useWorkspace()
  return useAccessMode() === "restricted" ? restrictedSurfacesOf(workspaces) ?? [] : null
}

/**
 * The selected workspace id, but only for a trusted session — null while the
 * mode is loading and for a restricted session.
 *
 * Shell data hooks (`useInboxUnreadCount`, `useEngineStatus`, `usePipelineRuns`,
 * …) already do nothing for a null workspace, so passing them this instead of
 * `workspaceId` is the whole gate: they start when the session is known to be
 * trusted and stop the moment it turns restricted.
 */
export function useTrustedWorkspaceId(): string | null {
  const { workspaceId } = useWorkspace()
  return useAccessMode() === "trusted" ? workspaceId : null
}

/** Re-check cadence while the tab is visible. Restricted screens have no
 *  WebSocket, so this re-read is how a change of access reaches an open tab. */
export const ACCESS_RECHECK_MS = 60_000
/** A tab coming back to the foreground re-checks at most this often. */
const VISIBLE_RECHECK_MIN_MS = 15_000

/**
 * Keeps the session access mode current while the app is open: re-reads
 * `GET /api/v1/workspaces` (allowlisted for every session) every
 * {@link ACCESS_RECHECK_MS} while the tab is visible and when it becomes
 * visible again. An unchanged answer is a no-op in the workspace store, so a
 * re-check never re-renders the app. Mounted once, by the dashboard layout.
 */
export function useAccessModeWatcher(): void {
  const { refresh } = useWorkspace()
  const lastRef = useRef(Date.now())

  useEffect(() => {
    if (typeof document === "undefined") return
    const recheck = () => {
      lastRef.current = Date.now()
      void refresh()
    }
    const tick = setInterval(() => {
      if (document.visibilityState === "visible") recheck()
    }, ACCESS_RECHECK_MS)
    const onVisible = () => {
      if (document.visibilityState !== "visible") return
      if (Date.now() - lastRef.current >= VISIBLE_RECHECK_MIN_MS) recheck()
    }
    document.addEventListener("visibilitychange", onVisible)
    return () => {
      clearInterval(tick)
      document.removeEventListener("visibilitychange", onVisible)
    }
  }, [refresh])
}
