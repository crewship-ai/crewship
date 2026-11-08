import type { PageView } from "@/hooks/use-pages"
import { chatArtifactProjects } from "@/lib/feature-flags"
import { artifactsProjectHref, libraryItems, versionPin, viewsProjectInline } from "./library-items"

/**
 * Where an old Pages address goes once the Artifacts library is the single
 * home (#5129): the library itself, a reference that resolves to the
 * artifact's immutable ID, or nowhere (it stays on Pages).
 */
export type LegacyPagesTarget =
  | { to: "library"; href: string }
  | { to: "resolve"; reference: string; workspaceId: string; project: boolean; version?: number }
  | { to: "stay" }

/**
 * `trusted` is the session's access mode: a restricted member is never moved,
 * because the library reads (`GET /api/v1/pages`, `GET /api/v1/pages/{slug}`)
 * are outside the restricted allowlist and Pages shows them their catalog.
 */
export interface LegacyPagesContext { workspaceId: string; library: boolean; trusted: boolean }

/** The one canonical address of an artifact: its immutable page ID, workspace and optional pin. */
export const canonicalArtifactHref = artifactsProjectHref

const SAFE_ID = /^[A-Za-z0-9_-]{1,128}$/
const STAY: LegacyPagesTarget = { to: "stay" }

/** The single value of a parameter, or null when it is missing or repeated. */
function single(params: URLSearchParams, key: string): string | null {
  const values = params.getAll(key)
  return values.length === 1 ? values[0] : null
}

/** An ID-addressed entry is accepted only for exactly one safe ID in exactly this workspace. */
function byId(params: URLSearchParams, key: string, workspaceId: string): LegacyPagesTarget {
  const id = single(params, key)
  if (!id || !SAFE_ID.test(id) || single(params, "workspace_id") !== workspaceId) return STAY
  return { to: "resolve", reference: "@" + id, workspaceId, project: false }
}

/**
 * Maps one old Pages address (`pathname` and query, without `?`) to its
 * Artifacts target. Pure: the caller resolves a `resolve` target with one
 * page read and moves only when `resolvedArtifactId` returns an ID.
 */
export function legacyPagesTarget(pathname: string, search: string, context: LegacyPagesContext): LegacyPagesTarget {
  if (!context.library || !context.trusted) return STAY
  const params = new URLSearchParams(search)
  const path = pathname.replace(/\/+$/, "")
  if (path === "/pages") {
    if (params.has("ref")) return byId(params, "ref", context.workspaceId)
    return { to: "library", href: `/artifacts${search ? `?${search}` : ""}` }
  }
  if (path === "/pages/craft/editor") return byId(params, "page_id", context.workspaceId)
  const match = /^\/pages\/([^/]+)$/.exec(path)
  if (!match) return STAY
  let reference: string
  try { reference = decodeURIComponent(match[1]) } catch { return STAY }
  // The Pages panel editor, the project source editor and a reference on a
  // single page have no Artifacts equivalent.
  if (params.has("mode") || params.has("edit") || params.has("ref")) return STAY
  // A pin is a project version: it moves only to a project viewer, with the pin.
  const project = params.get("artifact") === "1" || params.has("version")
  const version = project ? versionPin(params.get("version")) : undefined
  return { to: "resolve", reference, workspaceId: context.workspaceId, project, ...(version === undefined ? {} : { version }) }
}

/**
 * The ID a resolved page opens at in Artifacts, or null to stay on Pages. A
 * plain address moves for a kind the Artifacts detail opens (a Page, a native
 * Word or Sheet, a versioned project); a project address only to a project
 * viewer the detail has. Fails closed on a missing page or unusable ID.
 */
export function resolvedArtifactId(target: Extract<LegacyPagesTarget, { to: "resolve" }>, page: PageView | null): string | null {
  if (!page?.immutablePageId) return null
  const [item] = libraryItems([page])
  if (target.project) return chatArtifactProjects() && viewsProjectInline(item) ? page.immutablePageId : null
  return item.projectBacked || item.native || item.kind === "page" ? page.immutablePageId : null
}
