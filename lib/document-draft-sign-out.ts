// The durable intent of a confirmed sign-out. A tab holding a draft may finish
// retiring long after the tab that signed out: it may redirect first, never hear
// the broadcast, or die. So the confirmation lives beside the rows, in the same
// object store, where a retiring editor's write reads it in its own transaction.
// A reserved row needs no schema version, which open tabs would block.
const OWNER = "sign-out"
const OPEN_TIMEOUT_MS = 2_000
export const signOutRowKey = (human: string): [string, string] => [OWNER, human]
export const isSignOutRow = (owner: unknown) => owner === OWNER

function withDrafts(action: (store: IDBObjectStore) => void): Promise<void> {
  if (typeof indexedDB === "undefined") return Promise.resolve()
  return new Promise<void>((resolve, reject) => {
    let settled = false, db: IDBDatabase | undefined
    const finish = (error?: Error) => { if (settled) return; settled = true; clearTimeout(timer); db?.close(); if (error) reject(error); else resolve() }
    const fail = () => finish(new Error("draft sign-out storage unavailable"))
    const timer = setTimeout(fail, OPEN_TIMEOUT_MS)
    const request = indexedDB.open("crewship-document-drafts", 1)
    request.onupgradeneeded = () => request.result.createObjectStore("drafts", { keyPath: "key" })
    request.onerror = request.onblocked = fail
    request.onsuccess = () => {
      db = request.result
      if (settled) { db.close(); return }
      try {
        const tx = db.transaction("drafts", "readwrite", { durability: "strict" })
        tx.oncomplete = () => finish()
        tx.onabort = tx.onerror = fail
        action(tx.objectStore("drafts"))
      } catch { fail() }
    }
  })
}

/** Records, durably, that this person confirmed erasing these chains. Later
 * sign-outs add to it. Written after the server acknowledged, before any cleanup. */
export function recordSignOut(human: string, roots: string[]): Promise<void> {
  return withDrafts(store => {
    const key = signOutRowKey(human), existing = store.get(key)
    existing.onsuccess = () => { store.put({ key, roots: [...new Set([...(existing.result?.roots ?? []), ...roots])] }) }
  })
}

/** Erases every row in a confirmed chain, for every recorded person. Only signing
 * in clears a person's intent: a signed-out mount cannot know that no tab of theirs
 * is still retiring, and a root names one chain, so keeping it costs nothing. */
export function sweepSignOuts(signedIn: string | null): Promise<void> {
  return withDrafts(store => {
    const intents = store.getAll(IDBKeyRange.bound([OWNER, ""], [OWNER, "￿"]))
    intents.onsuccess = () => {
      const confirmed = new Map<string, Set<string>>(intents.result.map(row => [row.key[1], new Set(row.roots)]))
      if (!confirmed.size) return
      const cursor = store.openCursor()
      cursor.onsuccess = () => {
        const row = cursor.result
        if (!row) { if (signedIn) store.delete(signOutRowKey(signedIn)); return }
        const value = row.value
        try {
          if (value.draft && confirmed.get(JSON.parse(value.key[0])[0])?.has(value.finalRoot ?? value.revision)) row.delete()
        } catch { /* Unknown legacy rows are preserved. */ }
        row.continue()
      }
    }
  })
}
