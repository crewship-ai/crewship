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

/**
 * A saved item addressed by its Pages reference: `@<id>` opens at its ID
 * address, a slug at `/artifacts?item=<slug>`, which moves to the ID once the
 * slug resolves. With the library off (a build that sets it false) the Pages
 * address it always had.
 */
export function artifactsRefHref(workspaceId: string | null | undefined, reference: string, pagesHref: string): string {
  if (!artifactsLibrary()) return pagesHref
  if (reference.startsWith("@")) return artifactsItemHref(workspaceId ?? null, reference.slice(1))
  const query = new URLSearchParams({ item: reference })
  if (workspaceId) query.set("workspace_id", workspaceId)
  return `/artifacts?${query}`
}

/**
 * The editor of a saved item inside its Artifacts frame, `/artifacts/<id>?edit=1`:
 * the App source editor or the panel editor, with the permission checks they
 * always had. With the library off, the Pages address it always had.
 */
export function artifactsEditHref(workspaceId: string, pageId: string, pagesHref: string): string {
  if (!artifactsLibrary()) return pagesHref
  return `/artifacts/${encodeURIComponent(pageId)}?${new URLSearchParams({ workspace_id: workspaceId, edit: "1" })}`
}
