// What a restricted session's shell may offer (#2861).
//
// The server derives member/workspace `restricted_surfaces` within its route allowlist
// (internal/api/restricted_surfaces.go) and puts it on every workspace row of a
// restricted account. This file only maps those names to the screens that host
// them; it never decides on its own that a screen is allowed. A restricted row
// without the field allows nothing beyond the unavailable page — fail closed.

import type { WorkspaceData } from "@/hooks/use-workspace"

export type RestrictedSurface = "issues" | "chat" | "routines" | "pages" | "account_security"

/** The screen each surface lives at. The server's `pages` surface (Page
 *  actions) is the member's Artifacts screen (#5132): "Pages" is never shown. */
export const RESTRICTED_SURFACE_PATHS: Record<RestrictedSurface, string> = {
  issues: "/issues",
  chat: "/chat",
  routines: "/routines",
  pages: "/artifacts",
  account_security: "/settings",
}

/** Old addresses a surface still answers, only so that the route can move
 *  them to the surface's screen (`/pages*` to `/artifacts`). They are never a
 *  destination: navigation and "Go to" links use `RESTRICTED_SURFACE_PATHS`. */
export const RESTRICTED_LEGACY_PATHS: Partial<Record<RestrictedSurface, string>> = {
  pages: "/pages",
}

/** The surfaces the session may open, or null for a session that is not
 *  restricted. */
export function restrictedSurfacesOf(
  workspaces: readonly (Pick<WorkspaceData, "currentUserAccessMode" | "restricted_session" | "restricted_surfaces"> & { id?: string })[] | null | undefined,
  selectedWorkspaceId?: string | null,
): RestrictedSurface[] | null {
  const rows = workspaces ?? []
  if (!rows.some(w => w.restricted_session === true || w.currentUserAccessMode === "restricted")) return null
  const row = selectedWorkspaceId === undefined
    ? rows.find(w => w.restricted_session === true || w.currentUserAccessMode === "restricted")
    : rows.find(w => w.id === selectedWorkspaceId)
  if (!row) return []
  const known = new Set(Object.keys(RESTRICTED_SURFACE_PATHS))
  return (row.restricted_surfaces ?? []).filter((s): s is RestrictedSurface => known.has(s))
}

const under = (pathname: string, base: string | undefined) => base !== undefined && (pathname === base || pathname.startsWith(base + "/"))

/** Whether a restricted session may open this path as a screen. */
export function restrictedPathAllowed(pathname: string, surfaces: readonly RestrictedSurface[]): boolean {
  return surfaces.some((s) => under(pathname, RESTRICTED_SURFACE_PATHS[s]))
}

/** Whether this path is an old address of a granted surface, which the shell
 *  mounts only so that its route redirects it (or, for the native editor
 *  host, answers it exactly as before). Requires the same surface. */
export function restrictedLegacyPath(pathname: string, surfaces: readonly RestrictedSurface[]): boolean {
  return surfaces.some((s) => under(pathname, RESTRICTED_LEGACY_PATHS[s]))
}

/** Where a restricted session lands instead of the dashboard: chat when it has
 *  it, otherwise its first surface, otherwise nowhere. */
export function restrictedHome(surfaces: readonly RestrictedSurface[]): string | null {
  if (surfaces.includes("chat")) return RESTRICTED_SURFACE_PATHS.chat
  return surfaces.length ? RESTRICTED_SURFACE_PATHS[surfaces[0]] : null
}
