import { openProtectedDocumentDrafts as openDocumentDrafts } from "../protected-document-drafts"
import { DocumentStorageError, purgeDocumentDrafts, registerDocumentDraftWriter } from "../document-queue"

export interface NativeDraft {
  applicationId: string
  enginePin: string
  baseVersion: string
  generation: string
  sessionId: string
  editorRevision: string
  bytes: Uint8Array
}
export interface NativeRecovery { draft: NativeDraft; actions: readonly ["compare", "copy"] }
export interface NativeDraftJournal {
  persist(draft: NativeDraft): Promise<void>
  confirm: (draft: NativeDraft) => Promise<void>
  recovery(): Promise<NativeRecovery[]>
  onRetire(listener: () => void): () => void
  close(): Promise<void>
  revokeRead(): Promise<void>
}
// Raw native payload matches the server limit; base64 and journal metadata use a larger encoded budget.
const MAX_NATIVE_BYTES = 32 * 1024 * 1024
const MAX_ENCODED_BYTES = 64 * 1024 * 1024
const fail = () => new DocumentStorageError("Private native draft storage is unavailable; keep a recovery copy.")
const identifier = (s: unknown): s is string => typeof s === "string" && s.length > 0 && s.length <= 256 && !s.includes("\0")
export function validNativeVersion(value: unknown): value is string {
  return typeof value === "string" && /^(0|[1-9][0-9]{0,18})$/.test(value) && BigInt(value) <= 9223372036854775807n
}
export function validNativeDraft(d: NativeDraft): boolean {
  return identifier(d.applicationId) && identifier(d.enginePin) && identifier(d.sessionId)
    && validNativeVersion(d.baseVersion)
    && validNativeVersion(d.generation) && d.generation !== "0"
    && identifier(d.editorRevision)
    && d.bytes instanceof Uint8Array && d.bytes.byteLength <= MAX_NATIVE_BYTES
}
function encode(draft: NativeDraft): string {
  if (!validNativeDraft(draft)) throw fail()
  let binary = ""
  for (let i = 0; i < draft.bytes.length; i += 32768) binary += String.fromCharCode(...draft.bytes.subarray(i, i + 32768))
  return JSON.stringify({ kind: "native-whole-document-v1", applicationId: draft.applicationId, enginePin: draft.enginePin, baseVersion: draft.baseVersion, generation: draft.generation, sessionId: draft.sessionId, editorRevision: draft.editorRevision, nativeBase64: btoa(binary) })
}
function decode(snapshot: string): NativeDraft | null {
  try {
    if (snapshot.length > MAX_ENCODED_BYTES) return null
    const value = JSON.parse(snapshot)
    if (value?.kind !== "native-whole-document-v1" || typeof value.nativeBase64 !== "string") return null
    const binary = atob(value.nativeBase64)
    const draft: NativeDraft = { applicationId: value.applicationId, enginePin: value.enginePin, sessionId: value.sessionId, baseVersion: value.baseVersion, generation: value.generation, editorRevision: value.editorRevision, bytes: Uint8Array.from(binary, c => c.charCodeAt(0)) }
    return validNativeDraft(draft) ? draft : null
  } catch { return null }
}

/** Existing private IDB journal, with a separate row per mounted tab. Recovery
 * is data for compare/copy only; it never creates a write or adopts a lease. */
export async function openNativeDraftJournal(scope: { human: string; workspace: string; page: string; applicationId: string }): Promise<NativeDraftJournal> {
  scope = { ...scope }
  if (![scope.human, scope.workspace, scope.page, scope.applicationId].every(identifier)) throw fail()
  const id = `native:${crypto.randomUUID()}`
  let active = true, closed = false, closing = false, revoking = false
  let current: string | undefined
  let store: Awaited<ReturnType<typeof openDocumentDrafts>> | undefined
  let opening: Promise<Awaited<ReturnType<typeof openDocumentDrafts>>> | undefined
  let tail = Promise.resolve()
  let retired: Promise<void> | undefined, purged: Promise<void> | undefined
  let unregister = () => {}
  const listeners = new Set<() => void>()
  const closeStore = () => { if (!closed && store) { closed = true; store.close() } }
  const retire = () => {
    if (retired) return retired
    active = false
    unregister()
    retired = Promise.resolve(opening).catch(() => {}).then(() => tail)
    for (const listener of listeners) { try { listener() } catch { /* Observer failures cannot block privacy retirement. */ } }
    return retired
  }
  const close = () => {
    if (revoking) return purged!
    closing = true
    return retire().then(closeStore)
  }
  unregister = registerDocumentDraftWriter(scope.human, close, { storage: true, hasDraft: () => current !== undefined })
  try {
    if (!active) throw fail()
    opening = openDocumentDrafts(scope.human, scope.workspace, scope.page, { maxDraftBytes: MAX_ENCODED_BYTES, requireExclusive: true })
    store = await opening
    if (!active || !store.exclusive) { closeStore(); throw fail() }
  } catch (error) { active = false; unregister(); closeStore(); throw error }
  const journal = store
  function enqueue<T>(fn: () => Promise<T>): Promise<T> {
    if (!active) return Promise.reject(fail())
    const work = tail.then(async () => { if (!active) throw fail(); return fn() })
    tail = work.then(() => {}, () => {})
    return work.then(value => { if (!active) throw fail(); return value })
  }
  return {
    persist(draft) {
      if (!active || draft.applicationId !== scope.applicationId) return Promise.reject(fail())
      const snapshot = encode(draft)
      return enqueue(async () => {
        await journal.put({ id, schema: 1, epoch: "native-v1", update: "", attempted: true, snapshot })
        current = snapshot
      })
    },
    confirm: (draft) => {
      if (!active) return Promise.reject(fail())
      const snapshot = encode(draft)
      return enqueue(async () => { if (current === snapshot) { await journal.remove(id); current = undefined } })
    },
    recovery() {
      return enqueue(async () => {
        if (!journal.exclusive) return []
        const recovered: NativeRecovery[] = []
        for (const row of await journal.load()) {
          if (row.id === id || row.schema !== 1 || row.epoch !== "native-v1") continue
          const draft = decode(row.snapshot)
          if (draft?.applicationId === scope.applicationId) recovered.push({ draft, actions: ["compare", "copy"] })
        }
        return recovered
      })
    },
    onRetire(listener) {
      if (!active) listener()
      else listeners.add(listener)
      return () => { listeners.delete(listener) }
    },
    close,
    revokeRead() {
      if (purged) return purged
      // An old closed mount has no live admission to erase a fresh mount.
      if (closing || !active) return Promise.reject(fail())
      revoking = true
      let complete!: () => void, reject!: (cause: unknown) => void
      purged = new Promise<void>((resolve, fail) => { complete = resolve; reject = fail })
      // Install the promise before observers run, so a reentrant close joins
      // this purge and cannot release the exclusive Page lock prematurely.
      void retire().then(() => purgeDocumentDrafts(scope.human, scope.workspace, scope.page)).then(
        () => { closeStore(); complete() },
        error => { closeStore(); reject(error) },
      )
      return purged
    },
  }
}
