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

/** Reuse the browser-storage deadline for transactions as well as opening.
 * Abort the actual transaction before releasing a privacy barrier: a rejected
 * promise alone would still permit a late write after the final purge. */
function boundDraftTransaction(tx: IDBTransaction, complete: () => void, reject: (cause: Error) => void): () => void {
  let settled = false
  const finish = (ok: boolean) => {
    if (settled) return
    settled = true; clearTimeout(timer)
    if (ok) complete()
    else reject(new DocumentStorageError(message))
  }
  const abort = () => {
    if (settled) return
    // InvalidStateError means the native transaction has already finished.
    try { tx.abort() } catch { /* Already committed or aborted. */ }
    finish(false)
  }
  const timer = setTimeout(abort, STORAGE_OPEN_TIMEOUT_MS)
  tx.oncomplete = () => finish(true)
  tx.onabort = () => finish(false)
  tx.onerror = abort
  return abort
}

export async function openDocumentDrafts(human: string, workspace: string, page: string, options?: { maxDraftBytes?: number; requireExclusive?: boolean }): Promise<DocumentDraftStore> {
  const maxDraftBytes = options?.maxDraftBytes ?? 4 * 1024 * 1024
  const requireExclusive = options?.requireExclusive === true
  if (!Number.isSafeInteger(maxDraftBytes) || maxDraftBytes < 1 || maxDraftBytes > 64 * 1024 * 1024) throw new DocumentStorageError(message)
  if (!human || !workspace || !page || typeof indexedDB === "undefined") throw new DocumentStorageError(message)
  const owner = JSON.stringify([human, workspace, page])
  // Native journals require a real non-queued lock. Legacy callers retain
  // their remount wait and non-exclusive fallback.
  if (requireExclusive && (typeof navigator === "undefined" || !navigator.locks)) throw new DocumentStorageError(message)
  // One live writer per Page: rows in a journal that is free are orphans of a
  // closed tab and may be offered as recovery; rows of an active tab never are.
  let releaseLock: (() => void) | undefined
  if (typeof navigator !== "undefined" && navigator.locks) {
    // Wait briefly: a same-tab remount releases its own lock a moment after the
    // new instance starts. Only a lock that stays held is another live tab.
    const wait = new AbortController()
    let accepting = true
    const outcome = await new Promise<{ release: () => void } | "busy" | "none">(resolve => {
      const finish = (value: { release: () => void } | "busy" | "none") => {
        if (!accepting) return
        accepting = false; clearTimeout(timer); resolve(value)
      }
      const timer = setTimeout(() => { finish(requireExclusive ? "none" : "busy"); wait.abort() }, STORAGE_LOCK_WAIT_MS)
      const refused = (cause: unknown) => {
        // A refused request (SecurityError, ...) means no lock is available, not that another tab holds one.
        finish(cause instanceof DOMException && cause.name === "AbortError" ? "busy" : "none")
      }
      try {
        // The Web Locks specification forbids signal together with ifAvailable.
        navigator.locks.request(`crewship-document-drafts:${owner}`, requireExclusive ? { ifAvailable: true } : { signal: wait.signal }, lock => {
          if (!accepting) return Promise.resolve() // A late grant releases immediately after our deadline.
          if (!lock) { finish("busy"); return Promise.resolve() }
          return new Promise<void>(release => finish({ release }))
        }).catch(refused)
      } catch (cause) { refused(cause) }
    })
    if (outcome === "busy") throw new DocumentStorageBusy("Another tab is editing this document; its local draft is not available here.")
    if (outcome !== "none") releaseLock = outcome.release
    else if (requireExclusive) throw new DocumentStorageError(message)
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
      let abort = () => {}
      try {
        const tx = db.transaction("drafts", mode, { durability: "strict" })
        let request: IDBRequest<T>
        abort = boundDraftTransaction(tx, () => resolve(request.result), reject)
        request = action(tx.objectStore("drafts"))
      } catch { abort(); reject(new DocumentStorageError(message)) }
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
      const serialized = JSON.stringify(draft)
      if (serialized.length > maxDraftBytes || (options?.maxDraftBytes !== undefined && new TextEncoder().encode(serialized).byteLength > maxDraftBytes)) throw new DocumentStorageError(message)
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

// Auth transitions retire mounted writers synchronously, before any asynchronous
// storage operation. A late mount from the old principal is retired too.
const draftWriters = new Map<symbol, { human: string; retire: () => Promise<void> }>()
let journalHuman: string | null | undefined
let identityPurge = Promise.resolve()
export function registerDocumentDraftWriter(human: string, retire: () => Promise<void>): () => void {
  if (journalHuman !== undefined && human !== journalHuman) {
    void retire().catch(() => {})
    return () => {}
  }
  const key = Symbol()
  draftWriters.set(key, { human, retire })
  return () => { draftWriters.delete(key) }
}

/**
 * Privacy purge, independent of any open store or Page lock. The journal holds
 * snapshots of private Documents, so access loss (a Page denial) and sign-out
 * or an identity change delete the rows themselves, not only what is on screen.
 * Best effort: unavailable storage cannot be cleaned and must not block the caller.
 */
async function purgeRows(range: () => IDBKeyRange | undefined, owns: (owner: string) => boolean): Promise<void> {
  if (typeof indexedDB === "undefined") return
  try {
    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      let settled = false
      const fail = () => { if (settled) return; settled = true; clearTimeout(timer); reject(new Error("open")) }
      const timer = setTimeout(fail, STORAGE_OPEN_TIMEOUT_MS)
      const request = indexedDB.open("crewship-document-drafts", 1)
      request.onupgradeneeded = () => request.result.createObjectStore("drafts", { keyPath: "key" })
      request.onerror = request.onblocked = fail
      request.onsuccess = () => {
        if (settled) { request.result.close(); return }
        settled = true; clearTimeout(timer); resolve(request.result)
      }
    })
    try {
      await new Promise<void>((resolve, reject) => {
        const tx = db.transaction("drafts", "readwrite")
        const abort = boundDraftTransaction(tx, resolve, reject)
        try {
          const cursor = tx.objectStore("drafts").openCursor(range())
          cursor.onsuccess = () => {
            try {
              const row = cursor.result
              if (!row) return
              const key = row.key as [string, string]
              if (owns(key[0])) row.delete()
              row.continue()
            } catch { abort() }
          }
        } catch { abort() }
      })
    } finally { db.close() }
  } catch { /* Nothing more can be done for rows the browser will not let us delete. */ }
}
/** Deletes this Page's journal rows for one human and workspace. Best effort; never throws. */
export function purgeDocumentDrafts(human: string, workspace: string, page: string): Promise<void> {
  if (!human || !workspace || !page) return Promise.resolve()
  const owner = JSON.stringify([human, workspace, page])
  return purgeRows(() => IDBKeyRange.bound([owner, ""], [owner, "\uffff"]), () => true)
}
/**
 * Deletes every journal row not owned by `userId` (all rows when null: sign-out).
 * A shared browser never keeps one person's private text for the next sign-in.
 */
export function purgeOtherDocumentDrafts(userId: string | null): Promise<void> {
  journalHuman = userId
  const barriers: Promise<void>[] = []
  for (const [key, writer] of draftWriters) {
    if (userId !== null && writer.human === userId) continue
    draftWriters.delete(key)
    barriers.push(writer.retire())
  }
  // Serializing transitions prevents an older purge from deleting a new
  // principal's edits after that principal has been exposed by the auth hook.
  const drained = Promise.allSettled(barriers)
  identityPurge = identityPurge.catch(() => {}).then(() => drained).then(() => purgeRows(() => undefined, owner => {
    try { return !userId || JSON.parse(owner)[0] !== userId } catch { return true }
  }))
  return identityPurge
}
