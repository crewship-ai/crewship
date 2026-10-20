/**
 * Vault folders (GET /api/v1/credential-folders). A folder groups secrets and
 * carries a reveal policy, a Keeper tier floor/default and a replace-reminder;
 * folders nest two levels. Pure helpers only — the fetch lives in
 * hooks/use-credential-folders.ts.
 */

export type FolderRevealPolicy = "inherit" | "admins" | "nobody"

export interface CredentialFolder {
  id: string
  parent_id: string | null
  name: string
  description: string
  reveal_policy: FolderRevealPolicy
  remind_after_days: number
  default_security_level: number | null
  min_security_level: number
  /** Secrets filed directly in this folder that the caller can see. */
  credential_count: number
}

export const FOLDER_REVEAL_LABEL: Record<FolderRevealPolicy, string> = {
  inherit: "Workspace rules",
  admins: "Owners and admins",
  nobody: "Nobody · sealed folder",
}

export interface FolderNode {
  folder: CredentialFolder
  depth: 0 | 1
  /** Secrets in the folder and in its subfolders. */
  total: number
}

/** Roots A–Z, each followed by its subfolders A–Z. A folder whose parent is not
 *  in the list (should not happen) is shown as a root rather than dropped. */
export function buildFolderTree(folders: CredentialFolder[]): FolderNode[] {
  const byName = (a: CredentialFolder, b: CredentialFolder) => a.name.localeCompare(b.name)
  const ids = new Set(folders.map((f) => f.id))
  const roots = folders.filter((f) => !f.parent_id || !ids.has(f.parent_id)).sort(byName)
  const out: FolderNode[] = []
  for (const root of roots) {
    const kids = folders.filter((f) => f.parent_id === root.id).sort(byName)
    out.push({ folder: root, depth: 0, total: root.credential_count + kids.reduce((n, k) => n + k.credential_count, 0) })
    for (const kid of kids) out.push({ folder: kid, depth: 1, total: kid.credential_count })
  }
  return out
}

/** The folder ids a selection covers: the folder and, for a root, its subfolders. */
export function folderSelectionIds(folders: CredentialFolder[], selected: readonly string[]): Set<string> {
  const out = new Set<string>()
  for (const id of selected) {
    out.add(id)
    for (const f of folders) if (f.parent_id === id) out.add(f.id)
  }
  return out
}

/** "Production / Payments" for a subfolder, the bare name for a root. */
export function folderPathLabel(folders: CredentialFolder[], id: string | null | undefined): string {
  if (!id) return ""
  const f = folders.find((x) => x.id === id)
  if (!f) return ""
  const parent = f.parent_id ? folders.find((x) => x.id === f.parent_id) : null
  return parent ? `${parent.name} / ${f.name}` : f.name
}

/** True when filing into (or out of) this folder is an access decision the
 *  server reserves for owners and admins. */
export function folderIsRestricted(f: CredentialFolder | undefined): boolean {
  return !!f && (f.reveal_policy !== "inherit" || f.min_security_level > 1)
}
