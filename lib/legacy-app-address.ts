/**
 * Old app addresses that never had a page of their own, mapped to the
 * address that replaced them. Pure and address-only: no read decides it, so
 * the move says nothing about whether the artifact exists or who may open it.
 * The target address resolves access itself.
 *
 * `/pages/<slug-or-@id>/editor` was the editor address before the Pages
 * editor became `/pages/<slug>?mode=edit`; that address in turn moves to
 * `/artifacts/<id>?edit=1` for people who can open the artifact.
 */
export function legacyAppAddress(pathname: string, search: string): string | null {
  const match = /^\/pages\/([^/]+)\/editor\/?$/.exec(pathname)
  if (!match) return null
  let reference: string
  try { reference = decodeURIComponent(match[1]) } catch { return null }
  // `/pages/craft/editor` is a real page; anything else must be a plain slug or @ID.
  if (reference === "craft" || !/^@?[A-Za-z0-9_-]{1,128}$/.test(reference)) return null
  const query = new URLSearchParams(search)
  query.set("mode", "edit")
  return `/pages/${encodeURIComponent(reference)}?${query}`
}
