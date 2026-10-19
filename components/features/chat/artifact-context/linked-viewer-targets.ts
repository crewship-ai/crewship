/** A conversation-linked item the side viewer can show. A reference, never a read grant. */
export type LinkedViewerTarget =
  | { workspaceId: string; pageId: string; mode: "latest" }
  | { workspaceId: string; pageId: string; mode: "revision"; revision: number }

export interface ConversationLink {
  target: LinkedViewerTarget
  /** Present only when the current reader may open it; anonymous links carry none. */
  title: string | null
  accessible: boolean
}

export const targetKey = (target: LinkedViewerTarget) =>
  target.mode === "revision" ? `${target.workspaceId}/${target.pageId}@${target.revision}` : `${target.workspaceId}/${target.pageId}@latest`

export function validLinkedTarget(target: LinkedViewerTarget): boolean {
  if (!target.workspaceId || !target.pageId) return false
  return target.mode === "latest" || (target.mode === "revision" && Number.isSafeInteger(target.revision) && target.revision >= 0)
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
