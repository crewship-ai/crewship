/**
 * Local recovery copy of unsent Craft edits. It lives only in this browser
 * and is only ever offered back for compare or copy; nothing here writes to
 * the server, because a stale draft saved over a newer version loses work.
 *
 * Every storage access is wrapped: private mode, blocked site data or a full
 * quota must degrade to "no draft", never to a crash.
 */

export interface LocalDraft { content: string; baseVersion: number; savedAt: string }

const PREFIX = "crewship:craft-draft:v1:"

function draftKey(userId: string, workspaceId: string, slug: string): string {
  return PREFIX + [userId, workspaceId, slug].map(encodeURIComponent).join(":")
}

function isDraft(value: unknown): value is LocalDraft {
  if (!value || typeof value !== "object") return false
  const v = value as Record<string, unknown>
  return typeof v.content === "string" && typeof v.baseVersion === "number" && Number.isFinite(v.baseVersion) && typeof v.savedAt === "string"
}

/** Returns false when the browser refused to store it. */
export function saveLocalDraft(userId: string, workspaceId: string, slug: string, draft: LocalDraft): boolean {
  try {
    localStorage.setItem(draftKey(userId, workspaceId, slug), JSON.stringify(draft))
    return true
  } catch {
    return false
  }
}

export function readLocalDraft(userId: string, workspaceId: string, slug: string): LocalDraft | null {
  try {
    const raw = localStorage.getItem(draftKey(userId, workspaceId, slug))
    if (!raw) return null
    const parsed: unknown = JSON.parse(raw)
    return isDraft(parsed) ? { content: parsed.content, baseVersion: parsed.baseVersion, savedAt: parsed.savedAt } : null
  } catch {
    return null
  }
}

export function discardLocalDraft(userId: string, workspaceId: string, slug: string): void {
  try {
    localStorage.removeItem(draftKey(userId, workspaceId, slug))
  } catch {
    // Nothing to clean up if storage is unavailable.
  }
}
