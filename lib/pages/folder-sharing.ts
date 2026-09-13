/**
 * Folder sharing, the words (#2533, folder permissions F3′ §7).
 *
 * A folder's permissions are inherited by every page in it right now, and a
 * move changes who reaches the page. Every surface that talks about that —
 * the Sharing dialog, the move dialog's "After the move" block, the sidebar
 * marker, the Access section's "From folder" card — says it with the
 * sentences built here, so the same fact reads the same way everywhere.
 *
 * Two audiences, decided by the server and never guessed here:
 *
 *  · A MANAGER of the owning crew (or a workspace admin) reads the folder's
 *    full ACL and gets names: "Visible to crew Support and everyone in this
 *    workspace; crew Ops can edit."
 *  · Everyone else gets the marker the folder row carries — `shared` is one
 *    of `none | crew | workspace`, no names — and a sentence built from it.
 *
 * Nothing in this file decides which of the two a reader is. The ACL read
 * answers 200 or 403, and the caller hands over either the entries or
 * nothing.
 */

/** The marker every folder row carries — who, roughly, the folder is shared with. */
/**
 * The no-names sharing label the server puts on every folder. It tells the
 * KINDS of subject apart — a folder shared with one person is not "shared
 * with a crew" (audit 2026-09-13, F1) — and never who.
 */
export type FolderShared = "none" | "people" | "crews" | "people_and_crews" | "workspace"

export function toFolderShared(value: unknown): FolderShared {
  switch (value) {
    case "people":
    case "crews":
    case "people_and_crews":
    case "workspace":
      return value
    default:
      return "none"
  }
}

export type FolderAclSubjectType = "user" | "crew" | "workspace"

/** One ACL entry as every surface consumes it. `canRead` is always true —
 *  "edit without view" does not exist, in the data or in the control. */
export interface FolderAclEntry {
  subjectType: FolderAclSubjectType
  /** Empty for `workspace`. */
  subjectId: string
  /** What to print: an email, a crew's display name, or the workspace phrase. */
  label: string
  canWrite: boolean
  setBy: string | null
  setAt: string | null
}

export const EVERYONE_LABEL = "Everyone in this workspace"

/** The sentence above the manager's table. Verbatim from the spec. */
export const FOLDER_ACL_SENTENCE =
  "Folder permissions apply to every page that is in the folder right now, including pages added later. Panels owned by another crew stay sealed. Anyone who can edit can also remove pages from the folder."

/** The marker sentence a non-manager sees instead of the table. */
export function folderSharingSentence(shared: FolderShared): string {
  switch (shared) {
    case "workspace":
      return "Shared with everyone in this workspace"
    case "people":
      return "Shared with named people"
    case "crews":
      return "Shared with named crews"
    case "people_and_crews":
      return "Shared with named people and crews"
    default:
      return "Only the owning crew and workspace admins"
  }
}

/** The sidebar marker's `title`, and the same words wherever the marker is drawn. */
export const SHARED_WITH_WORKSPACE_TITLE = "Shared with everyone in this workspace"

/** "crew Support", "ada@example.com", "everyone in this workspace". */
export function aclSubjectPhrase(entry: Pick<FolderAclEntry, "subjectType" | "label">): string {
  if (entry.subjectType === "workspace") return "everyone in this workspace"
  if (entry.subjectType === "crew") return `crew ${entry.label}`
  return entry.label
}

/** "a", "a and b", "a, b and c". */
export function joinNames(names: readonly string[]): string {
  if (names.length === 0) return ""
  if (names.length === 1) return names[0]
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`
}

function capitalise(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}

/**
 * "After the move", for a reader who may read the target's ACL. Built from
 * the entries, with names. Entries that can edit are listed as editors, not
 * repeated among the viewers — edit implies view, and a sentence that said
 * so twice would be read twice.
 */
export function moveImpactFromAcl(entries: readonly FolderAclEntry[]): string {
  const viewers = entries.filter((e) => !e.canWrite).map(aclSubjectPhrase)
  const editors = entries.filter((e) => e.canWrite).map(aclSubjectPhrase)
  // What the FOLDER adds, never a claim about the page's whole audience: the
  // page's own grants, its owner and the crews owning its panels reach it
  // exactly as before the move, so "only" was false in both directions
  // (audit 2026-09-13, F2).
  if (viewers.length === 0 && editors.length === 0) return `${FOLDER_ADDS_NOTHING} ${GRANTS_STAY}`
  if (editors.length === 0) return `Visible to ${joinNames(viewers)}.`
  if (viewers.length === 0) return `${capitalise(joinNames(editors))} can view and edit.`
  return `Visible to ${joinNames(viewers)}; ${joinNames(editors)} can edit.`
}

export const FOLDER_ADDS_NOTHING = "The folder adds no sharing of its own."
export const GRANTS_STAY = "The page's own grants, its owner and the crews owning its panels reach it as before."

/** "After the move", for a reader who may not read the ACL: the marker only. */
export function moveImpactFromShared(shared: FolderShared): string {
  switch (shared) {
    case "workspace":
      return `Through the folder, everyone in this workspace can see it. ${GRANTS_STAY}`
    case "people":
      return `Through the folder, named people can see it. ${GRANTS_STAY}`
    case "crews":
      return `Through the folder, named crews can see it. ${GRANTS_STAY}`
    case "people_and_crews":
      return `Through the folder, named people and crews can see it. ${GRANTS_STAY}`
    default:
      return `${FOLDER_ADDS_NOTHING} ${GRANTS_STAY}`
  }
}

/** "After the move" out of every folder. No folder, no inherited permission. */
export const MOVE_IMPACT_UNFILED = "No folder permissions apply; the page's own access stays as it is."

/**
 * One of the caller's own paths to a page, as a phrase. The vocabulary is
 * the server's (`owner`, `role`, `crew:<slug>`, `panel_crew:<slug>`, `grant`,
 * `grant:page:<level>`, `folder:<slug>`); a word this build does not know is
 * printed as it came rather than dropped, so a new path is visible before
 * the UI learns its name.
 */
export function describeOwnPath(path: string): string {
  if (path === "owner") return "you own it"
  if (path === "role") return "your workspace role"
  if (path === "grant") return "a grant on the page"
  if (path.startsWith("grant:")) {
    const level = path.split(":").pop()
    return level && level !== "page" ? `a ${level} grant on the page` : "a grant on the page"
  }
  if (path.startsWith("crew:")) return `your crew ${path.slice("crew:".length)}`
  if (path.startsWith("panel_crew:")) {
    const crew = path.slice("panel_crew:".length)
    return crew === "withheld" ? "a panel owned by a crew you are in" : `a panel owned by your crew ${crew}`
  }
  if (path.startsWith("folder:")) return `the folder ${path.slice("folder:".length)}`
  return path
}

/** "You reach it as: you own it, your crew ops and the folder ops." */
export function ownPathsSentence(paths: readonly string[]): string {
  const known = paths.map((p) => p.trim()).filter((p) => p !== "")
  if (known.length === 0) return "You have no path of your own to this page."
  return `You reach it through ${joinNames(known.map(describeOwnPath))}.`
}
