/**
 * The Sheets opened in a conversation, remembered in this browser so the person
 * finds them again after a reload. A convenience, not a record: only page ids,
 * per person, workspace and conversation; the server decides on every read
 * whether the Sheet is still theirs to open. Replaced by the server's own
 * conversation link once it exists.
 */
export interface ConversationScope { userId: string; workspaceId: string; chatId: string }
const MAX = 10
const key = (s: ConversationScope) => `crewship.chat-sheets:v1:${s.userId}:${s.workspaceId}:${s.chatId}`

export function conversationSheets(scope: ConversationScope): string[] {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(key(scope)) ?? "[]")
    return Array.isArray(value) ? value.filter((id): id is string => typeof id === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(id)).slice(0, MAX) : []
  } catch { return [] }
}
function write(scope: ConversationScope, ids: string[]) {
  try { if (ids.length) localStorage.setItem(key(scope), JSON.stringify(ids.slice(0, MAX))); else localStorage.removeItem(key(scope)) } catch { /* Works for this visit only. */ }
}
export function rememberConversationSheet(scope: ConversationScope, pageId: string) {
  write(scope, [pageId, ...conversationSheets(scope).filter(id => id !== pageId)])
}
export function forgetConversationSheet(scope: ConversationScope, pageId: string) {
  write(scope, conversationSheets(scope).filter(id => id !== pageId))
}
