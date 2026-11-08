/**
 * Where an old Pages address takes a restricted member (#5132). Their one
 * surface is /artifacts; the old address only moves there, without any page
 * read (the library reads are outside the restricted allowlist), so the
 * target is decided from the address alone:
 *
 * - `/pages[?…]` (and a `?ref=` that is not exactly one ID) → `/artifacts`
 * - `/pages/<slug>[?…]` → `/artifacts?item=<slug>`, which the restricted
 *   catalog answers for a granted artifact and gates for any other
 * - `/pages/@<id>`, `/pages?ref=<id>` → `/artifacts/<id>`, which is gated:
 *   the catalog names artifacts by slug only, so an ID never proves a grant
 * - `/pages/craft/editor` → null: the native editor host keeps answering it
 *   exactly as before (it refuses a restricted session itself)
 *
 * Only the workspace link survives; versions, modes and project flags have
 * nothing to open in the restricted catalog.
 */
export function restrictedArtifactsHref(pathname: string, search: string): string | null {
  const params = new URLSearchParams(search)
  const path = pathname.replace(/\/+$/, "") || "/"
  if (path === "/pages/craft/editor" || path.startsWith("/pages/craft/editor/")) return null
  const query = new URLSearchParams()
  const workspaces = params.getAll("workspace_id")
  if (workspaces.length === 1 && workspaces[0]) query.set("workspace_id", workspaces[0])
  const home = () => `/artifacts${query.size ? `?${query}` : ""}`
  const byId = (id: string) => SAFE_ID.test(id) ? `/artifacts/${encodeURIComponent(id)}${query.size ? `?${query}` : ""}` : home()
  if (path === "/pages") {
    const refs = params.getAll("ref")
    return refs.length === 1 ? byId(refs[0]) : home()
  }
  const match = /^\/pages\/([^/]+)$/.exec(path)
  if (!match) return home()
  let reference: string
  try { reference = decodeURIComponent(match[1]) } catch { return home() }
  if (reference.startsWith("@")) return byId(reference.slice(1))
  if (!reference || reference === "_") return home()
  query.set("item", reference)
  return home()
}

const SAFE_ID = /^[A-Za-z0-9_-]{1,128}$/
