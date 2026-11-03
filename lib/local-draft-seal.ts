import { apiFetch } from "./api-fetch"

export interface LocalDraftScope { human: string; workspace: string; page: string }
export interface SealedLocalDraft { format: "crewship-local-draft-v1"; envelope: string; iv: string; ciphertext: string }
export class LocalDraftProtectionError extends Error {}
export class LocalDraftReadRevoked extends LocalDraftProtectionError {}
export interface LocalDraftCipher {
  seal(text: string, purpose: string): Promise<string>
  open(sealed: string, purpose: string): Promise<string>
  close(): void
}
const format = "crewship-local-draft-v1" as const
const unavailable = () => new LocalDraftProtectionError("Private draft protection is unavailable. Keep your open copy.")
let principal: string | null | undefined
let identityGeneration = 0
const handles = new Set<LocalDraftScope & { generation: number; close(): void }>()

/** Block old reads/new admissions immediately; admitted writes drain before finish. */
export function beginLocalDraftIdentity(human: string | null) { if (principal !== human) identityGeneration++; principal = human; return identityGeneration }
/** Call after mounted writers and strict storage transactions have drained. */
export function finishLocalDraftIdentity(human: string | null, generation = identityGeneration) {
  if (generation !== identityGeneration || principal !== human) return // a newer identity transition superseded this drain
  for (const handle of handles) if (handle.human !== human || handle.generation !== identityGeneration) handle.close()
}
export function forgetLocalDraftPage(scope: LocalDraftScope) {
  for (const handle of handles) {
    if (handle.human === scope.human && handle.workspace === scope.workspace && handle.page === scope.page) handle.close()
  }
}
function base64(bytes: Uint8Array): string {
  let raw = ""
  for (let i = 0; i < bytes.length; i += 32768) raw += String.fromCharCode(...bytes.subarray(i, i + 32768))
  return btoa(raw)
}
function bytes(raw: string, max: number): Uint8Array {
  if (raw.length > Math.ceil(max / 3) * 4) throw unavailable()
  let decoded: string
  try { decoded = atob(raw) } catch { throw unavailable() }
  if (decoded.length > max) throw unavailable()
  return Uint8Array.from(decoded, char => char.charCodeAt(0))
}
export function isSealedLocalDraft(raw: string): boolean {
  try { return JSON.parse(raw)?.format === format } catch { return false }
}
/** Each open performs fresh server admission. Data keys stay only in this handle's RAM. */
export async function createLocalDraftCipher(scope: LocalDraftScope): Promise<LocalDraftCipher> {
  scope = { ...scope }
  if (![scope.human, scope.workspace, scope.page].every(value => value && value.length <= 256 && !value.includes("\0"))) throw unavailable()
  if (principal !== undefined && principal !== scope.human) throw unavailable()
  const lifetime = new AbortController()
  let active = true
  const keys = new Map<string, Promise<CryptoKey>>()
  let writer: CryptoKey | undefined, writerEnvelope = ""
  const handle = { ...scope, generation: identityGeneration, close() { active = false; lifetime.abort(); writer = undefined; keys.clear(); handles.delete(handle) } }
  handles.add(handle)
  const ready = (read = true) => {
    if (!active || lifetime.signal.aborted || (read && ((principal !== undefined && principal !== scope.human) || handle.generation !== identityGeneration))) throw unavailable()
  }
  const aad = (purpose: string) => new TextEncoder().encode(JSON.stringify([format, scope.human, scope.workspace, scope.page, purpose]))
  async function request(operation: "wrap" | "unwrap", value: string): Promise<Record<string, unknown>> {
    ready()
    const response = await apiFetch(`/api/v1/pages/@${encodeURIComponent(scope.page)}/draft-key?workspace_id=${encodeURIComponent(scope.workspace)}`, {
      method: "POST", cache: "no-store", redirect: "error", skipRefresh: true, signal: lifetime.signal,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ human: scope.human, operation, [operation === "wrap" ? "key" : "envelope"]: value }),
    })
    ready()
    const body = await response.json().catch(() => null)
    ready()
    if (response.status === 403 && body?.code === "draft_read_revoked") { forgetLocalDraftPage(scope); throw new LocalDraftReadRevoked("Current Page read access was removed.") }
    if (!response.ok || !body || typeof body !== "object" || Array.isArray(body)) throw unavailable()
    return body
  }
  async function importKey(encoded: string): Promise<CryptoKey> {
    const raw = bytes(encoded, 32)
    if (raw.length !== 32) throw unavailable()
    try { return await crypto.subtle.importKey("raw", new Uint8Array(raw), "AES-GCM", false, ["encrypt", "decrypt"]) }
    finally { raw.fill(0) }
  }
  try {
    // Prepare before editing, so offline changes can still be sealed without
    // obtaining a new key after the session has expired.
    const raw = crypto.getRandomValues(new Uint8Array(32))
    try {
      const result = await request("wrap", base64(raw))
      if (typeof result.envelope !== "string" || result.envelope.length > 2048 || !/^v[1-9]\d?:/.test(result.envelope)) throw unavailable()
      writerEnvelope = result.envelope
      writer = await importKey(base64(raw)); ready()
    } finally { raw.fill(0) }
  } catch (error) { handle.close(); throw error }
  return {
    async seal(text, purpose) {
      // Old admitted writers may finish encrypted persistence during identity
      // retirement; no plaintext read is released across that transition.
      ready(false)
      if (!writer || typeof text !== "string" || !purpose || purpose.length > 1024) throw unavailable()
      const plain = new TextEncoder().encode(text)
      if (plain.length > 64 * 1024 * 1024) throw unavailable()
      const iv = crypto.getRandomValues(new Uint8Array(12))
      try {
        const encrypted = await crypto.subtle.encrypt({ name: "AES-GCM", iv, additionalData: aad(purpose) }, writer, new Uint8Array(plain))
        ready(false)
        return JSON.stringify({ format, envelope: writerEnvelope, iv: base64(iv), ciphertext: base64(new Uint8Array(encrypted)) } satisfies SealedLocalDraft)
      } finally { plain.fill(0) }
    },
    async open(raw, purpose) {
      ready()
      let value: SealedLocalDraft
      if (raw.length > 96 * 1024 * 1024) throw unavailable()
      try { value = JSON.parse(raw) } catch { throw unavailable() }
      if (value?.format !== format || typeof value.envelope !== "string" || value.envelope.length > 2048 || typeof value.iv !== "string" || typeof value.ciphertext !== "string" || !purpose || purpose.length > 1024) throw unavailable()
      const iv = bytes(value.iv, 12)
      if (iv.length !== 12) throw unavailable()
      let key = keys.get(value.envelope)
      if (!key) {
        key = request("unwrap", value.envelope).then(result => {
          ready()
          if (typeof result.key !== "string") throw unavailable()
          return importKey(result.key)
        })
        keys.set(value.envelope, key)
      }
      try {
        const decrypted = await crypto.subtle.decrypt({ name: "AES-GCM", iv: new Uint8Array(iv), additionalData: aad(purpose) }, await key, new Uint8Array(bytes(value.ciphertext, 64 * 1024 * 1024 + 16)))
        ready()
        const plain = new Uint8Array(decrypted)
        try { return new TextDecoder("utf-8", { fatal: true }).decode(plain) } finally { plain.fill(0) }
      } catch (error) { if (error instanceof LocalDraftReadRevoked) throw error; throw unavailable() }
    },
    close: handle.close,
  }
}
