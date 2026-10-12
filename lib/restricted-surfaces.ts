// What a restricted session's shell may offer (#2861).
//
// The server derives `restricted_surfaces` from its route allowlist
// (internal/api/restricted_surfaces.go) and puts it on every workspace row of a
// restricted account. This file only maps those names to the screens that host
// them; it never decides on its own that a screen is allowed. A restricted row
// without the field allows nothing beyond the unavailable page — fail closed.

import type { WorkspaceData } from "@/hooks/use-workspace"

export type RestrictedSurface = "chat" | "routines" | "pages" | "account_security"

/** The screen each surface lives at. */
export const RESTRICTED_SURFACE_PATHS: Record<RestrictedSurface, string> = {
  chat: "/chat",
  routines: "/routines",
  pages: "/pages",
  account_security: "/settings",
}

/** The surfaces the session may open, or null for a session that is not
 *  restricted. */
export function restrictedSurfacesOf(
  workspaces: readonly Pick<WorkspaceData, "currentUserAccessMode" | "restricted_session" | "restricted_surfaces">[] | null | undefined,
): RestrictedSurface[] | null {
  const row = (workspaces ?? []).find((w) => w.restricted_session === true || w.currentUserAccessMode === "restricted")
  if (!row) return null
  const known = new Set(Object.keys(RESTRICTED_SURFACE_PATHS))
  return (row.restricted_surfaces ?? []).filter((s): s is RestrictedSurface => known.has(s))
}

/** Whether a restricted session may open this path. */
export function restrictedPathAllowed(pathname: string, surfaces: readonly RestrictedSurface[]): boolean {
  return surfaces.some((s) => {
    const base = RESTRICTED_SURFACE_PATHS[s]
    return pathname === base || pathname.startsWith(base + "/")
  })
}

/** Where a restricted session lands instead of the dashboard: chat when it has
 *  it, otherwise its first surface, otherwise nowhere. */
export function restrictedHome(surfaces: readonly RestrictedSurface[]): string | null {
  if (surfaces.includes("chat")) return RESTRICTED_SURFACE_PATHS.chat
  return surfaces.length ? RESTRICTED_SURFACE_PATHS[surfaces[0]] : null
}
