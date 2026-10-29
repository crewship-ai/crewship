/**
 * Private, browser-local drafts of artifact work: the unsaved Project source
 * and unsent comments. A draft is scoped to one person, workspace and project.
 *
 * Drafts live in IndexedDB and every write uses a `durability: "strict"`
 * transaction, so a write has reached disk when its promise resolves. The
 * earlier localStorage copy was lost when the whole browser was killed within
 * a few seconds of typing (Chromium writes localStorage to disk in batches).
 * localStorage remains only as a fallback when IndexedDB is unavailable and as
 * a one-time migration source for drafts written by older builds.
 *
 * Drafts are private text: sign-out deletes all of them and an identity change
 * deletes everyone else's (see purgeOtherArtifactDrafts and hooks/use-auth).
 */

export type ArtifactDraftKind = "artifact-source" | "artifact-comment" | "artifact-viewer-comment"
const KINDS: readonly ArtifactDraftKind[] = ["artifact-source", "artifact-comment", "artifact-viewer-comment"]
type Row = [human: string, kind: ArtifactDraftKind, workspace: string, target: string]

/** Durable storage under the draft lanes; replaceable in tests. Every method
 * resolves only once the change is durable and rejects when it is not. */
export interface ArtifactDraftStore {
  get(row: Row): Promise<string | undefined>
  put(row: Row, text: string): Promise<void>
  delete(row: Row): Promise<void>
  /** Deletes every draft not owned by `keep` (all drafts when null). */
  purge(keep: string | null): Promise<void>
}

/** Opaque draft identity used by components; null without a signed-in person. */
export function artifactDraftKey(kind: ArtifactDraftKind, human: string | null | undefined, workspace: string, target: string | null | undefined): string | null {
  if (!human || !workspace || !target) return null
  return JSON.stringify([human, kind, workspace, target] satisfies Row)
}

function parse(key: string): Row | null {
  try {
    const row = JSON.parse(key)
    if (Array.isArray(row) && row.length === 4 && row.every(part => typeof part === "string" && part) && KINDS.includes(row[1])) return row as Row
  } catch { /* Not a draft key. */ }
  return null
}
const legacyKey = ([human, kind, workspace, target]: Row) => `${kind}:${human}:${workspace}:${target}`

const OPEN_TIMEOUT_MS = 2000
const DB = "crewship-artifact-drafts"
const STORE = "drafts"
let opening: Promise<IDBDatabase> | undefined

function openDatabase(): Promise<IDBDatabase> {
  if (opening) return opening
  const attempt = new Promise<IDBDatabase>((resolve, reject) => {
    let settled = false
    const fail = () => { if (settled) return; settled = true; clearTimeout(timer); reject(new Error("Artifact draft storage is unavailable.")) }
    const timer = setTimeout(fail, OPEN_TIMEOUT_MS)
    try {
      const request = indexedDB.open(DB, 1)
      request.onupgradeneeded = () => request.result.createObjectStore(STORE, { keyPath: "key" })
      request.onerror = request.onblocked = fail
      request.onsuccess = () => {
        const db = request.result
        if (settled) { db.close(); return }
        settled = true; clearTimeout(timer)
        db.onversionchange = () => { db.close(); if (opening === attempt) opening = undefined }
        db.onclose = () => { if (opening === attempt) opening = undefined }
        resolve(db)
      }
    } catch { fail() }
  })
  opening = attempt
  attempt.catch(() => { if (opening === attempt) opening = undefined })
  return attempt
}

async function transact<T>(mode: IDBTransactionMode, action: (store: IDBObjectStore) => IDBRequest<T> | void): Promise<T | undefined> {
  const db = await openDatabase()
  return new Promise<T | undefined>((resolve, reject) => {
    let settled = false
    let request: IDBRequest<T> | void
    const finish = (ok: boolean) => {
      if (settled) return
      settled = true; clearTimeout(timer)
      if (ok) resolve(request ? request.result : undefined)
      else reject(new Error("Artifact draft storage is unavailable."))
    }
    const timer = setTimeout(() => { try { tx.abort() } catch { /* Already finished. */ } finish(false) }, OPEN_TIMEOUT_MS)
    // Strict: the transaction completes only after the browser flushed it to
    // disk, so a resolved write survives a killed browser process.
    const tx = db.transaction(STORE, mode, { durability: "strict" })
    tx.oncomplete = () => finish(true)
    tx.onabort = tx.onerror = () => finish(false)
    try { request = action(tx.objectStore(STORE)) } catch { try { tx.abort() } catch { /* Already finished. */ } finish(false) }
  })
}

const indexedDbStore: ArtifactDraftStore = {
  async get(row) {
    const found = await transact<{ text?: unknown } | undefined>("readonly", store => store.get(row))
    return typeof found?.text === "string" ? found.text : undefined
  },
  async put(row, text) { await transact("readwrite", store => store.put({ key: row, text })) },
  async delete(row) { await transact("readwrite", store => store.delete(row)) },
  async purge(keep) {
    await transact("readwrite", store => {
      if (keep === null) return store.clear()
      const cursor = store.openCursor()
      cursor.onsuccess = () => {
        const next = cursor.result
        if (!next) return
        if ((next.key as Row)[0] !== keep) next.delete()
        next.continue()
      }
    })
  },
}

let override: ArtifactDraftStore | undefined
/** Test seam: replaces IndexedDB with another durable store. */
export function setArtifactDraftStore(store: ArtifactDraftStore | undefined) { override = store }
const durable = (): ArtifactDraftStore | undefined => override ?? (typeof indexedDB === "undefined" ? undefined : indexedDbStore)

function legacyGet(row: Row): string | undefined {
  try { return localStorage.getItem(legacyKey(row)) ?? undefined } catch { return undefined }
}
function legacyRemove(row: Row) {
  try { localStorage.removeItem(legacyKey(row)) } catch { /* Unavailable storage holds nothing to remove. */ }
}

// One lane per draft: writes are ordered and coalesced (latest value wins), so
// typing never queues more than one strict transaction behind the running one.
interface Lane { next: string; scheduled?: Promise<void>; tail: Promise<void> }
const lanes = new Map<string, Lane>()
const writes = new Map<string, number>()

async function persist(row: Row, text: string): Promise<void> {
  const store = durable()
  if (store) {
    try {
      if (text) await store.put(row, text)
      else await store.delete(row)
      legacyRemove(row)
      return
    } catch { /* Fall back to localStorage below rather than lose the text. */ }
  }
  if (!text) { legacyRemove(row); if (!store) return; throw new Error("Artifact draft storage is unavailable.") }
  localStorage.setItem(legacyKey(row), text)
}

/**
 * Stores the draft (empty text deletes it). Resolves once the latest text is
 * durable; rejects when no browser storage accepted it, so the UI can ask the
 * person to copy the draft.
 */
export function writeArtifactDraft(key: string, text: string): Promise<void> {
  const row = parse(key)
  if (!row) return Promise.reject(new Error("Invalid artifact draft key."))
  writes.set(key, (writes.get(key) ?? 0) + 1)
  let lane = lanes.get(key)
  if (!lane) { lane = { next: text, tail: Promise.resolve() }; lanes.set(key, lane) }
  lane.next = text
  if (lane.scheduled) return lane.scheduled
  const current = lane
  const scheduled = current.tail.then(() => {
    current.scheduled = undefined
    return persist(row, current.next)
  })
  current.scheduled = scheduled
  current.tail = scheduled.then(() => {}, () => {}).then(() => { if (!current.scheduled && lanes.get(key) === current) lanes.delete(key) })
  return scheduled
}

/** Reads the draft, moving an older localStorage copy into IndexedDB once. */
export async function readArtifactDraft(key: string): Promise<string> {
  const row = parse(key)
  if (!row) return ""
  await lanes.get(key)?.tail
  const seen = writes.get(key) ?? 0
  const store = durable()
  let stored: string | undefined
  if (store) { try { stored = await store.get(row) } catch { /* Fall back to localStorage. */ } }
  // A localStorage copy is newer than IndexedDB: it is removed after every
  // durable write, so it exists only from an older build or a failed write.
  const legacy = legacyGet(row)
  if (legacy === undefined) return stored ?? ""
  // Migrate through the lane unless the person has typed since this read began.
  if (store && (writes.get(key) ?? 0) === seen) void writeArtifactDraft(key, legacy).catch(() => {})
  return legacy
}

/**
 * Deletes every artifact draft not owned by `userId`, or all of them when
 * null (sign-out), in IndexedDB and in localStorage. Pending writes finish
 * first so a late write cannot recreate a purged draft. Best effort; never throws.
 */
export async function purgeOtherArtifactDrafts(userId: string | null): Promise<void> {
  await Promise.all([...lanes.values()].map(lane => lane.tail))
  const store = durable()
  if (store) { try { await store.purge(userId) } catch { /* Storage the browser will not open holds nothing we can reach. */ } }
  try {
    for (const name of Object.keys(localStorage)) {
      const kind = KINDS.find(prefix => name.startsWith(`${prefix}:`))
      if (!kind) continue
      const owner = name.slice(kind.length + 1).split(":")[0]
      if (userId === null || owner !== userId) localStorage.removeItem(name)
    }
  } catch { /* Unavailable browser storage must not block signing out. */ }
}
