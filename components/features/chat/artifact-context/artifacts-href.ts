import { artifactsLibrary } from "@/lib/feature-flags"

/** One saved item's own address in Artifacts, opened in the workspace it belongs to. */
export function artifactsItemHref(workspaceId: string | null, pageId: string): string {
  return `/artifacts/${encodeURIComponent(pageId)}${workspaceId ? `?${new URLSearchParams({ workspace_id: workspaceId })}` : ""}`
}

/**
 * Where a chat link to a saved item goes: Artifacts once the library is on,
 * otherwise the Pages route it always had. Chat never links to Pages beside
 * the library.
 */
export function chatItemHref(workspaceId: string | null, pageId: string, pagesHref: string): string {
  return artifactsLibrary() ? artifactsItemHref(workspaceId, pageId) : pagesHref
}
