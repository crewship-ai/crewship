import type { PageView } from "@/hooks/use-pages"
import { kindOf } from "./kinds"
import { artifactsProjectHref, libraryItems, opensAsProject, opensInArtifacts, versionPin, type LibraryItem } from "./library-items"

/**
 * Where an old Pages address goes once the Artifacts library is the single
 * home (#5129): the library itself, a reference that resolves to the
 * artifact's immutable ID, or nowhere (it stays on Pages).
 */
export type LegacyPagesTarget =
  | { to: "library"; href: string }
  | { to: "resolve"; reference: string; workspaceId: string; project: boolean; version?: number; edit?: boolean }
  | { to: "stay" }

/**
 * `trusted` is the session's access mode: a restricted member is never resolved
 * here, because the library reads (`GET /api/v1/pages`, `GET /api/v1/pages/{slug}`)
 * are outside the restricted allowlist. Their /pages addresses move to their
 * catalog by address alone (`restricted-artifacts-url.ts`, #5132).
 */
export interface LegacyPagesContext { workspaceId: string; library: boolean; trusted: boolean }

/** The one canonical address of an artifact: its immutable page ID, workspace, optional pin and its editor (`edit=1`). */
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
  // A reference on a single page has no Artifacts equivalent.
  if (params.has("ref")) return STAY
  // The Pages panel editor (`mode=edit`) and the project source editor
  // (`artifact=1&edit=1`) open in the frame at /artifacts/<id>?edit=1.
  const mode = params.getAll("mode"), editFlag = params.getAll("edit")
  if (mode.length > 1 || mode.length === 1 && mode[0] !== "edit" && mode[0] !== "view") return STAY
  if (editFlag.length > 1 || editFlag.length === 1 && editFlag[0] !== "1") return STAY
  const edit = mode[0] === "edit" || editFlag[0] === "1"
  // `artifact=1` names the project viewer; a pin alone is a version of whatever
  // the page is (a project's version, a Doc's revision, a Design's version) and
  // moves with the address (#5168). resolvedArtifactId decides which kinds keep it.
  const project = params.get("artifact") === "1"
  const version = versionPin(params.get("version"))
  // A bare pin that is not a version is not guessed at.
  if (!project && params.has("version") && version === undefined) return STAY
  return { to: "resolve", reference, workspaceId: context.workspaceId, project, ...(version === undefined ? {} : { version }), ...(edit ? { edit } : {}) }
}

/**
 * The ID a resolved page opens at in Artifacts, or null to stay on Pages.
 * Every kind opens in the Artifacts frame (owner, 8 Nov 2026); a project
 * address only for a versioned project, a pinned address only for a kind with
 * versions, and a Design only behind its switch.
 * Fails closed on a missing page or unusable ID.
 */
export function resolvedArtifactId(target: Extract<LegacyPagesTarget, { to: "resolve" }>, page: PageView | null): string | null {
  if (!page?.immutablePageId) return null
  const [item] = libraryItems([page])
  if (!opensInArtifacts(item)) return null
  if (target.project) return opensAsProject(item) ? page.immutablePageId : null
  // A pin opens only where the frame has versions: a project, a Doc's History, a Design.
  if (target.version !== undefined && !opensAsProject(item) && !hasVersionView(item)) return null
  return page.immutablePageId
}

function hasVersionView(item: LibraryItem): boolean {
  const kind = kindOf(item)
  return kind.kind === "design" || kind.kind === "docs" && kind.mode === "page"
}
