/**
 * Undo of an agent's change goes through the open editor of that Sheet: the server's undo endpoint
 * needs the editor's session and its lease, which only the editor host holds.
 * The chat card asks; the host of that page answers with the server's response.
 */
export type SheetUndoAnswer = Response | "no_editor" | "not_editing"
type Handler = (operationId: string) => Promise<Response | "not_editing">
const handlers = new Map<string, Handler>()

/** The editor host of `pageId` takes undo requests while it is mounted. */
export function registerSheetUndo(pageId: string, handler: Handler): () => void {
  handlers.set(pageId, handler)
  return () => { if (handlers.get(pageId) === handler) handlers.delete(pageId) }
}

/** "no_editor": the Sheet is not open here; "not_editing": it is open but the person does not edit it now. */
export async function requestSheetUndo(pageId: string, operationId: string): Promise<SheetUndoAnswer> {
  const handler = handlers.get(pageId)
  return handler ? handler(operationId) : "no_editor"
}
