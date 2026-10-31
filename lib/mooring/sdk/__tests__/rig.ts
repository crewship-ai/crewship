/** Test rig for the SDK contract tests. Only the server transport and the
 * private journal are synthetic; the lifecycle is the real one. */
import { vi } from "vitest"
import { createSingleEditorSave, type RecoveryDraft, type RecoveryJournal, type SaveRequest } from ".."

export const deferred = <T>() => { let resolve!: (value: T) => void, reject!: (error: unknown) => void; const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail }); return { promise, resolve, reject } }
export const receipt = (r: SaveRequest) => ({ durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash, workspaceId: r.workspaceId, pageId: r.pageId, applicationId: r.applicationId, enginePin: r.enginePin, generation: r.generation, sessionId: r.sessionId, baseVersion: r.baseVersion, editorRevision: r.editorRevision, contentHash: r.contentHash, version: (BigInt(r.baseVersion) + 1n).toString() })

/** In-memory journal with the real journal's contract: one row per mount,
 * `recovery()` lists other mounts' rows, `confirm` removes only an exact row. */
export function memoryJournal(rows: Map<string, RecoveryDraft> = new Map(), id = crypto.randomUUID()) {
  const listeners = new Set<() => void>()
  const same = (a: RecoveryDraft, b: RecoveryDraft) => a.editorRevision === b.editorRevision && a.baseVersion === b.baseVersion && a.generation === b.generation && a.bytes.length === b.bytes.length && a.bytes.every((v, i) => v === b.bytes[i])
  const journal = {
    rows,
    persist: vi.fn(async (draft: RecoveryDraft) => { rows.set(id, { ...draft, bytes: draft.bytes.slice() }) }),
    confirm: vi.fn(async (draft: RecoveryDraft) => { const row = rows.get(id); if (row && same(row, draft)) rows.delete(id) }),
    recovery: vi.fn(async () => [...rows].filter(([key]) => key !== id).map(([, draft]) => ({ draft: { ...draft, bytes: draft.bytes.slice() }, actions: ["compare", "copy"] as const }))),
    onRetire: vi.fn((listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener) } }),
    close: vi.fn(async () => {}),
    revokeRead: vi.fn(async () => { rows.clear() }),
  } satisfies RecoveryJournal & { rows: Map<string, RecoveryDraft> }
  return journal
}

export function saveRig(options: { baseVersion?: string; generation?: string; journal?: ReturnType<typeof memoryJournal> } = {}) {
  const journal = options.journal ?? memoryJournal()
  let text = "a", revision = 0
  const engine = {
    snapshot: vi.fn(async () => ({ bytes: new TextEncoder().encode(text), editorRevision: `note:${revision}` })),
    validate: vi.fn(async () => {}),
  }
  const transport = { save: vi.fn(async (r: SaveRequest): Promise<unknown> => receipt(r)), reconcile: vi.fn(async (r: SaveRequest): Promise<unknown> => receipt(r)) }
  const states: { phase: string; dirty: boolean; reason?: string }[] = []
  const controller = createSingleEditorSave({ workspaceId: "w", pageId: "p", applicationId: "notecraft", enginePin: "pin", generation: options.generation ?? "1", sessionId: "tab-a", baseVersion: options.baseVersion ?? "1", journal, engine, transport, onState: s => states.push(s), idleMs: 20 })
  const type = (value: string) => { text = value; revision++; controller.edited() }
  return { journal, engine, transport, states, controller, type, last: () => states.at(-1)! }
}
