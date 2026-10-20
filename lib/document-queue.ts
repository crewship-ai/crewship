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
  /** True when this tab holds the Page's journal lock, so every row in it is an orphan. */
  exclusive: boolean
}
export class DocumentStorageError extends Error {}
/** Another tab of this browser is the active writer of this Page's journal. */
export class DocumentStorageBusy extends DocumentStorageError {}
/** A browser that never answers an open (blocked profile, stuck upgrade) must not stall the editor. */
export const STORAGE_OPEN_TIMEOUT_MS = 2000
export const STORAGE_LOCK_WAIT_MS = 1000
const message = "Private document storage is unavailable. Changes are unsaved; recover your draft."

export async function openDocumentDrafts(human: string, workspace: string, page: string): Promise<DocumentDraftStore> {
  if (!human || !workspace || !page || typeof indexedDB === "undefined") throw new DocumentStorageError(message)
  const owner = JSON.stringify([human, workspace, page])
  // One live writer per Page: rows in a journal that is free are orphans of a
  // closed tab and may be offered as recovery; rows of an active tab never are.
  let releaseLock: (() => void) | undefined
  if (typeof navigator !== "undefined" && navigator.locks) {
    // Wait briefly: a same-tab remount releases its own lock a moment after the
    // new instance starts. Only a lock that stays held is another live tab.
    const wait = new AbortController()
    const timer = setTimeout(() => wait.abort(), STORAGE_LOCK_WAIT_MS)
    const outcome = await new Promise<{ release: () => void } | "busy" | "none">(resolve => {
      navigator.locks.request(`crewship-document-drafts:${owner}`, { signal: wait.signal }, () => new Promise<void>(release => resolve({ release }))).catch(cause => {
        // A refused request (SecurityError, ...) means no lock is available, not that another tab holds one.
        resolve(cause instanceof DOMException && cause.name === "AbortError" ? "busy" : "none")
      })
    })
    clearTimeout(timer)
    if (outcome === "busy") throw new DocumentStorageBusy("Another tab is editing this document; its local draft is not available here.")
    if (outcome !== "none") releaseLock = outcome.release
  }
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
  }).catch(() => { releaseLock?.(); throw new DocumentStorageError(message) })
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
      // A transaction that never completes must not keep the caller polling.
      let timer: ReturnType<typeof setTimeout> | undefined
      const expired = new Promise<never>((_, reject) => { timer = setTimeout(() => reject(new DocumentStorageError(message)), STORAGE_OPEN_TIMEOUT_MS) })
      try {
        const rows = await Promise.race([transaction("readonly", store => store.getAll(IDBKeyRange.bound([owner, ""], [owner, "\uffff"]))), expired])
        return rows.map((row: { key: [string,string]; draft: DocumentDraft }) => {
          keys.set(row.draft.id,row.key)
          return row.draft
        })
      } finally { clearTimeout(timer) }
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
    exclusive: Boolean(releaseLock),
    close() { db.close(); releaseLock?.() },
  }
}
