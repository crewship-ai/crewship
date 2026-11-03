import { createLocalDraftCipher, forgetLocalDraftPage, isSealedLocalDraft, LocalDraftReadRevoked, type LocalDraftCipher, type LocalDraftScope } from "./local-draft-seal"
import { captureDocumentDrafts, removeCapturedDocumentDrafts, type CapturedDocumentDraft } from "./document-queue"

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
 * New records contain ciphertext scoped to a person and immutable Page ID.
 * Expiry retains records and drains admitted writers. Recovery requires fresh
 * same-person server authorization. Explicit sign-out removes only confirmed
 * persistent revisions; newer writes survive an older sign-out acknowledgement.
 */

export type ArtifactDraftKind = "artifact-source" | "artifact-comment" | "artifact-viewer-comment" | "design-canvas" | "design-comment" | "wordcraft-pending"
const KINDS: readonly ArtifactDraftKind[] = ["artifact-source", "artifact-comment", "artifact-viewer-comment", "design-canvas", "design-comment", "wordcraft-pending"]
export type ArtifactDraftRow = [human: string, kind: ArtifactDraftKind, workspace: string, target: string]
type Row = ArtifactDraftRow
export interface CapturedArtifactDraft { row: Row; revision: string | null; text?: string }

/** Durable storage under the draft lanes; replaceable in tests. Every method
 * resolves only once the change is durable and rejects when it is not. */
export interface ArtifactDraftStore {
  get(row: Row): Promise<string | undefined>
  put(row: Row, text: string, revision?: string): Promise<void>
  delete(row: Row): Promise<void>
  /** Deletes every draft not owned by `keep` (all drafts when null). */
  purge(keep: string | null): Promise<void>
  /** Privacy migration: retain only sealed rows belonging to other identities. */
  purgeUnsealed?(keep: string | null): Promise<void>
  read?(row: Row): Promise<{ text: string; revision?: string } | undefined>
  /** Atomic compare-and-set: stores only when no row exists, in one
   * transaction. Resolves false when a row exists. */
  putIfAbsent?(row: Row, text: string, revision: string): Promise<boolean>
  capture(human: string, workspace?: string, page?: string): Promise<CapturedArtifactDraft[]>
  removeCaptured(rows: CapturedArtifactDraft[]): Promise<void>
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
  async put(row, text, revision = crypto.randomUUID()) { await transact("readwrite", store => store.put({ key: row, text, revision })) },
  async delete(row) { await transact("readwrite", store => store.delete(row)) },
  async putIfAbsent(row, text, revision) {
    let written = false
    // One readwrite transaction: IndexedDB serializes it against every other
    // tab's write to this store, so the check and the put cannot be split.
    await transact("readwrite", store => {
      const found = store.get(row)
      found.onsuccess = () => { if (found.result === undefined) { store.put({ key: row, text, revision }); written = true } }
    })
    return written
  },
  async read(row) {
    const found = await transact<{ text?: unknown; revision?: unknown } | undefined>("readonly", store => store.get(row))
    return typeof found?.text === "string" ? { text: found.text, revision: typeof found.revision === "string" ? found.revision : undefined } : undefined
  },
  async capture(human, workspace, page) {
    const rows: CapturedArtifactDraft[] = []
    await transact("readwrite", store => {
      const cursor = store.openCursor()
      cursor.onsuccess = () => {
        const row = cursor.result; if (!row) return
        const value = row.value
        if (Array.isArray(value.key) && value.key[0] === human && (!workspace || value.key[2] === workspace) && (!page || value.key[3] === page) && typeof value.text === "string") {
          const revision = typeof value.revision === "string" ? value.revision : crypto.randomUUID()
          if (!value.revision) row.update({...value, revision})
          rows.push({row: value.key, revision})
        }
        row.continue()
      }
    })
    return rows
  },
  async removeCaptured(rows) {
    await transact("readwrite", store => {
      for (const row of rows) { const get = store.get(row.row); get.onsuccess = () => { if (row.revision !== null ? get.result?.revision === row.revision : get.result && !get.result.revision && get.result.text === row.text) store.delete(row.row) } }
    })
  },
  async purgeUnsealed(keep) {
    await transact("readwrite", store => {
      const cursor = store.openCursor()
      cursor.onsuccess = () => {
        const row = cursor.result; if (!row) return
        const value = row.value
        if ((!keep || value.key?.[0] !== keep) && (typeof value.text !== "string" || !sealedAtRest(value.text))) row.delete()
        row.continue()
      }
    })
  },
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

function legacyKeys(row: Row): string[] {
  const keys = [legacyKey(row)]
  if (row[1] === "design-canvas") keys.push(`crewship-design:v2:${row[0]}:${row[2]}:${row[3]}`)
  if (row[1] === "design-comment") keys.push(`design-comment:v2:${row[0]}:${row[2]}:${row[3]}`)
  return keys
}
function legacyGet(row: Row): string | undefined {
  try { for (const key of legacyKeys(row)) { const text = localStorage.getItem(key); if (text !== null) return text } } catch { /* Preserve inaccessible storage. */ }
  return undefined
}
function legacyRemove(row: Row) {
  try { for (const key of legacyKeys(row)) localStorage.removeItem(key) } catch { /* Unavailable storage holds nothing to remove. */ }
}
const cipherCells = new Map<string, Promise<LocalDraftCipher>>()
const scopeFor = (row: Row): LocalDraftScope => ({ human: row[0], workspace: row[2], page: row[3] })
function cipherFor(key: string, row: Row) {
  let cell = cipherCells.get(key)
  if (!cell) { cell = createLocalDraftCipher(scopeFor(row)); cipherCells.set(key, cell); void cell.catch(() => { if (cipherCells.get(key) === cell) cipherCells.delete(key) }) }
  return cell
}
async function freshCipher(key: string, row: Row) {
  const next = createLocalDraftCipher(scopeFor(row)), previous = cipherCells.get(key)
  const ready = await next
  cipherCells.set(key, next)
  if (previous) void (lanes.get(key)?.tail ?? Promise.resolve()).then(() => previous.then(value => value.close(), () => {}))
  return ready
}

// One lane per draft: writes are ordered and coalesced (latest value wins), so
// typing never queues more than one strict transaction behind the running one.
interface Lane { next: string; nextSeq?: number; scheduled?: Promise<void>; tail: Promise<void>; failed?: boolean; merging?: number }
const lanes = new Map<string, Lane>()
const writes = new Map<string, number>()
// The newest write number (see `writes`, fixed when the write is admitted)
// whose text is durably stored.
const durableWrites = new Map<string, number>()
const stored = (key: string, seq: number) => { if (seq > (durableWrites.get(key) ?? 0)) durableWrites.set(key, seq) }

// The person whose drafts may be written: undefined until the auth hook first
// settles, null after sign-out. Set by purgeOtherArtifactDrafts.
let owner: string | null | undefined
const retired = (row: Row) => owner !== undefined && owner !== row[0]
const retiredError = () => new Error("This browser is no longer signed in as the draft's owner.")
let identityPurge: Promise<void> = Promise.resolve()

// `over` for a fallback copy: the IndexedDB revision it supersedes, null when
// IndexedDB held none, undefined when that could not be determined.
const successfulWrites = new Map<string, { text: string; revision: string; fallback?: string; over?: string | null }>()
async function persist(row: Row, text: string, seq: number): Promise<void> {
  const key = JSON.stringify(row), revision = crypto.randomUUID()
  // Admission happened in writeArtifactDraft. Already admitted writes must
  // finish before identity retirement; later writes are rejected at the call.
  let sealed = ""
  if (text) {
    try { sealed = await (await cipherFor(JSON.stringify(row), row)).seal(text, row[1]) }
    catch (error) { if (error instanceof LocalDraftReadRevoked) void purgeLocalDraftPage(scopeFor(row)).catch(() => {}); throw error }
  }
  const store = durable()
  // The IndexedDB revision a fallback copy would supersede, read before this
  // write: a revision another tab stores later is newer and must stay.
  let over: string | null | undefined
  if (text && store?.read) { try { over = (await store.read(row))?.revision ?? null } catch { /* unknown: the copy must not be cleared blindly */ } }
  if (store) {
    try {
      if (text) await store.put(row, sealed, revision)
      else await store.delete(row)
      legacyRemove(row)
      successfulWrites.set(key, { text, revision })
      stored(key, seq)
      return
    } catch { /* Fall back to localStorage below rather than lose the text. */ }
  }
  if (!text) { legacyRemove(row); if (!store) { stored(key, seq); return } throw new Error("Artifact draft storage is unavailable.") }
  localStorage.setItem(legacyKey(row), sealed)
  stored(key, seq)
  // Remember the fallback copy so a save, discard or sign-out can remove it.
  successfulWrites.set(key, { text, revision, fallback: sealed, over })
}

/**
 * Leaving the page (#4823). A strict write is asynchronous (seal, then the
 * IndexedDB transaction) and dies with the document, so an edit typed just
 * before a navigation or tab close may never be stored. Nothing can seal it
 * synchronously without an unauthenticated record or raw key bytes in the
 * page, so the browser asks before leaving while any draft's newest write is
 * not durable yet. A pagehide without beforeunload (a killed mobile tab)
 * cannot ask; that edit can be lost.
 *
 * States per draft: "queued" (typed: accepted into the lane, RAM only) and
 * "durable" (strict IndexedDB commit, or the sealed localStorage fallback).
 * Server confirmation belongs to the editor: its save ACK clears the draft
 * with clearArtifactDraft.
 */
function uncommittedDrafts(): number {
  let count = 0
  for (const [key, lane] of lanes) {
    if ((writes.get(key) ?? 0) <= (durableWrites.get(key) ?? 0)) continue
    // A pending deletion loses no work.
    if (!lane.next && !lane.merging && !lane.failed) continue
    count++
  }
  return count
}
let unloadHooked = false
function hookUnload() {
  if (unloadHooked || typeof window === "undefined") return
  unloadHooked = true
  window.addEventListener("beforeunload", event => { if (uncommittedDrafts() > 0) { event.preventDefault(); event.returnValue = "" } })
}
export type ArtifactDraftStorageState = "none" | "queued" | "durable"
/** Where the newest write of this draft is: see "Leaving the page" above. */
export function artifactDraftStorageState(key: string): ArtifactDraftStorageState {
  const seq = writes.get(key) ?? 0
  if (!seq) return "none"
  return seq <= (durableWrites.get(key) ?? 0) ? "durable" : "queued"
}

/**
 * Stores the draft (empty text deletes it). Resolves once the latest text is
 * durable; rejects when no browser storage accepted it, so the UI can ask the
 * person to copy the draft.
 */
function queueArtifactDraft(key: string, text: string, admitted = false): Promise<void> {
  const row = parse(key)
  if (!row) return Promise.reject(new Error("Invalid artifact draft key."))
  if (!admitted && retired(row)) return Promise.reject(retiredError())
  writes.set(key, (writes.get(key) ?? 0) + 1)
  hookUnload()
  let lane = lanes.get(key)
  if (!lane) { lane = { next: text, tail: Promise.resolve() }; lanes.set(key, lane) }
  lane.next = text
  lane.nextSeq = writes.get(key)
  if (lane.scheduled) return lane.scheduled
  const current = lane
  // A write also waits for an identity purge that began before it, so that
  // purge cannot delete what the newly signed-in person types next.
  // A retirement capture already owns its private cipher and follows its
  // lane tail. Waiting for the drain that is waiting for this new lane cycles.
  const purging = admitted ? Promise.resolve() : identityPurge
  const scheduled = Promise.all([current.tail, purging]).then(() => {
    current.scheduled = undefined
    // Coalesced: this persist stores the newest admitted text.
    return persist(row, current.next, current.nextSeq ?? 0)
  })
  current.scheduled = scheduled
  current.tail = scheduled.then(() => { current.failed = false }, () => { current.failed = true }).then(() => { if (!current.scheduled && !current.failed && !current.merging && lanes.get(key) === current) lanes.delete(key) })
  return scheduled
}

// A stop merged after an identity change cannot open a row another tab wrote
// meanwhile. It appends its own sealed part instead; the owner's next load
// opens every part and combines them. Parts are sealed like any other row.
const BUNDLE = "crewship-local-draft-bundle-v1"
const MAX_PARTS = 64
function bundleParts(raw: string): string[] | undefined {
  try {
    const value = JSON.parse(raw)
    if (value?.format === BUNDLE && Array.isArray(value.parts) && value.parts.length >= 1 && value.parts.length <= MAX_PARTS && value.parts.every((part: unknown) => typeof part === "string" && isSealedLocalDraft(part))) return value.parts
  } catch { /* Not a bundle. */ }
  return undefined
}
const sealedAtRest = (raw: string) => isSealedLocalDraft(raw) || bundleParts(raw) !== undefined
type LaneRead = { text: string } | { append: string[] }

/** The stored text, read inside the draft's own lane (no lane wait, no
 * migration write). A failed read rejects: the caller must not merge into an
 * assumed-empty draft. Uses the cipher this page already admitted. */
async function readInLane(key: string, row: Row, combine?: (parts: string[]) => string): Promise<LaneRead> {
  const store = durable()
  const record = store ? (store.read ? await store.read(row) : await store.get(row).then(text => text === undefined ? undefined : { text, revision: undefined })) : undefined
  const legacy = legacyGet(row), raw = legacy ?? record?.text
  if (raw === undefined) return { text: "" }
  // Appending needs no plaintext: only for a caller retired by an identity
  // change, with a combiner, on durable storage.
  const appendable = (error: unknown) => !(error instanceof LocalDraftReadRevoked) && retired(row) && legacy === undefined && !!combine && !!store
  const parts = bundleParts(raw)
  if (parts) {
    if (!combine || legacy !== undefined) throw new Error("Artifact draft storage could not be read.")
    try {
      const cipher = await cipherFor(key, row), texts: string[] = []
      for (const part of parts) texts.push(await cipher.open(part, row[1]))
      return { text: combine(texts) }
    } catch (error) { if (appendable(error)) return { append: parts }; throw error }
  }
  if (!isSealedLocalDraft(raw)) return { text: raw }
  try { return { text: await (await cipherFor(key, row)).open(raw, row[1]) } }
  catch (error) {
    // An identity change that began after this merge was admitted blocks
    // every plaintext read (beginLocalDraftIdentity), so the deferred open
    // fails. When the stored row is exactly the revision this tab itself last
    // stored or loaded, its text is already in this tab's RAM: merge into it
    // rather than lose the stop's originals. Any other row stays unreadable.
    const known = successfulWrites.get(key)
    if (!(error instanceof LocalDraftReadRevoked) && retired(row) && legacy === undefined && known && record?.revision !== undefined && known.revision === record.revision) return { text: known.text }
    // A row another tab wrote meanwhile: keep it as it is and add this stop.
    if (appendable(error)) return { append: [raw] }
    throw error
  }
}

/**
 * Read-modify-write of one draft, admitted at the call like a write: an
 * identity change that begins later waits for it (its lane tail), so the
 * person who stopped keeps the result and the next person never receives it.
 * The read runs at execution time under a cross-tab Web Lock, so a late
 * result never overwrites a newer draft. Resolves with the stored text ("" for
 * a caller retired by an identity change); rejects when nothing durable was
 * stored.
 */
export function mergeArtifactDraft(key: string, merge: (existing: string) => string, combine?: (parts: string[]) => string): Promise<string> {
  const row = parse(key)
  if (!row) return Promise.reject(new Error("Invalid artifact draft key."))
  if (retired(row)) return Promise.reject(retiredError())
  // A merge is not coalesced: its write number is fixed here, at admission.
  const seq = (writes.get(key) ?? 0) + 1
  writes.set(key, seq)
  hookUnload()
  let lane = lanes.get(key)
  if (!lane) { lane = { next: "", tail: Promise.resolve() }; lanes.set(key, lane) }
  const current = lane
  current.merging = (current.merging ?? 0) + 1
  const before = Promise.all([current.scheduled ?? Promise.resolve(), current.tail]).catch(() => {})
  const task = async () => {
    const existing = await readInLane(key, row, combine)
    if ("append" in existing) {
      // Retired and unable to open what is stored: seal this stop's own
      // result as one more part under the original owner's scope. Nothing
      // stored is opened or replaced; the owner's next load combines them.
      if (existing.append.length >= MAX_PARTS) throw new Error("Artifact draft storage is unavailable.")
      const sealed = await (await cipherFor(key, row)).seal(merge(""), row[1])
      await durable()!.put(row, JSON.stringify({ format: BUNDLE, parts: [...existing.append, sealed] }), crypto.randomUUID())
      successfulWrites.delete(key)
      stored(key, seq)
      current.failed = false
      return ""
    }
    const text = merge(existing.text)
    await persist(row, text, seq)
    // The merge is now the lane's latest durable value: an older failed write
    // must not be retried over it when the lane retires. A write queued after
    // this merge keeps its own (newer) value.
    current.failed = false
    if (!current.scheduled) current.next = text
    // A caller retired by an identity change gets no plaintext back.
    return retired(row) ? "" : text
  }
  const locks = typeof navigator === "undefined" ? undefined : navigator.locks
  const run = before.then(() => locks ? locks.request(`crewship-artifact-draft:${key}`, task) : task())
  current.tail = run.then(() => {}, () => {}).then(() => {
    current.merging! -= 1
    if (!current.scheduled && !current.failed && !current.merging && lanes.get(key) === current) lanes.delete(key)
  })
  return run
}

export function writeArtifactDraft(key: string, text: string): Promise<void> { return queueArtifactDraft(key, text) }
/** Captured only after a successful draft load. This capability may finish
 * local persistence during retirement; it never obtains new authority. */
export function admittedArtifactDraftCapture(key: string): (text: string) => Promise<void> {
  const cell = cipherCells.get(key)
  if (!cell) throw new Error("Private draft was not admitted.")
  return text => {
    if (cipherCells.get(key) !== cell) return Promise.reject(retiredError())
    return queueArtifactDraft(key, text, true)
  }
}
/**
 * Reads the draft ("" when there is none), moving an older localStorage copy
 * into IndexedDB once. Rejects when IndexedDB could not be read and no
 * localStorage copy exists: the draft may still be on disk, so a caller must
 * not treat the failure as "no draft" and write an empty or fresh value over it.
 */
export async function loadArtifactDraft(key: string, combine?: (parts: string[]) => string): Promise<string> {
  const row = parse(key)
  if (!row || retired(row)) return ""
  await lanes.get(key)?.tail
  const seen = writes.get(key) ?? 0
  const captured = await captureLocalDraftPage(scopeFor(row))
  try {
    const cipher = await freshCipher(key, row)
    if (retired(row)) throw retiredError()
    const store = durable()
    let stored: string | undefined, failed = false
    let persistedRevision: string | undefined
    if (store) { try { if (store.read) { const record = await store.read(row); stored = record?.text; persistedRevision = record?.revision } else stored = await store.get(row) } catch { failed = true } }
    const legacy = legacyGet(row), raw = legacy ?? stored
    if (raw === undefined) {
      if (failed) throw new Error("Artifact draft storage could not be read.")
      return ""
    }
    // A bundle is opened part by part and combined by its owner's editor.
    // Recovery never rewrites it: a write here would hold no cross-tab lock
    // and could replace a merge another tab committed meanwhile. The next
    // stop merge compacts it under the lock. Without a combiner it is unreadable.
    const parts = bundleParts(raw)
    if (parts && !combine) throw new Error("Artifact draft storage could not be read.")
    let text: string
    if (parts) { const texts: string[] = []; for (const part of parts) texts.push(await cipher.open(part, row[1])); text = combine!(texts) }
    else text = isSealedLocalDraft(raw) ? await cipher.open(raw, row[1]) : raw
    if (retired(row)) throw retiredError()
    if (persistedRevision && legacy === undefined && (writes.get(key) ?? 0) === seen) successfulWrites.set(key, { text, revision: persistedRevision })
    // A valid legacy row is sealed durably before it can enter the editor.
    if (!parts && (!isSealedLocalDraft(raw) || legacy !== undefined) && (writes.get(key) ?? 0) === seen) await writeArtifactDraft(key, text)
    if (retired(row)) throw retiredError()
    return text
  } catch (error) {
    if (error instanceof LocalDraftReadRevoked) await purgeLocalDraftPage(scopeFor(row), captured)
    throw error
  }
}

/** Like loadArtifactDraft, but reports a failed read as "". Only for callers
 * that never write back what they read. */
export function readArtifactDraft(key: string): Promise<string> {
  return loadArtifactDraft(key).catch(() => "")
}

// localStorage drafts swept with the artifact drafts. Each name continues with
// the owner's id; versioned prefixes come before their legacy forms.
const LOCAL_PREFIXES = ["crewship-design:v2:", "design-comment:v2:", ...KINDS.map(kind => `${kind}:`), "crewship-design:"]

/**
 * A Project source or comment draft that a build before Page-ID keys (#4628)
 * kept in localStorage under the Page *slug*. The record carries no Page
 * identity, and a slug can be renamed or reused by another Page (G4 B23b), so
 * it is never applied automatically and never deleted silently: the editor
 * shows it as an older draft that may belong to another Page, and only the
 * person's explicit choice adopts it (sealed under the Page ID) or discards
 * it. Only the signed-in person's own key is read; other accounts' copies are
 * removed on identity change and sign-out like every legacy plaintext copy.
 */
export type SlugArtifactDraftKind = "artifact-source" | "artifact-comment"
export interface SlugArtifactDraft { key: string; text: string; kind: SlugArtifactDraftKind; human: string; workspace: string; slug: string }
export function findSlugArtifactDraft(kind: SlugArtifactDraftKind, human: string | null | undefined, workspace: string, slug: string, pageId: string | null | undefined): SlugArtifactDraft | undefined {
  // slug === pageId is the Page-ID key itself, migrated by loadArtifactDraft.
  if (!human || !workspace || !slug || !pageId || slug === pageId) return undefined
  if (owner !== undefined && owner !== human) return undefined
  const key = legacyKey([human, kind, workspace, slug])
  try {
    const text = localStorage.getItem(key)
    // Builds before #4628 wrote only plaintext; a sealed value is not theirs.
    if (!text || isSealedLocalDraft(text)) return undefined
    return { key, text, kind, human, workspace, slug }
  } catch { return undefined }
}
/** Explicit adoption into an empty Page-ID draft, as a compare-and-set: the
 * text is sealed first, then written in one IndexedDB transaction only if no
 * Page-ID row exists. A draft any tab stored meanwhile, including during the
 * seal, is kept ("kept-existing") and so is the slug copy. Only after a durable
 * adoption is the slug copy removed, and only if it is still the one shown. A
 * failure rejects and keeps the slug copy. */
export async function adoptSlugArtifactDraft(found: SlugArtifactDraft, pageKey: string, text = found.text): Promise<"adopted" | "kept-existing"> {
  const row = parse(pageKey)
  if (!row || row[0] !== found.human || row[1] !== found.kind || row[2] !== found.workspace) throw new Error("Invalid artifact draft key.")
  if (retired(row)) throw retiredError()
  const store = durable()
  if (!store?.putIfAbsent || !text) throw new Error("Artifact draft storage is unavailable.")
  await lanes.get(pageKey)?.tail
  // A sealed fallback copy of a Page-ID draft is a draft too.
  if (legacyGet(row) !== undefined) return "kept-existing"
  const sealed = await (await cipherFor(pageKey, row)).seal(text, row[1])
  if (retired(row)) throw retiredError()
  const revision = crypto.randomUUID()
  if (!(await store.putIfAbsent(row, sealed, revision))) return "kept-existing"
  successfulWrites.set(pageKey, { text, revision })
  removeCapturedLegacyLocalDrafts([{ key: found.key, text: found.text }])
  return "adopted"
}
/** Explicit discard of the copy that was shown; a changed copy stays. */
export function discardSlugArtifactDraft(found: SlugArtifactDraft): void {
  removeCapturedLegacyLocalDrafts([{ key: found.key, text: found.text }])
}

export interface CapturedLegacyLocalDraft { key: string; text: string }
/** Old localStorage copies are included in sign-out's visible discard warning.
 * `includeSealed` also takes encrypted fallback copies (written when IndexedDB
 * refused the draft); sign-out removes them, an identity change keeps them. */
export function captureLegacyLocalDrafts(human?: string, includeSealed = false): CapturedLegacyLocalDraft[] {
  const rows: CapturedLegacyLocalDraft[] = []
  try {
    if (typeof localStorage === "undefined") return rows
    for (const key of Object.keys(localStorage)) {
      const prefix = LOCAL_PREFIXES.find(start => key.startsWith(start))
      if (!prefix || (human && key.slice(prefix.length).split(":")[0] !== human)) continue
      const text = localStorage.getItem(key)
      if (text !== null && (includeSealed || !isSealedLocalDraft(text))) rows.push({key, text})
    }
  } catch { /* Storage the browser will not let us read holds nothing we can protect or warn about. */ }
  return rows
}
export function removeCapturedLegacyLocalDrafts(rows: CapturedLegacyLocalDraft[]): void {
  try {
    for (const row of rows) if (localStorage.getItem(row.key) === row.text) localStorage.removeItem(row.key)
  } catch { /* Unavailable browser storage must not block signing out. */ }
}

/**
 * Deletes every artifact draft not owned by `userId`, or all of them when
 * null (sign-out), in IndexedDB and in localStorage, including the Design
 * canvas and Design comment drafts. From the call on, writes for anyone but
 * `userId` are refused (writer retirement); a write already running finishes
 * first so it cannot recreate a purged draft. Purges run one after another,
 * so an older sign-out never deletes the next person's drafts.
 * Best effort; never throws.
 */
export function purgeOtherArtifactDrafts(userId: string | null): Promise<void> {
  owner = userId
  // Only writes queued before this call are awaited; later ones wait for the purge.
  const pending = [...lanes.values()].map(lane => lane.tail)
  identityPurge = identityPurge.then(async () => {
    await Promise.all(pending)
    const store = durable()
    if (store) { try { await store.purge(userId) } catch { /* Storage the browser will not open holds nothing we can reach. */ } }
    try {
      for (const name of Object.keys(localStorage)) {
        const prefix = LOCAL_PREFIXES.find(start => name.startsWith(start))
        if (!prefix) continue
        const holder = name.slice(prefix.length).split(":")[0]
        if (userId === null || holder !== userId) localStorage.removeItem(name)
      }
    } catch { /* Unavailable browser storage must not block signing out. */ }
  })
  return identityPurge
}

/** Retire writes for other identities, preserving their durable encrypted rows. */
export function suspendOtherArtifactDrafts(userId: string | null): Promise<void> {
  const previous = owner
  owner = userId
  identityPurge = identityPurge.catch(() => {}).then(async () => {
    const retiring = [...lanes.entries()].filter(([key]) => { const row = parse(key); return row && (row[0] !== userId || previous !== userId) })
    await Promise.all(retiring.map(([, lane]) => lane.tail))
    for (const [key, lane] of retiring) {
      if (!lane.failed || lanes.get(key) !== lane) continue
      const row = parse(key)!
      await persist(row, lane.next, lane.nextSeq ?? 0) // retained admitted text, no new authority or server save
      lane.failed = false; lane.next = ""
      if (lanes.get(key) === lane) lanes.delete(key)
    }
    if (owner !== userId) return
    // No new identity enters an editor while the previous one's reachable
    // legacy plaintext remains. A failed cleanup keeps the auth curtain up.
    await durable()?.purgeUnsealed?.(userId)
    for (const row of captureLegacyLocalDrafts()) {
      const prefix = LOCAL_PREFIXES.find(start => row.key.startsWith(start))!
      if (userId === null || row.key.slice(prefix.length).split(":")[0] !== userId) removeCapturedLegacyLocalDrafts([row])
    }
  })
  void identityPurge.catch(() => {})
  return identityPurge
}
/** Release private RAM only after every editor has confirmed local capture. */
export function finishArtifactDraftIdentity(userId: string | null) {
  if (owner !== userId) return
  for (const [key, cell] of cipherCells) {
    const row = parse(key)
    if (row && row[0] !== userId) { cipherCells.delete(key); successfulWrites.delete(key); void cell.then(value => value.close(), () => {}) }
  }
}
/** Capture only persistent revisions that can later be removed with an atomic comparison. */
export async function captureArtifactDrafts(human: string, workspace?: string, page?: string): Promise<CapturedArtifactDraft[]> {
  return await durable()?.capture(human, workspace, page) ?? []
}
export async function removeCapturedArtifactDrafts(rows: CapturedArtifactDraft[]): Promise<void> {
  await durable()?.removeCaptured(rows)
}
export const artifactDraftRevision = (key: string) => writes.get(key) ?? 0
/** A save/comment ACK must never delete another tab's later revision. */
export async function clearArtifactDraft(key: string, text: string, revision: number): Promise<void> {
  await lanes.get(key)?.tail
  const row = parse(key), saved = successfulWrites.get(key)
  if (!row || !saved || saved.text !== text || artifactDraftRevision(key) !== revision) return
  if (saved.fallback !== undefined) {
    // The superseded IndexedDB revision goes first (revision-guarded: a newer
    // write from another tab stays). If that fails, the newer fallback copy
    // stays too, so the older revision is never what reopening finds.
    // An unknown superseded revision is uncertainty, not consent: removing the
    // copy could leave an older IndexedDB revision as the recovered draft, so
    // the copy stays. A verified read revocation still purges it
    // (purgeLocalDraftPage), independent of this.
    if (saved.over === undefined) { successfulWrites.delete(key); return }
    if (saved.over) await durable()?.removeCaptured([{ row, revision: saved.over }])
    try { if (localStorage.getItem(legacyKey(row)) === saved.fallback) localStorage.removeItem(legacyKey(row)) } catch { /* Unavailable storage holds nothing to remove. */ }
    successfulWrites.delete(key)
    return
  }
  await durable()?.removeCaptured([{ row, revision: saved.revision }])
}
export interface LocalDraftPageCapture { artifacts: CapturedArtifactDraft[]; documents: CapturedDocumentDraft[]; local: {key:string; text:string}[] }
/** Snapshot before the authority check, so a late refusal cannot erase a new revision. */
export async function captureLocalDraftPage(scope: LocalDraftScope): Promise<LocalDraftPageCapture> {
  const [artifacts, documents] = await Promise.all([captureArtifactDrafts(scope.human, scope.workspace, scope.page), captureDocumentDrafts(scope.human, scope.workspace, scope.page)])
  const local: {key:string; text:string}[] = []
  // Blocked localStorage must not fail a load that IndexedDB can serve.
  try {
    for (const kind of KINDS) for (const key of legacyKeys([scope.human, kind, scope.workspace, scope.page])) {
      const text = localStorage.getItem(key)
      if (text !== null) local.push({key,text})
    }
  } catch { /* Preserve inaccessible storage. */ }
  return {
    local,
    artifacts: artifacts.filter(({row}) => row[2] === scope.workspace && row[3] === scope.page),
    documents: documents.filter(({key}) => key[0] === JSON.stringify([scope.human, scope.workspace, scope.page])),
  }
}
/** Only call after a positively authenticated current-Page read revocation.
 * Other tabs delete their own newer revisions when they observe that revocation.
 * No immediate deletion is promised for an offline client. */
export async function purgeLocalDraftPage(scope: LocalDraftScope, captured?: LocalDraftPageCapture): Promise<void> {
  const known = captured ?? await captureLocalDraftPage(scope)
  forgetLocalDraftPage(scope)
  const matching = [...lanes.entries()].filter(([key]) => { const row = parse(key); return row && row[0] === scope.human && row[2] === scope.workspace && row[3] === scope.page })
  await Promise.all(matching.map(([, lane]) => lane.tail))
  for (const [key, lane] of matching) {
    if (lanes.get(key) === lane) lanes.delete(key)
    lane.next = ""
  }
  for (const kind of KINDS) {
    const key = JSON.stringify([scope.human, kind, scope.workspace, scope.page])
    cipherCells.delete(key); successfulWrites.delete(key)
  }
  await Promise.all([removeCapturedArtifactDrafts(known.artifacts), removeCapturedDocumentDrafts(known.documents)])
  try { for (const row of known.local) if (localStorage.getItem(row.key) === row.text) localStorage.removeItem(row.key) } catch { /* Unavailable storage holds nothing to remove. */ }
}
