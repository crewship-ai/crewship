/** A conversation-linked item the side viewer can show. A reference, never a read grant. */
export type LinkedViewerTarget =
  | { workspaceId: string; pageId: string; mode: "latest" }
  /** A checkpoint id is not an arbitrary revision: an exact read needs the server-issued checkpoint that holds it. */
  | { workspaceId: string; pageId: string; mode: "revision"; revision: number; checkpointId: string }

export interface ConversationLink {
  target: LinkedViewerTarget
  /** Present only when the current reader may open it; anonymous links carry none. */
  title: string | null
  accessible: boolean
}

/** JSON tuple: ids containing separators cannot collide. */
export const targetKey = (target: LinkedViewerTarget) =>
  JSON.stringify([target.workspaceId, target.pageId, target.mode === "revision" ? [target.revision, target.checkpointId] : "latest"])

export function validLinkedTarget(target: LinkedViewerTarget): boolean {
  if (!target.workspaceId || !target.pageId) return false
  return target.mode === "latest" || (target.mode === "revision" && Number.isSafeInteger(target.revision) && target.revision >= 0 && Boolean(target.checkpointId))
}

/** The header may switch only between accessible, valid links of this conversation, each once. */
export function switchableLinks(links: ConversationLink[]): ConversationLink[] {
  const seen = new Set<string>()
  return links.filter(link => {
    if (!link.accessible || !link.title?.trim() || !validLinkedTarget(link.target)) return false
    const key = targetKey(link.target)
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

export const targetLabel = (target: LinkedViewerTarget) => target.mode === "revision" ? `Revision ${target.revision}` : "Latest"
