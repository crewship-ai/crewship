/**
 * Sheets whose import was left before it finished: the Page exists and is
 * empty. Remembered in this browser (page ids only, per person and workspace)
 * so the library can say "Import not finished" and offer to delete it; the
 * server still decides whether the Page is there and who may delete it.
 */
export interface ImportScope { userId: string; workspaceId: string }
const key = (s: ImportScope) => `crewship.unfinished-imports:v1:${s.userId}:${s.workspaceId}`

export function unfinishedImports(scope: ImportScope): string[] {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(key(scope)) ?? "[]")
    return Array.isArray(value) ? value.filter((id): id is string => typeof id === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(id)).slice(0, 20) : []
  } catch { return [] }
}
function write(scope: ImportScope, ids: string[]) {
  try { if (ids.length) localStorage.setItem(key(scope), JSON.stringify(ids.slice(0, 20))); else localStorage.removeItem(key(scope)) } catch { /* This visit only. */ }
}
export function noteUnfinishedImport(scope: ImportScope, pageId: string) { write(scope, [pageId, ...unfinishedImports(scope).filter(id => id !== pageId)]) }
export function clearUnfinishedImport(scope: ImportScope, pageId: string) { write(scope, unfinishedImports(scope).filter(id => id !== pageId)) }
