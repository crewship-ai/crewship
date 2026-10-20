/** Private, browser-local recovery journal. It never grants replay authority. */
export interface DocumentDraft {
  id: string
  update: string
  attempted: boolean
  schema: number
  epoch: string
  snapshot: string
  baselineRev?: number
  request?: { id: string; update: string; schema: number; epoch: string; kind: string; page_id?: string }
}
export interface DocumentDraftStore {
  load(): Promise<DocumentDraft[]>
  put(draft: DocumentDraft): Promise<void>
  remove(id: string): Promise<void>
  close(): void
}
export class DocumentStorageError extends Error {}
/** A browser that never answers an open (blocked profile, stuck upgrade) must not stall the editor. */
export const STORAGE_OPEN_TIMEOUT_MS = 2000
const message = "Private document storage is unavailable. Changes are unsaved; recover your draft."

export async function openDocumentDrafts(human: string, workspace: string, page: string): Promise<DocumentDraftStore> {
  if (!human || !workspace || !page || typeof indexedDB === "undefined") throw new DocumentStorageError(message)
  const owner = JSON.stringify([human, workspace, page])
  const db = await new Promise<IDBDatabase>((resolve, reject) => {
    let settled = false
    const timer = setTimeout(() => { settled = true; reject(new DocumentStorageError(message)) }, STORAGE_OPEN_TIMEOUT_MS)
    const fail = () => { if (settled) return; settled = true; clearTimeout(timer); reject(new DocumentStorageError(message)) }
    try {
      const request = indexedDB.open("crewship-document-drafts", 1)
      request.onupgradeneeded = () => request.result.createObjectStore("drafts", { keyPath: "key" })
      request.onerror = fail
      request.onblocked = fail
      request.onsuccess = () => {
        // Arrived after the timeout: nobody will use it.
        if (settled) { request.result.close(); return }
        settled = true; clearTimeout(timer); resolve(request.result)
      }
    } catch { fail() }
  }).catch(() => { throw new DocumentStorageError(message) })
  db.onversionchange = () => db.close()
  const keys = new Map<string, [string, string]>()
  function transaction<T>(mode: IDBTransactionMode, action: (store: IDBObjectStore) => IDBRequest<T>): Promise<T> {
    return new Promise((resolve, reject) => {
      try {
        const tx = db.transaction("drafts", mode, { durability: "strict" })
        const request = action(tx.objectStore("drafts"))
        tx.oncomplete = () => resolve(request.result)
        tx.onabort = tx.onerror = () => reject(new DocumentStorageError(message))
      } catch { reject(new DocumentStorageError(message)) }
    })
  }
  return {
    async load() {
      // The compound prefix keeps other people, workspaces and Pages out of
      // this read, including drafts from another tab of the same document.
      const rows = await transaction("readonly", store => store.getAll(IDBKeyRange.bound([owner, ""], [owner, "\uffff"])))
      return rows.map((row: { key: [string,string]; draft: DocumentDraft }) => {
        keys.set(row.draft.id,row.key)
        return row.draft
      })
    },
    async put(draft) {
      // Bound each journal row before structured cloning large editor state.
      if (JSON.stringify(draft).length > 4 * 1024 * 1024) throw new DocumentStorageError(message)
      const key: [string,string] = [owner,JSON.stringify([draft.schema,draft.epoch,draft.id])]
      await transaction("readwrite", store => store.put({ key, draft }))
      keys.set(draft.id,key)
    },
    async remove(id) {
      const key = keys.get(id)
      if (!key) throw new DocumentStorageError(message)
      await transaction("readwrite", store => store.delete(key))
      keys.delete(id)
    },
    close() { db.close() },
  }
}
