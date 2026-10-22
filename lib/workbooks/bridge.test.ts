import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { SaveEvent } from "@/lib/editor-lifecycle/save-state"
import { BRIDGE_MAX_INPUT_BYTES, BRIDGE_MAX_MESSAGE_BYTES, BRIDGE_MAX_OPS, createWorkbookBridge, createWorkbookHttpTransport, parseCommits, parseFrameMessage, parseWorkbookSession, type HostMessage, type KillReason, type TransportResponse, type WorkbookTransport } from "./bridge"

const SID = "ws_session1"
const ORIGIN = "https://editor.test"
const DIGEST = "sha256:bundle"
const session = { session_id: SID, actor_label: "Owner", rev: 41, generation: 1, protocol: 1, bundle_digest: DIGEST }

type Cells = [cell: string, input: string][]
const ops = (cells: Cells) => cells.map(([cell, input]) => ({ op: "cell.set", sheet: "Sheet1", cell, input }))
const mutation = (seq: number, base: number, cells: Cells) => ({ type: "mutation", protocol: 1, seq, base_rev: base, ops: ops(cells) })
const ok = (body: unknown): TransportResponse => ({ status: 200, body })
const receipt = (seq: number, rev: number, durability = "node") => ok({
  operation_key: { artifact_id: "page1", authority_id: "user:u1", mutation_id: `${SID}:${seq}` },
  outcome: "applied", rev, payload_hash: "sha256:x", issued_by: "host", validated_by: "gridcraft", effects: [], durability, at: "2026-10-22T10:00:00Z",
})
const remote = (rev: number, cells: Cells, actor = "Agent Ada") => ({ rev, kind: "cells", actor_label: actor, own: false, ops: cells.map(([cell, input]) => ({ sheet: "Sheet1", cell, input })) })
const own = (rev: number, seq: number, cells: Cells) => ({ rev, kind: "cells", actor_label: "Owner", own: true, session_id: SID, mutation_id: `${SID}:${seq}`, ops: cells.map(([cell, input]) => ({ sheet: "Sheet1", cell, input })) })
const feed = (head: number, commits: unknown[], generation = 1) => ok({ head_rev: head, generation, commits })

function deferred<T>() {
  let resolve!: (value: T) => void, reject!: (cause: unknown) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

const flush = () => vi.advanceTimersByTimeAsync(0)

beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { vi.useRealTimers() })

function setup(overrides: Partial<WorkbookTransport> = {}) {
  const frame = { postMessage: vi.fn() }
  const port = { postMessage: vi.fn(), close: vi.fn(), start: vi.fn(), onmessage: null as ((event: { data: unknown }) => void) | null }
  const base: WorkbookTransport = {
    snapshot: async () => ok({ schema_version: 1, rev: 41, generation: 1, snapshot: { sheets: [] } }),
    edit: async () => ({ status: 500, body: null }),
    commits: async since => feed(since, []),
    operation: async () => ({ status: 404, body: { error: "Workbook not found" } }),
    closeSession: async () => {},
    ...overrides,
  }
  const transport = { snapshot: vi.fn(base.snapshot), edit: vi.fn(base.edit), commits: vi.fn(base.commits), operation: vi.fn(base.operation), closeSession: vi.fn(base.closeSession) }
  const events: SaveEvent[] = []
  const kills: { reason: KillReason; unsaved: number }[] = []
  const bridge = createWorkbookBridge({
    editorOrigin: ORIGIN, session, frame: () => frame, transport, pageLabel: "Budget", clientId: "client-a",
    createChannel: () => ({ port1: port, port2: "port2" }), random: () => 1,
    onSaveEvent: event => events.push(event), onKill: kill => kills.push(kill),
  })
  const toFrame = (): HostMessage[] => port.postMessage.mock.calls.map(([data]) => JSON.parse(data as string))
  const types = () => toFrame().map(message => message.type === "committed" || message.type === "ack" ? `${message.type}:${message.rev}` : message.type)
  const hello = (data: unknown = { type: "gridcraft:hello", protocol: 1, bundle: DIGEST }, origin = ORIGIN, source: unknown = frame) => bridge.handleWindowMessage({ origin, source, data })
  const send = (message: unknown) => port.onmessage?.({ data: typeof message === "string" ? message : JSON.stringify(message) })
  const connect = async () => { hello(); await flush(); port.postMessage.mockClear() }
  const editBodies = () => transport.edit.mock.calls.map(([, body]) => body)
  return { bridge, frame, port, transport, events, kills, toFrame, types, hello, send, connect, editBodies }
}

describe("handshake", () => {
  it("connects only our frame on the editor origin with the expected bundle", async () => {
    const t = setup()
    t.hello(undefined, "https://evil.test")
    t.hello(undefined, ORIGIN, { postMessage: vi.fn() })
    t.hello({ type: "other" })
    expect(t.frame.postMessage).not.toHaveBeenCalled()
    expect(t.bridge.phase).toBe("waiting")

    t.hello(); await flush()
    expect(t.frame.postMessage).toHaveBeenCalledWith(
      { type: "gridcraft:connect", protocol: 1, session: "client-a", page_label: "Budget", rev: 41, generation: 1, client_id: "client-a" },
      ORIGIN, ["port2"],
    )
    // The session id never reaches the frame.
    expect(JSON.stringify(t.frame.postMessage.mock.calls)).not.toContain(SID)
    expect(t.toFrame()).toEqual([
      { type: "snapshot", protocol: 1, rev: 41, generation: 1, snapshot: { sheets: [] }, discarded: [] },
      { type: "online", protocol: 1 },
    ])
    expect(t.bridge.phase).toBe("live")
    expect(t.events).toContainEqual({ type: "opened", canWrite: true })
  })

  it.each([
    ["a wrong protocol", { type: "gridcraft:hello", protocol: 2, bundle: DIGEST }],
    ["an unknown bundle digest", { type: "gridcraft:hello", protocol: 1, bundle: "sha256:other" }],
    ["extra fields", { type: "gridcraft:hello", protocol: 1, bundle: DIGEST, token: "x" }],
  ])("kills the session on %s", async (_, data) => {
    const t = setup()
    t.hello(data)
    await flush()
    expect(t.kills).toEqual([{ reason: "handshake", unsaved: 0 }])
    expect(t.frame.postMessage).not.toHaveBeenCalled()
    expect(t.transport.snapshot).not.toHaveBeenCalled()
  })

  it("treats a second hello as a reloaded frame", async () => {
    const t = setup()
    await t.connect()
    t.hello()
    expect(t.kills).toEqual([{ reason: "handshake", unsaved: 0 }])
    expect(t.port.close).toHaveBeenCalled()
  })

  it("refuses a wildcard editor origin", () => {
    expect(() => createWorkbookBridge({ editorOrigin: "*", session, frame: () => null, transport: {} as WorkbookTransport, pageLabel: "", clientId: "" })).toThrow()
  })
})

describe("inbound validation", () => {
  it.each([
    ["non-JSON", "{"],
    ["a non-string message", { toString: () => "x" }],
    ["a wrong protocol", { ...mutation(1, 41, [["B5", "x"]]), protocol: 2 }],
    ["an unknown type", { type: "debug", protocol: 1 }],
    ["extra keys", { ...mutation(1, 41, [["B5", "x"]]), mutation_id: "forged" }],
    ["a base revision the editor never received", mutation(1, 42, [["B5", "x"]])],
    ["a message over the size limit", JSON.stringify({ ...mutation(1, 41, [["B5", "x".repeat(BRIDGE_MAX_MESSAGE_BYTES)]]) })],
  ])("kills the session on %s", async (_, message) => {
    const t = setup()
    await t.connect()
    if (typeof message === "object" && "toString" in message) t.port.onmessage?.({ data: message })
    else t.send(message)
    await flush()
    expect(t.kills).toEqual([{ reason: "protocol", unsaved: 0 }])
    expect(t.transport.edit).not.toHaveBeenCalled()
  })

  it.each([
    ["a bad cell address", [{ op: "cell.set", sheet: "Sheet1", cell: "b5", input: "x" }]],
    ["an unknown op", [{ op: "row.insert", sheet: "Sheet1", cell: "B5", input: "x" }]],
    ["a sheet index instead of a name", [{ op: "cell.set", sheet: 0, cell: "B5", input: "x" }]],
    ["an oversized input", [{ op: "cell.set", sheet: "Sheet1", cell: "B5", input: "é".repeat(BRIDGE_MAX_INPUT_BYTES / 2 + 1) }]],
    ["too many ops", Array.from({ length: BRIDGE_MAX_OPS + 1 }, (_, i) => ({ op: "cell.set", sheet: "Sheet1", cell: `A${i + 1}`, input: "1" }))],
    ["the same cell twice", [{ op: "cell.set", sheet: "Sheet1", cell: "B5", input: "1" }, { op: "cell.set", sheet: "Sheet1", cell: "B5", input: "2" }]],
    ["no ops", []],
  ])("refuses %s without calling the server", async (_, badOps) => {
    const t = setup()
    await t.connect()
    t.send({ type: "mutation", protocol: 1, seq: 1, base_rev: 41, ops: badOps })
    await flush()
    expect(t.toFrame()).toEqual([{ type: "rejected", protocol: 1, seq: 1, code: "invalid_ops", current_rev: 41, cells: [] }])
    expect(t.transport.edit).not.toHaveBeenCalled()
    expect(t.kills).toEqual([])
  })

  it("parses only well-formed messages", () => {
    expect(parseFrameMessage(JSON.stringify(mutation(3, 41, [["B5", "7"]])))).toEqual({ ok: true, value: { type: "mutation", mutation: { seq: 3, base_rev: 41, ops: ops([["B5", "7"]]) } } })
    expect(parseFrameMessage(JSON.stringify({ type: "resume", protocol: 1, last_rev: 41, pending: [{ seq: 1, base_rev: 41, ops: ops([["B5", "7"]]) }] })).ok).toBe(true)
    expect(parseFrameMessage(JSON.stringify({ type: "resume", protocol: 1, last_rev: 41, pending: [{ seq: 1, base_rev: 41, ops: [] }] })).ok).toBe(false)
    expect(parseWorkbookSession(session)).toEqual(session)
    expect(parseWorkbookSession({ ...session, protocol: 2 })).toBeNull()
    expect(parseCommits({ head_rev: 44, generation: 1, commits: [remote(43, [["B2", "1"]])] }, 41)).toBeNull() // gap inside the feed
  })
})

describe("sending and receipts", () => {
  it("sends the mutation with the host's session and acknowledges only a durable receipt", async () => {
    const t = setup({ edit: vi.fn(async () => receipt(1, 42)) })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush()
    expect(t.transport.edit).toHaveBeenCalledWith(`${SID}:1`, JSON.stringify({ session_id: SID, mutation_id: `${SID}:1`, base_rev: 41, generation: 1, ops: [{ kind: "cell.set", sheet: "Sheet1", cell: "B3", input: "21" }] }))
    expect(t.toFrame()).toEqual([{ type: "ack", protocol: 1, seq: 1, rev: 42 }])
    expect(t.events.slice(-2)).toEqual([{ type: "send" }, { type: "ack", pending: 0 }])
    expect(t.bridge.confirmedRev).toBe(42)
  })

  it("does not call a non-durable receipt saved", async () => {
    const t = setup({ edit: vi.fn(async () => receipt(1, 42, "memory")) })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush()
    expect(t.toFrame()).toEqual([])
    expect(t.events.some(event => event.type === "ack")).toBe(false)
    expect(t.events.at(-1)).toEqual({ type: "unconfirmed" })
    expect(t.bridge.pending).toBe(1)
  })

  it("does not accept a receipt for another mutation", async () => {
    const t = setup({ edit: vi.fn(async () => receipt(9, 42)) })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush()
    expect(t.toFrame()).toEqual([])
    expect(t.bridge.pending).toBe(1)
  })

  it("holds an ack until every earlier revision reached the editor (gap → commits fetch)", async () => {
    const commits = deferred<TransportResponse>()
    const t = setup({ edit: vi.fn(async () => receipt(1, 44)), commits: vi.fn(() => commits.promise) })
    await t.connect()
    t.send(mutation(1, 41, [["B5", "draft"]]))
    await flush()
    expect(t.transport.commits).toHaveBeenCalledWith(41, 200)
    expect(t.toFrame()).toEqual([]) // no ack before 42 and 43
    expect(t.events.at(-1)).toEqual({ type: "ack", pending: 0 }) // the receipt is durable: saved
    commits.resolve(feed(44, [remote(42, [["B2", "99"]]), remote(43, [["C1", "x"]], "Owner (window B)"), own(44, 1, [["B5", "draft"]])]))
    await flush()
    expect(t.types()).toEqual(["committed:42", "committed:43", "ack:44"])
    expect(t.toFrame()[0]).toEqual({ type: "committed", protocol: 1, rev: 42, actor_label: "Agent Ada", ops: ops([["B2", "99"]]) })
    expect(t.bridge.confirmedRev).toBe(44)
  })

  it("acks a held receipt even when the feed omits the mutation id", async () => {
    const t = setup({ edit: vi.fn(async () => receipt(1, 43)), commits: vi.fn(async () => feed(43, [remote(42, [["B2", "99"]]), { rev: 43, kind: "cells", actor_label: "Owner", own: true, ops: [{ sheet: "Sheet1", cell: "B5", input: "x" }] }])) })
    await t.connect()
    t.send(mutation(1, 41, [["B5", "x"]]))
    await flush()
    expect(t.types()).toEqual(["committed:42", "ack:43"])
  })
})

describe("push and ordering", () => {
  it("fetches commits on a push and delivers them in order", async () => {
    const t = setup({ commits: vi.fn(async () => feed(43, [remote(42, [["B2", "99"]]), remote(43, [["B3", "1"]])])) })
    await t.connect()
    t.bridge.notify({ rev: 43, generation: 1 })
    await flush()
    expect(t.types()).toEqual(["committed:42", "committed:43"])
  })

  it("treats a duplicate or stale push as a no-op", async () => {
    const t = setup({ commits: vi.fn(async () => feed(42, [remote(42, [["B2", "99"]])])) })
    await t.connect()
    t.bridge.notify({ rev: 42, generation: 1 })
    t.bridge.notify({ rev: 42, generation: 1 }) // while the first fetch is running
    await flush()
    t.bridge.notify({ rev: 42, generation: 1 }) // after it
    t.bridge.notify({ rev: 40, generation: 1 })
    await flush()
    expect(t.transport.commits).toHaveBeenCalledTimes(1)
    expect(t.types()).toEqual(["committed:42"])
  })

  it("pages through the feed until the head", async () => {
    const t = setup({ commits: vi.fn(async (since: number) => since === 41 ? feed(43, [remote(42, [["A1", "1"]])]) : feed(43, [remote(43, [["A2", "2"]])])) })
    await t.connect()
    t.bridge.notify({ rev: 43, generation: 1 })
    await flush()
    expect(t.transport.commits.mock.calls.map(([since]) => since)).toEqual([41, 42])
    expect(t.types()).toEqual(["committed:42", "committed:43"])
  })

  it("retries a failed fetch with backoff and resumes from the last revision on reconnect", async () => {
    let calls = 0
    const t = setup({ commits: vi.fn(async (since: number) => { calls++; if (calls === 1) throw new Error("offline"); return since === 41 ? feed(42, [remote(42, [["B2", "9"]])]) : feed(since, []) }) })
    await t.connect()
    t.bridge.notify({ rev: 42, generation: 1 })
    await flush()
    expect(t.types()).toEqual([])
    await vi.advanceTimersByTimeAsync(250)
    expect(t.types()).toEqual(["committed:42"])
    t.bridge.offline()
    t.bridge.online()
    await flush()
    expect(t.transport.commits).toHaveBeenLastCalledWith(42, 200)
    expect(t.types()).toEqual(["committed:42", "offline", "online"])
  })

  it("kills the session when a push announces a new generation", async () => {
    const t = setup()
    await t.connect()
    t.bridge.notify({ rev: 50, generation: 2 })
    expect(t.kills).toEqual([{ reason: "generation", unsaved: 0 }])
    expect(t.transport.commits).not.toHaveBeenCalled()
  })

  it("reloads the snapshot after an import and discards edits made against the old content", async () => {
    const edit = deferred<TransportResponse>()
    const t = setup({
      edit: vi.fn(() => edit.promise),
      commits: vi.fn(async () => feed(43, [remote(42, [["B2", "1"]]), { rev: 43, kind: "snapshot" }])),
    })
    await t.connect()
    t.transport.snapshot.mockResolvedValueOnce(ok({ schema_version: 1, rev: 43, generation: 1, snapshot: { sheets: ["imported"] } }))
    t.send(mutation(1, 41, [["B5", "a"]]))
    t.send(mutation(2, 41, [["B6", "b"]]))
    await flush()
    t.bridge.notify({ rev: 43, generation: 1 })
    await flush()
    expect(t.toFrame()).toEqual([
      { type: "committed", protocol: 1, rev: 42, actor_label: "Agent Ada", ops: ops([["B2", "1"]]) },
      { type: "snapshot", protocol: 1, rev: 43, generation: 1, snapshot: { sheets: ["imported"] }, discarded: [1, 2] },
    ])
    // The write in flight was refused by the server (import inside its window): nothing more reaches the editor.
    edit.resolve({ status: 409, body: { error: "precondition_failed", head_rev: 43, conflicts: [] } })
    await vi.advanceTimersByTimeAsync(1000)
    expect(t.toFrame()).toHaveLength(2)
    expect(t.bridge.pending).toBe(0)
    expect(t.transport.edit).toHaveBeenCalledTimes(1)
  })

  it("reloads the snapshot when the feed answers 410", async () => {
    const t = setup({ commits: vi.fn(async (): Promise<TransportResponse> => ({ status: 410, body: { error: "snapshot_required" } })) })
    await t.connect()
    t.transport.snapshot.mockResolvedValueOnce(ok({ schema_version: 1, rev: 900, generation: 1, snapshot: {} }))
    t.bridge.notify({ rev: 900, generation: 1 })
    await flush()
    expect(t.toFrame()).toEqual([{ type: "snapshot", protocol: 1, rev: 900, generation: 1, snapshot: {}, discarded: [] }])
    expect(t.bridge.confirmedRev).toBe(900)
  })
})

describe("conflicts and the busy artifact", () => {
  it("409: reconciles first, refuses with the server's cells, then sends the next mutation", async () => {
    const conflict: TransportResponse = { status: 409, body: { error: "precondition_failed", head_rev: 42, conflicts: [{ cell: "B2", input: "7", rev: 42, actor_label: "Agent Ada" }] } }
    const t = setup({
      edit: vi.fn(async (id: string) => id.endsWith(":1") ? conflict : receipt(2, 43)),
      commits: vi.fn(async () => feed(42, [remote(42, [["B2", "7"]])])),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B2", "mine"]]))
    t.send(mutation(2, 41, [["C3", "independent"]]))
    await flush()
    expect(t.toFrame()).toEqual([
      { type: "committed", protocol: 1, rev: 42, actor_label: "Agent Ada", ops: ops([["B2", "7"]]) },
      { type: "rejected", protocol: 1, seq: 1, code: "precondition_failed", current_rev: 42, cells: [{ cell: "B2", input: "7", rev: 42, actor_label: "Agent Ada" }] },
      { type: "ack", protocol: 1, seq: 2, rev: 43 },
    ])
    expect(t.events).toContainEqual({ type: "conflict" })
    expect(t.bridge.pending).toBe(0)
  })

  it("429 on the artifact retries the same mutation id with the same bytes", async () => {
    let calls = 0
    const t = setup({ edit: vi.fn(async (): Promise<TransportResponse> => ++calls < 3 ? { status: 429, body: { error: "capacity_exhausted", scope: "artifact", retry_after_ms: 150 } } : receipt(1, 42)) })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush()
    expect(t.transport.edit).toHaveBeenCalledTimes(1)
    expect(t.events.at(-1)).toEqual({ type: "rate-limited", retryAfterMs: 150 })
    await vi.advanceTimersByTimeAsync(149)
    expect(t.transport.edit).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await vi.advanceTimersByTimeAsync(150)
    expect(t.transport.edit).toHaveBeenCalledTimes(3)
    const bodies = t.editBodies()
    expect(new Set(bodies).size).toBe(1)
    expect(t.transport.edit.mock.calls.every(([id]) => id === `${SID}:1`)).toBe(true)
    expect(t.toFrame()).toEqual([{ type: "ack", protocol: 1, seq: 1, rev: 42 }])
  })

  it("429 global keeps the write and backs off", async () => {
    const t = setup({ edit: vi.fn(async (): Promise<TransportResponse> => ({ status: 429, body: { error: "capacity_exhausted", scope: "global" } })) })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush()
    expect(t.events.at(-1)).toMatchObject({ type: "transient-fail" })
    expect(t.bridge.pending).toBe(1)
    expect(t.toFrame()).toEqual([])
  })

  it("429 history quota refuses the write for good", async () => {
    const t = setup({ edit: vi.fn(async (): Promise<TransportResponse> => ({ status: 429, body: { error: "history_quota_exceeded" } })) })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush()
    expect(t.toFrame()).toEqual([{ type: "rejected", protocol: 1, seq: 1, code: "history_quota_exceeded", current_rev: 41, cells: [] }])
    expect(t.bridge.pending).toBe(0)
    expect(t.transport.edit).toHaveBeenCalledTimes(1)
  })

  it.each([401, 403, 404])("%i kills the session and the port", async status => {
    const t = setup({ edit: vi.fn(async (): Promise<TransportResponse> => ({ status, body: { error: "Workbook not found" } })) })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    t.send(mutation(2, 41, [["B4", "22"]]))
    await flush()
    expect(t.kills).toEqual([{ reason: "access", unsaved: 2 }])
    expect(t.port.close).toHaveBeenCalled()
    expect(t.port.onmessage).toBeNull()
    expect(t.events.at(-1)).toEqual({ type: "denied" })
    expect(t.bridge.phase).toBe("killed")
    // Nothing reaches the frame or the server afterwards.
    t.bridge.notify({ rev: 50, generation: 1 })
    await vi.advanceTimersByTimeAsync(60_000)
    expect(t.toFrame()).toEqual([])
    expect(t.transport.edit).toHaveBeenCalledTimes(1)
    expect(t.transport.commits).not.toHaveBeenCalled()
    expect(t.transport.closeSession).not.toHaveBeenCalled()
  })

  it("a 401 from the commits feed kills the session too", async () => {
    const t = setup({ commits: vi.fn(async (): Promise<TransportResponse> => ({ status: 401, body: null })) })
    await t.connect()
    t.bridge.notify({ rev: 42, generation: 1 })
    await flush()
    expect(t.kills).toEqual([{ reason: "access", unsaved: 0 }])
  })

  it("close() ends the session on the server", async () => {
    const t = setup()
    await t.connect()
    t.bridge.close()
    expect(t.kills).toEqual([{ reason: "closed", unsaved: 0 }])
    expect(t.transport.closeSession).toHaveBeenCalledTimes(1)
  })
})

describe("a remote commit while a local edit is pending", () => {
  async function pendingB5(edit: WorkbookTransport["edit"], commits: WorkbookTransport["commits"]) {
    const t = setup({ edit: vi.fn(edit), commits: vi.fn(commits) })
    await t.connect()
    t.send(mutation(1, 41, [["B5", "draft"]]))
    await flush()
    return t
  }

  it("preserves the local pending edit and acks it after the remote commit", async () => {
    const edit = deferred<TransportResponse>()
    const t = await pendingB5(() => edit.promise, async () => feed(42, [remote(42, [["B2", "99"]])]))
    t.bridge.notify({ rev: 42, generation: 1 })
    await flush()
    expect(t.toFrame()).toEqual([{ type: "committed", protocol: 1, rev: 42, actor_label: "Agent Ada", ops: ops([["B2", "99"]]) }])
    expect(t.bridge.pending).toBe(1) // not dropped, not rejected
    edit.resolve(receipt(1, 43))
    await flush()
    expect(t.types()).toEqual(["committed:42", "ack:43"])
  })

  it("rebases: the head moved without touching the cell, so the same mutation is sent again", async () => {
    let calls = 0
    const t = await pendingB5(
      async () => ++calls === 1 ? { status: 409, body: { error: "precondition_failed", head_rev: 42, conflicts: [] } } : receipt(1, 43),
      async () => feed(42, [remote(42, [["B2", "99"]])]),
    )
    await vi.advanceTimersByTimeAsync(0)
    expect(t.types()).toEqual(["committed:42", "ack:43"])
    expect(t.editBodies()).toHaveLength(2)
    expect(t.editBodies()[0]).toBe(t.editBodies()[1])
  })

  it("reports: the remote commit wrote the same cell, so the editor learns the server's value", async () => {
    const t = await pendingB5(
      async () => ({ status: 409, body: { error: "precondition_failed", head_rev: 42, conflicts: [{ cell: "B5", input: "agent", rev: 42, actor_label: "Agent Ada" }] } }),
      async () => feed(42, [remote(42, [["B5", "agent"]])]),
    )
    expect(t.toFrame()).toEqual([
      { type: "committed", protocol: 1, rev: 42, actor_label: "Agent Ada", ops: ops([["B5", "agent"]]) },
      { type: "rejected", protocol: 1, seq: 1, code: "precondition_failed", current_rev: 42, cells: [{ cell: "B5", input: "agent", rev: 42, actor_label: "Agent Ada" }] },
    ])
    expect(t.bridge.pending).toBe(0)
  })
})

describe("idempotence and resume", () => {
  it("ignores a restated pending mutation and replays the answer to an acknowledged one", async () => {
    const edit = deferred<TransportResponse>()
    const t = setup({ edit: vi.fn(async (id: string) => id.endsWith(":1") ? receipt(1, 42) : edit.promise) })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush()
    t.send(mutation(2, 42, [["B4", "x"]]))
    await flush()
    t.port.postMessage.mockClear()
    t.send({ type: "resume", protocol: 1, last_rev: 42, pending: [{ seq: 1, base_rev: 41, ops: ops([["B3", "21"]]) }, { seq: 2, base_rev: 42, ops: ops([["B4", "x"]]) }] })
    await flush()
    expect(t.transport.edit).toHaveBeenCalledTimes(2)
    expect(t.toFrame()).toEqual([{ type: "ack", protocol: 1, seq: 1, rev: 42 }])
  })

  it("kills on a restatement that changes the mutation", async () => {
    const t = setup({ edit: vi.fn(() => new Promise<TransportResponse>(() => {})) })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    t.send(mutation(1, 41, [["B3", "22"]]))
    expect(t.kills).toEqual([{ reason: "protocol", unsaved: 1 }])
  })

  it("kills on a sequence number that goes backwards", async () => {
    const t = setup({ edit: vi.fn(() => new Promise<TransportResponse>(() => {})) })
    await t.connect()
    t.send(mutation(5, 41, [["B3", "21"]]))
    t.send(mutation(4, 41, [["B4", "21"]]))
    expect(t.kills).toEqual([{ reason: "protocol", unsaved: 1 }])
  })

  it("asks for the status after a lost answer and takes the original receipt instead of resending", async () => {
    const t = setup({
      edit: vi.fn(async () => { throw new TypeError("network") }),
      operation: vi.fn(async () => ok({ state: "applied", receipt: receipt(1, 42).body, head_rev: 42, effect_in_head: "present", delivery: "sent" })),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush()
    expect(t.events.at(-1)).toEqual({ type: "offline" })
    t.bridge.online()
    await flush()
    expect(t.transport.operation).toHaveBeenCalledWith(`${SID}:1`)
    expect(t.transport.edit).toHaveBeenCalledTimes(1)
    expect(t.toFrame()).toEqual([{ type: "online", protocol: 1 }, { type: "ack", protocol: 1, seq: 1, rev: 42 }])
  })

  it("resends the same bytes when the status shows the mutation never arrived", async () => {
    let calls = 0
    const t = setup({ edit: vi.fn(async () => { if (++calls === 1) throw new TypeError("network"); return receipt(1, 42) }) })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush()
    t.bridge.online()
    await flush()
    expect(t.transport.operation).toHaveBeenCalledTimes(1)
    expect(t.editBodies()).toHaveLength(2)
    expect(t.editBodies()[0]).toBe(t.editBodies()[1])
    expect(t.types()).toEqual(["online", "ack:42"])
  })

  it("acks from the feed when the receipt was lost, and only once", async () => {
    let calls = 0
    const t = setup({
      edit: vi.fn(async () => { if (++calls === 1) throw new TypeError("network"); return receipt(1, 42) }),
      commits: vi.fn(async (since: number) => since === 41 ? feed(42, [own(42, 1, [["B3", "21"]])]) : feed(since, [])),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush()
    t.bridge.notify({ rev: 42, generation: 1 })
    await flush()
    expect(t.types()).toEqual(["ack:42"])
    await vi.advanceTimersByTimeAsync(1000) // the queued retry returns the original receipt
    expect(t.types()).toEqual(["ack:42"])
    expect(t.bridge.pending).toBe(0)
  })

  it("replays from the editor's last revision when it is behind", async () => {
    const t = setup({ commits: vi.fn(async (since: number) => since === 41 ? feed(42, [remote(42, [["B2", "99"]])]) : feed(since, [])) })
    await t.connect()
    t.bridge.notify({ rev: 42, generation: 1 })
    await flush()
    t.send({ type: "resume", protocol: 1, last_rev: 41, pending: [] })
    await flush()
    expect(t.types()).toEqual(["committed:42", "committed:42"])
  })

  it("kills when the editor claims a revision it was never sent", async () => {
    const t = setup()
    await t.connect()
    t.send({ type: "resume", protocol: 1, last_rev: 99, pending: [] })
    expect(t.kills).toEqual([{ reason: "protocol", unsaved: 0 }])
  })
})

describe("HTTP transport", () => {
  it("calls the workbook API of one page without caching", async () => {
    const fetch = vi.fn(async () => new Response(JSON.stringify({ ok: true }), { status: 200 }))
    const transport = createWorkbookHttpTransport({ workspaceId: "w1", pageId: "p1", sessionId: "ws_1", fetch })
    await transport.edit("ws_1:1", "{\"a\":1}")
    await transport.commits(41, 200)
    await transport.operation("ws_1:1")
    await transport.snapshot()
    await transport.closeSession()
    expect(fetch.mock.calls.map(call => { const [url, init] = call as unknown as [string, RequestInit]; return [init.method ?? "GET", url, init.cache] })).toEqual([
      ["POST", "/api/v1/pages/%40p1/workbook/edits?workspace_id=w1", "no-store"],
      ["GET", "/api/v1/pages/%40p1/workbook/commits?workspace_id=w1&since=41&limit=200", "no-store"],
      ["GET", "/api/v1/pages/%40p1/workbook/operations/ws_1%3A1?workspace_id=w1", "no-store"],
      ["GET", "/api/v1/pages/%40p1/workbook?workspace_id=w1", "no-store"],
      ["DELETE", "/api/v1/pages/%40p1/workbook/sessions/ws_1?workspace_id=w1", "no-store"],
    ])
    expect((fetch.mock.calls[0] as unknown as [string, RequestInit])[1].body).toBe("{\"a\":1}")
  })
})
