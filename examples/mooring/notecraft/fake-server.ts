/** In-memory stand-in for the Go backend's Craft routes, for NoteCraft's
 * tests and local demo only. It applies the same fences the server applies:
 * one lease holder, a generation per custody, every write checked against
 * generation and base version, restore as a new version. It is not a server. */
import { SaveError, type LeaseActor, type LeaseTransport, type RecoveryDraft, type RecoveryJournal, type SaveRequest, type VersionsTransport } from "../../../lib/mooring/sdk"

interface StoredVersion { version: string; bytes: Uint8Array; contentHash?: string; sessionId?: string; generation?: string; editorRevision?: string; restoredFrom?: string; createdAt: string }
export interface FakeCraftClient extends LeaseTransport {
  read(signal?: AbortSignal): Promise<{ version: string; bytes: Uint8Array }>
  save(request: SaveRequest, signal?: AbortSignal): Promise<unknown>
  reconcile(request: SaveRequest, signal?: AbortSignal): Promise<unknown>
  versions: VersionsTransport
}

export function createFakeCraftServer(initial = "") {
  const versions: StoredVersion[] = [{ version: "1", bytes: new TextEncoder().encode(initial), createdAt: new Date(0).toISOString() }]
  let holder: LeaseActor | null = null, generation = 0n, requests: (LeaseActor & { request_id: string })[] = []
  const head = () => versions.at(-1)!
  const owns = (actor: LeaseActor, g: string | undefined) => holder?.actor_id === actor.actor_id && g === String(generation)
  const receipt = (r: SaveRequest, version: string) => ({ durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash, workspaceId: r.workspaceId, pageId: r.pageId, applicationId: r.applicationId, enginePin: r.enginePin, generation: r.generation, sessionId: r.sessionId, baseVersion: r.baseVersion, editorRevision: r.editorRevision, contentHash: r.contentHash, version })
  const server = {
    get head() { return head().version },
    text: (version = head().version) => new TextDecoder().decode(versions.find(v => v.version === version)!.bytes),
    get generation() { return String(generation) },
    /** Another actor takes custody, as after an expired lease. */
    takeOver(actor: LeaseActor) { holder = actor; generation++; requests = [] },
    connect(actor: LeaseActor): FakeCraftClient {
      const client: FakeCraftClient = {
        async lease() {
          const mine = holder?.actor_id === actor.actor_id
          return { holder, can_edit: true, mine, generation: holder ? String(generation) : null, can_write: mine, acquired_at: holder ? new Date().toISOString() : null, expires_at: holder ? new Date(Date.now() + 3_600_000).toISOString() : null, handoff_requested_by: mine ? requests.map(r => ({ ...r })) : [] }
        },
        async leaseAction(action, body) {
          if (action === "acquire") {
            if (holder && holder.actor_id !== actor.actor_id) throw new SaveError("conflict")
            if (!holder) { holder = actor; generation++ }
          } else if (action === "request-handoff") {
            if (holder && holder.actor_id !== actor.actor_id && !requests.some(r => r.actor_id === actor.actor_id)) requests.push({ ...actor, request_id: `req-${actor.actor_id}` })
          } else if (action === "release") {
            if (!owns(actor, body.generation)) throw new SaveError("conflict")
            holder = null; requests = []
          } else {
            const target = requests.find(r => r.request_id === body.request_id)
            if (!owns(actor, body.generation) || !target) throw new SaveError("conflict")
            holder = { actor_id: target.actor_id, display_name: target.display_name, kind: target.kind }; generation++; requests = []
          }
          return {}
        },
        async read() { return { version: head().version, bytes: head().bytes.slice() } },
        async save(r) {
          if (!owns(actor, r.generation) || r.baseVersion !== head().version) throw new SaveError("conflict")
          const version = String(BigInt(r.baseVersion) + 1n)
          versions.push({ version, bytes: r.bytes.slice(), contentHash: r.contentHash, sessionId: r.sessionId, generation: r.generation, editorRevision: r.editorRevision, createdAt: new Date().toISOString() })
          return receipt(r, version)
        },
        async reconcile(r) {
          const next = versions.find(v => v.version === String(BigInt(r.baseVersion) + 1n))
          if (!next || next.contentHash !== r.contentHash || next.sessionId !== r.sessionId || next.generation !== r.generation || next.editorRevision !== r.editorRevision) throw new SaveError("conflict")
          return receipt(r, next.version)
        },
        versions: {
          async list() { return { head: head().version, versions: versions.map(v => ({ version: v.version, createdAt: v.createdAt, ...(v.restoredFrom ? { restoredFrom: v.restoredFrom } : {}) })) } },
          async restore({ target, base, generation: g }) {
            const source = versions.find(v => v.version === target)
            if (!owns(actor, g) || base !== head().version || !source) throw new SaveError("conflict")
            const version = String(BigInt(base) + 1n)
            versions.push({ version, bytes: source.bytes.slice(), restoredFrom: target, createdAt: new Date().toISOString() })
            return { version }
          },
        },
      }
      return client
    },
  }
  return server
}

/** Journal double with the private journal's contract: one row per mount,
 * shared rows per browser profile, `recovery()` lists other mounts' rows. */
export function memoryJournal(rows: Map<string, RecoveryDraft>, id = Math.random().toString(36).slice(2)): RecoveryJournal {
  let current: RecoveryDraft | undefined
  const listeners = new Set<() => void>()
  return {
    async persist(draft) { current = { ...draft, bytes: draft.bytes.slice() }; rows.set(id, current) },
    async confirm(draft) { if (current && current.editorRevision === draft.editorRevision && current.baseVersion === draft.baseVersion && current.bytes.length === draft.bytes.length && current.bytes.every((b, i) => b === draft.bytes[i])) { rows.delete(id); current = undefined } },
    async recovery() { return [...rows].filter(([key]) => key !== id).map(([, draft]) => ({ draft: { ...draft, bytes: draft.bytes.slice() }, actions: ["compare", "copy"] as const })) },
    onRetire(listener) { listeners.add(listener); return () => { listeners.delete(listener) } },
    async close() { for (const listener of listeners) listener() },
    async revokeRead() { rows.clear() },
  }
}
