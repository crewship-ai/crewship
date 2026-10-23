import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { initialSaveState, saveReducer, saveWordOf, type SaveEvent } from "@/lib/editor-lifecycle/save-state"
import { BRIDGE_HANDSHAKE_TIMEOUT_MS, BRIDGE_MAX_REFUSALS, BRIDGE_MAX_REBASES, BRIDGE_MAX_INPUT_BYTES, BRIDGE_MAX_UNCONFIRMED, EDITOR_FRAME_ATTRIBUTES, BRIDGE_MAX_MESSAGE_BYTES, BRIDGE_MAX_OPS, createWorkbookBridge, createWorkbookHttpTransport, parseCommits, parseFrameMessage, parseWorkbookSession, type DraftConflict, type HostMessage, type Refusal, type KillReason, type TransportResponse, type WorkbookBridgeOptions, type WorkbookSession, type WorkbookTransport } from "./bridge"

const SID = "ws_session1"
/** The editor document is served with CSP `sandbox allow-scripts`: its origin is opaque. */
const ORIGIN = "null"
/** A bundle pin: the plain lowercase hex SHA-256 of the bundle's manifest bytes. */
const DIGEST = "d".repeat(64)
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

/** `loaded`: the frame's first load event already happened, as it does before a real hello. */
function setup(overrides: Partial<WorkbookTransport> = {}, { loaded = true, reopen, sessionOverride, onReload }: { loaded?: boolean; reopen?: WorkbookBridgeOptions["reopenSession"]; sessionOverride?: Partial<WorkbookSession>; onReload?: () => void } = {}) {
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
  const conflicts: DraftConflict[] = []
  let refusals: Refusal[] = []
  const bridge = createWorkbookBridge({
    session: { ...session, ...sessionOverride }, frame: () => frame, transport, pageLabel: "Budget", clientId: "client-a", reopenSession: reopen,
    createChannel: () => ({ port1: port, port2: "port2" }), random: () => 1,
    onSaveEvent: event => events.push(event), onKill: kill => kills.push(kill), onConflict: conflict => conflicts.push(conflict), onReload,
    onRefusals: open => { refusals = open },
  })
  if (loaded) bridge.frameLoaded()
  const toFrame = (): HostMessage[] => port.postMessage.mock.calls.map(([data]) => JSON.parse(data as string))
  const types = () => toFrame().map(message => message.type === "committed" || message.type === "ack" ? `${message.type}:${message.rev}` : message.type)
  const hello = (data: unknown = { type: "gridcraft:hello", protocol: 1, bundle: DIGEST }, origin = ORIGIN, source: unknown = frame) => bridge.handleWindowMessage({ origin, source, data })
  const send = (message: unknown) => port.onmessage?.({ data: typeof message === "string" ? message : JSON.stringify(message) })
  const connect = async () => { hello(); await flush(); port.postMessage.mockClear() }
  const editBodies = () => transport.edit.mock.calls.map(([, body]) => body)
  /** The save state the host would show after every event so far. */
  const state = () => events.reduce(saveReducer, initialSaveState)
  return { bridge, frame, port, transport, events, kills, conflicts, refusals: () => refusals, state, toFrame, types, hello, send, connect, editBodies }
}

describe("handshake", () => {
  it("connects only our sandboxed frame with the expected bundle", async () => {
    const t = setup()
    t.hello({ type: "other" })
    expect(t.frame.postMessage).not.toHaveBeenCalled()
    expect(t.bridge.phase).toBe("waiting")

    t.hello(); await flush()
    // An opaque origin cannot be named as a target: "*" goes to the verified window object only.
    expect(t.frame.postMessage).toHaveBeenCalledTimes(1)
    expect(t.frame.postMessage).toHaveBeenCalledWith(
      { type: "gridcraft:connect", protocol: 1, session: "client-a", page_label: "Budget", rev: 41, generation: 1, client_id: "client-a" },
      "*", ["port2"],
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

  it("ignores a null-origin hello from another window", async () => {
    const t = setup()
    const other = { postMessage: vi.fn() }
    t.hello(undefined, "null", other)
    t.hello(undefined, "null", null)
    await flush()
    expect(t.frame.postMessage).not.toHaveBeenCalled()
    expect(other.postMessage).not.toHaveBeenCalled()
    expect(t.bridge.phase).toBe("waiting")
    expect(t.kills).toEqual([])
  })

  it("ignores an unexpected non-null origin from another window", async () => {
    const t = setup()
    t.hello(undefined, "https://evil.test", { postMessage: vi.fn() })
    await flush()
    expect(t.frame.postMessage).not.toHaveBeenCalled()
    expect(t.bridge.phase).toBe("waiting")
  })

  it("kills the session when our frame speaks from a real origin (it is not sandboxed)", async () => {
    const t = setup()
    t.hello(undefined, "https://editor.test")
    t.hello(undefined, "https://crewship.test")
    await flush()
    expect(t.frame.postMessage).not.toHaveBeenCalled()
    expect(t.kills).toEqual([{ reason: "handshake", unsaved: 0 }])
  })

  it.each([
      ["a wrong protocol", { type: "gridcraft:hello", protocol: 2, bundle: DIGEST }],
    ["a missing bundle field", { type: "gridcraft:hello", protocol: 1 }],
    ["extra fields", { type: "gridcraft:hello", protocol: 1, bundle: DIGEST, token: "x" }],
  ])("kills the session on %s", async (_, data) => {
    const t = setup()
    t.hello(data)
    await flush()
    expect(t.kills).toEqual([{ reason: "handshake", unsaved: 0 }])
    expect(t.frame.postMessage).not.toHaveBeenCalled()
    expect(t.transport.snapshot).not.toHaveBeenCalled()
  })

  // A well-formed hello that names another bundle than the session's pin is a bundle mismatch,
  // not a broken protocol, so the host can say which: the operator has to look at the bundle pin.
  it.each([
    ["a wrong bundle digest", "e".repeat(64)],
    ["no bundle digest (null)", null],
    ["the right digest with a prefix", `sha256:${DIGEST}`],
    ["the right digest in upper case", DIGEST.toUpperCase()],
  ])("kills the session as a bundle mismatch on %s", async (_, bundle) => {
    const t = setup()
    t.hello({ type: "gridcraft:hello", protocol: 1, bundle })
    await flush()
    expect(t.kills).toEqual([{ reason: "bundle", unsaved: 0 }])
    expect(t.frame.postMessage).not.toHaveBeenCalled()
    expect(t.transport.snapshot).not.toHaveBeenCalled()
  })

  it("rejects a second hello: connect is sent once", async () => {
    const t = setup()
    await t.connect()
    t.hello()
    await flush()
    expect(t.frame.postMessage).toHaveBeenCalledTimes(1)
    expect(t.kills).toEqual([{ reason: "handshake", unsaved: 0 }])
    expect(t.port.close).toHaveBeenCalled()
  })

  it("rejects any window message from the frame after connect: the port is the only channel", async () => {
    const t = setup()
    await t.connect()
    t.bridge.handleWindowMessage({ origin: "null", source: t.frame, data: JSON.stringify(mutation(1, 41, [["B3", "1"]])) })
    expect(t.kills).toEqual([{ reason: "handshake", unsaved: 0 }])
    expect(t.transport.edit).not.toHaveBeenCalled()
  })

  it("ignores window messages from other windows after connect", async () => {
    const t = setup()
    await t.connect()
    t.bridge.handleWindowMessage({ origin: "null", source: { postMessage: vi.fn() }, data: { type: "gridcraft:hello", protocol: 1, bundle: DIGEST } })
    t.bridge.handleWindowMessage({ origin: "https://evil.test", source: null, data: "x" })
    expect(t.kills).toEqual([])
    expect(t.bridge.phase).toBe("live")
  })

  it("describes the frame the host must create, exactly", () => {
    // Exactly these attributes: a new sandbox token (above all allow-same-origin) must fail here.
    expect(EDITOR_FRAME_ATTRIBUTES).toStrictEqual({ sandbox: "allow-scripts", credentialless: true, referrerPolicy: "no-referrer" })
    expect(EDITOR_FRAME_ATTRIBUTES.sandbox.split(/\s+/)).toStrictEqual(["allow-scripts"])
    expect(Object.isFrozen(EDITOR_FRAME_ATTRIBUTES)).toBe(true)
  })

  it("ignores the frame's first load, before its hello", async () => {
    const t = setup({}, { loaded: false })
    t.bridge.frameLoaded()
    t.hello(); await flush()
    expect(t.bridge.phase).toBe("live")
    expect(t.kills).toEqual([])
  })

  it("refuses a hello before the frame's first load: the port goes only to a document seen loading", async () => {
    const t = setup({}, { loaded: false })
    t.hello(); await flush()
    expect(t.kills).toEqual([{ reason: "handshake", unsaved: 0 }])
    expect(t.frame.postMessage).not.toHaveBeenCalled()
    t.bridge.frameLoaded()
    t.hello(); await flush()
    expect(t.frame.postMessage).not.toHaveBeenCalled()
    expect(t.kills).toHaveLength(1)
  })

  it("discards the session on a second load before the hello: a navigated document never gets the port", async () => {
    const t = setup({}, { loaded: false })
    t.bridge.frameLoaded()
    t.bridge.frameLoaded() // the first document navigated before saying hello
    expect(t.kills).toEqual([{ reason: "navigation", unsaved: 0 }])
    expect(t.bridge.phase).toBe("killed")
    t.hello(); await flush()
    expect(t.frame.postMessage).not.toHaveBeenCalled()
    expect(t.transport.snapshot).not.toHaveBeenCalled()
    expect(t.transport.closeSession).toHaveBeenCalledTimes(1)
  })

  it("discards the session when the frame loads a document after connect, and never resends the port", async () => {
    const t = setup({ edit: vi.fn(() => new Promise<TransportResponse>(() => {})) })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "1"]]))
    t.bridge.frameLoaded() // the frame navigated or reloaded
    expect(t.kills).toEqual([{ reason: "navigation", unsaved: 1 }])
    expect(t.port.close).toHaveBeenCalled()
    expect(t.bridge.phase).toBe("killed")
    // The new document says hello: it gets nothing; a new frame and handshake are required.
    t.hello(); await flush()
    t.bridge.frameLoaded()
    expect(t.frame.postMessage).toHaveBeenCalledTimes(1)
    expect(t.kills).toHaveLength(1)
  })

  it("discards the session when the frame loads while connect is still in progress", async () => {
    const snapshot = deferred<TransportResponse>()
    const t = setup({ snapshot: () => snapshot.promise })
    t.hello()
    t.bridge.frameLoaded()
    snapshot.resolve(ok({ schema_version: 1, rev: 41, generation: 1, snapshot: {} }))
    await flush()
    expect(t.kills).toEqual([{ reason: "navigation", unsaved: 0 }])
    expect(t.port.postMessage).not.toHaveBeenCalled()
  })
})

describe("connect failures and the handshake deadline", () => {
  it.each([
    ["a server error", async (): Promise<TransportResponse> => ({ status: 500, body: { error: "boom" } })],
    ["a network failure", async (): Promise<TransportResponse> => { throw new TypeError("network") }],
    ["an unreadable snapshot", async (): Promise<TransportResponse> => ok(null)],
  ])("ends the session as a load failure when the snapshot read fails at connect (%s)", async (_, snapshot) => {
    const t = setup({ snapshot: vi.fn(snapshot) })
    t.hello(); await flush()
    // Not "closed": that is an unmount, which the host never shows. This one must reach the UI.
    expect(t.kills).toEqual([{ reason: "load", unsaved: 0 }])
    expect(t.transport.closeSession).toHaveBeenCalledTimes(1)
    expect(t.port.close).toHaveBeenCalled()
  })

  it("ends the session when the editor turned off before the snapshot read", async () => {
    const t = setup({ snapshot: vi.fn(async (): Promise<TransportResponse> => ({ status: 503, body: { error: "gridcraft_editor_disabled" } })) })
    t.hello(); await flush()
    expect(t.kills).toEqual([{ reason: "disabled", unsaved: 0 }])
  })

  it("ends the session when the editor never says hello", async () => {
    const t = setup()
    await vi.advanceTimersByTimeAsync(BRIDGE_HANDSHAKE_TIMEOUT_MS - 1)
    expect(t.kills).toEqual([])
    await vi.advanceTimersByTimeAsync(1)
    expect(t.kills).toEqual([{ reason: "timeout", unsaved: 0 }])
    expect(t.transport.closeSession).toHaveBeenCalledTimes(1)
    // A hello after the deadline is not heard.
    t.hello(); await flush()
    expect(t.frame.postMessage).not.toHaveBeenCalled()
  })

  it("ends the session when the snapshot read after the hello never answers", async () => {
    const t = setup({ snapshot: vi.fn(() => new Promise<TransportResponse>(() => {})) })
    t.hello(); await flush()
    await vi.advanceTimersByTimeAsync(BRIDGE_HANDSHAKE_TIMEOUT_MS)
    expect(t.kills).toEqual([{ reason: "timeout", unsaved: 0 }])
  })

  it("has no deadline once the editor is open", async () => {
    const t = setup()
    await t.connect()
    await vi.advanceTimersByTimeAsync(BRIDGE_HANDSHAKE_TIMEOUT_MS * 4)
    expect(t.kills).toEqual([])
    expect(t.bridge.phase).toBe("live")
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
    expect(t.events.at(-1)).toMatchObject({ type: "transient-fail" })
    expect(t.bridge.pending).toBe(1)
  })

  it.each([
    ["a non-durable receipt", receipt(1, 42, "memory")],
    ["an unreadable body", ok(null)],
    ["another shape", ok({ status: "ok" })],
  ])("retries %s with the same mutation, asks for its status, then refuses it and sends later edits", async (_, answer) => {
    const t = setup({ edit: vi.fn(async (id: string) => id === `${SID}:1` ? answer : receipt(2, 42)) })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    t.send(mutation(2, 41, [["B4", "22"]]))
    await vi.advanceTimersByTimeAsync(60_000)
    const first = t.transport.edit.mock.calls.filter(([id]) => id === `${SID}:1`)
    expect(first).toHaveLength(BRIDGE_MAX_UNCONFIRMED)
    expect(new Set(first.map(([, body]) => body)).size).toBe(1)
    // Each retry first asks whether the earlier attempt committed after all.
    expect(t.transport.operation).toHaveBeenCalledTimes(BRIDGE_MAX_UNCONFIRMED - 1)
    expect(t.toFrame()).toEqual([
      { type: "rejected", protocol: 1, seq: 1, code: "not_confirmed", current_rev: 41, cells: [] },
      { type: "ack", protocol: 1, seq: 2, rev: 42 },
    ])
    expect(t.events).toContainEqual({ type: "unconfirmed" })
    expect(t.events.at(-1)).toEqual({ type: "ack", pending: 0 })
    expect(t.bridge.pending).toBe(0)
  })

  it("settles a pending edit the feed shows committed, without waiting for its receipt", async () => {
    const t = setup({
      edit: vi.fn(async () => receipt(1, 42, "memory")),
      commits: vi.fn(async (since: number) => since === 41 ? feed(42, [own(42, 1, [["B3", "21"]])]) : feed(since, [])),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush()
    expect(t.bridge.pending).toBe(1)
    t.bridge.notify({ rev: 42, generation: 1 })
    await flush()
    expect(t.types()).toEqual(["ack:42"])
    expect(t.bridge.pending).toBe(0)
    expect(t.events.at(-1)).toEqual({ type: "ack", pending: 0 })
    await vi.advanceTimersByTimeAsync(60_000)
    expect(t.transport.edit).toHaveBeenCalledTimes(1)
    expect(t.types()).toEqual(["ack:42"])
  })

  it("delivers an edit it refused as unconfirmed when the feed later shows it committed", async () => {
    let head = 41
    const t = setup({
      edit: vi.fn(async () => ok(null)),
      commits: vi.fn(async (since: number) => since === 41 && head === 42 ? feed(42, [own(42, 1, [["B3", "21"]])]) : feed(since, [])),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await vi.advanceTimersByTimeAsync(60_000)
    expect(t.types()).toEqual(["rejected"])
    head = 42
    t.bridge.notify({ rev: 42, generation: 1 })
    await flush()
    // The editor rolled the cell back on the refusal; it learns the committed value as a commit.
    expect(t.toFrame().at(-1)).toEqual({ type: "committed", protocol: 1, rev: 42, actor_label: "Owner", ops: [{ op: "cell.set", sheet: "Sheet1", cell: "B3", input: "21" }] })
    expect(t.bridge.confirmedRev).toBe(42)
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

  it.each([
    ["edit", "disabled", 503, { error: "gridcraft_editor_disabled" }],
    ["edit", "expired", 401, { error: "session_expired" }],
    ["commits", "disabled", 503, { error: "gridcraft_editor_disabled" }],
  ] as const)("a refused %s (%s) ends the session for good instead of retrying", async (call, reason, status, body) => {
    const refused = vi.fn(async (): Promise<TransportResponse> => ({ status, body }))
    const t = setup(call === "edit" ? { edit: refused } : { commits: refused })
    await t.connect()
    if (call === "edit") t.send(mutation(1, 41, [["B3", "21"]]))
    else t.bridge.notify({ rev: 42, generation: 1 })
    await flush()
    expect(t.kills).toEqual([{ reason, unsaved: call === "edit" ? 1 : 0 }])
    expect(t.port.close).toHaveBeenCalled()
    expect(t.transport.closeSession).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(refused).toHaveBeenCalledTimes(1)
  })

  it("keeps retrying a 503 that does not say the editor is off", async () => {
    const t = setup({ edit: vi.fn(async (): Promise<TransportResponse> => ({ status: 503, body: { error: "Workbook unavailable" } })) })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await vi.advanceTimersByTimeAsync(5_000)
    expect(t.kills).toEqual([])
    expect(t.transport.edit.mock.calls.length).toBeGreaterThan(1)
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

describe("same-cell conflicts with an open draft", () => {
  const draft = (cells: string[]) => ({ type: "draft", protocol: 1, cells: cells.map(cell => ({ sheet: "Sheet1", cell })) })

  it("warns at once when another writer changes a cell the editor reports as an open draft, and keeps the session", async () => {
    const t = setup({ commits: vi.fn(async () => feed(42, [remote(42, [["B3", "99"], ["C1", "x"]])])) })
    await t.connect()
    t.send(draft(["B3"]))
    t.bridge.notify({ rev: 42, generation: 1 })
    await flush()
    // The editor still gets the revision exactly as before; the draft cell is the editor's to keep.
    expect(t.types()).toEqual(["committed:42"])
    expect(t.conflicts).toEqual([{ kind: "changed", sheet: "Sheet1", cell: "B3", input: "99", rev: 42, actor_label: "Agent Ada" }])
    expect(t.kills).toEqual([])
  })

  it("warns for a change from another window of the same person too", async () => {
    const other = { ...own(42, 7, [["B3", "other window"]]), session_id: "ws_other", mutation_id: "ws_other:7" }
    const t = setup({ commits: vi.fn(async () => feed(42, [other])) })
    await t.connect()
    t.send(draft(["B3"]))
    t.bridge.notify({ rev: 42, generation: 1 })
    await flush()
    expect(t.conflicts).toEqual([{ kind: "changed", sheet: "Sheet1", cell: "B3", input: "other window", rev: 42, actor_label: "Owner" }])
  })

  it("does not warn for cells without a draft, after the draft closed, or for its own revisions", async () => {
    let head = 42
    const history = [remote(42, [["C1", "x"]]), remote(43, [["B3", "99"]]), own(44, 1, [["D4", "mine"]])]
    const t = setup({
      commits: vi.fn(async (since: number) => feed(head, history.filter(c => c.rev > since && c.rev <= head))),
      edit: vi.fn(async () => receipt(1, 44)),
    })
    await t.connect()
    t.send(draft(["B3"]))
    t.bridge.notify({ rev: 42, generation: 1 })
    await flush()
    t.send(draft([]))
    head = 43
    t.bridge.notify({ rev: 43, generation: 1 })
    await flush()
    t.send(draft(["D4"]))
    t.send(mutation(1, 43, [["D4", "mine"]]))
    head = 44
    await flush()
    expect(t.conflicts).toEqual([])
    expect(t.kills).toEqual([])
  })

  it("validates the draft message: cell addresses only, bounded, exact keys", () => {
    expect(parseFrameMessage(JSON.stringify(draft(["B3"])))).toEqual({ ok: true, value: { type: "draft", cells: [{ sheet: "Sheet1", cell: "B3" }] } })
    expect(parseFrameMessage(JSON.stringify(draft([])))).toEqual({ ok: true, value: { type: "draft", cells: [] } })
    expect(parseFrameMessage(JSON.stringify({ ...draft(["B3"]), text: "secret" })).ok).toBe(false)
    expect(parseFrameMessage(JSON.stringify({ type: "draft", protocol: 1, cells: [{ sheet: "Sheet1", cell: "B3", input: "x" }] })).ok).toBe(false)
    expect(parseFrameMessage(JSON.stringify({ type: "draft", protocol: 1, cells: [{ sheet: "Sheet1", cell: "b3" }] })).ok).toBe(false)
    expect(parseFrameMessage(JSON.stringify(draft(Array.from({ length: BRIDGE_MAX_OPS + 1 }, (_, i) => `A${i + 1}`)))).ok).toBe(false)
  })

  const conflictOn = (cell: string, input: string, rev = 42, actor = "Agent Ada"): TransportResponse => ({ status: 409, body: { error: "precondition_failed", head_rev: rev, conflicts: [{ sheet: "Sheet1", cell, input, rev, actor_label: actor }] } })
  const hostReceipt = (id: string, rev: number) => ok({ operation_key: { artifact_id: "page1", authority_id: "user:u1", mutation_id: id }, outcome: "applied", rev, durability: "node" })
  const hostCommit = (rev: number, host: number, cells: Cells) => ({ rev, kind: "cells", actor_label: "Owner", own: true, session_id: SID, mutation_id: `${SID}:h${host}`, ops: cells.map(([cell, input]) => ({ sheet: "Sheet1", cell, input })) })
  const bodyOps = (body: string | undefined) => (JSON.parse(body!) as { ops: { cell: string; input: string }[] }).ops.map(op => [op.cell, op.input])

  it("on a refused commit keeps every cell of the edit, names the writer of the conflicting one, and marks the rest as not conflicting", async () => {
    const t = setup({ edit: vi.fn(async () => conflictOn("B2", "7")), commits: vi.fn(async () => feed(42, [remote(42, [["B2", "7"]])])) })
    await t.connect()
    t.send(mutation(1, 41, [["B2", "mine"], ["C2", "also mine"]]))
    await flush()
    expect(t.refusals()).toEqual([{
      id: 1, seq: 1, reason: "precondition_failed", state: "open", retryable: true,
      cells: [
        { sheet: "Sheet1", cell: "B2", mine: "mine", theirs: "7", rev: 42, actor_label: "Agent Ada" },
        { sheet: "Sheet1", cell: "C2", mine: "also mine" },
      ],
    }])
    expect(t.state().refused).toBe(1)
    expect(saveWordOf(t.state())).toBe("unsaved")
  })

  it("keep mine on a multi-cell edit resends every cell of it against the head, and is not saved before the receipt", async () => {
    let head = 42
    const kept = hostCommit(43, 1, [["B2", "mine"], ["C2", "also mine"], ["D2", "and this"]])
    const hold = deferred<TransportResponse>()
    const t = setup({
      edit: vi.fn(async (id: string) => id === `${SID}:h1` ? hold.promise : conflictOn("B2", "7")),
      commits: vi.fn(async (since: number) => feed(head, [remote(42, [["B2", "7"]]), kept].filter(c => c.rev > since && c.rev <= head))),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B2", "mine"], ["C2", "also mine"], ["D2", "and this"]]))
    await flush()
    const [refusal] = t.refusals()
    expect(t.bridge.keepMine(refusal.id)).toBe(true)
    await flush()
    expect(JSON.parse(t.editBodies().at(-1)!)).toEqual({ session_id: SID, mutation_id: `${SID}:h1`, base_rev: 42, generation: 1, ops: [
      { kind: "cell.set", sheet: "Sheet1", cell: "B2", input: "mine" },
      { kind: "cell.set", sheet: "Sheet1", cell: "C2", input: "also mine" },
      { kind: "cell.set", sheet: "Sheet1", cell: "D2", input: "and this" },
    ] })
    // In flight: shown as sending (no second click), counted as unsent, never "saved".
    expect(t.refusals()).toEqual([expect.objectContaining({ id: refusal.id, state: "sending" })])
    expect(t.bridge.keepMine(refusal.id)).toBe(false)
    expect(t.bridge.takeTheirs(refusal.id)).toBe(false)
    expect(saveWordOf(t.state())).not.toBe("saved")
    head = 43
    hold.resolve(hostReceipt(`${SID}:h1`, 43))
    await flush()
    expect(t.refusals()).toEqual([])
    expect(t.state()).toMatchObject({ pending: 0, refused: 0, receipt: true })
    expect(saveWordOf(t.state())).toBe("saved")
    // The editor never sent this edit, so it gets no ack for it: it gets the revision instead.
    expect(t.toFrame().at(-1)).toEqual({ type: "committed", protocol: 1, rev: 43, actor_label: "Owner", ops: ops([["B2", "mine"], ["C2", "also mine"], ["D2", "and this"]]) })
    expect(t.transport.edit).toHaveBeenCalledTimes(2)
    // Later frame edits keep their own numbering.
    t.send(mutation(2, 43, [["C9", "next"]]))
    await flush()
    expect(t.transport.edit.mock.calls.at(-1)![0]).toBe(`${SID}:2`)
  })

  it("take theirs on a multi-cell edit drops only the conflicting cell and saves the others, then reloads the head", async () => {
    let head = 42
    const t = setup({
      edit: vi.fn(async (id: string) => id === `${SID}:h1` ? hostReceipt(id, 43) : conflictOn("B2", "7")),
      commits: vi.fn(async (since: number) => feed(head, [remote(42, [["B2", "7"]]), hostCommit(43, 1, [["C2", "also mine"], ["D2", "and this"]])].filter(c => c.rev > since && c.rev <= head))),
      snapshot: vi.fn(async () => ok({ schema_version: 1, rev: head, generation: 1, snapshot: { sheets: ["head"] } })),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B2", "mine"], ["C2", "also mine"], ["D2", "and this"]]))
    await flush()
    head = 43
    expect(t.bridge.takeTheirs(t.refusals()[0].id)).toBe(true)
    await flush()
    expect(bodyOps(t.editBodies().at(-1))).toEqual([["C2", "also mine"], ["D2", "and this"]])
    expect(t.refusals()).toEqual([])
    expect(t.state()).toMatchObject({ pending: 0, refused: 0, trouble: undefined })
    // Everything is settled: the editor reloads the head and drops its own refusal notice.
    expect(t.types().at(-1)).toBe("snapshot")
  })

  it("take theirs when every cell conflicted sends nothing, resolves the refusal and reloads the head so the editor drops its refusal", async () => {
    const t = setup({
      edit: vi.fn(async () => conflictOn("B2", "7")),
      commits: vi.fn(async (since: number) => feed(42, [remote(42, [["B2", "7"]])].filter(c => c.rev > since))),
      snapshot: vi.fn(async () => ok({ schema_version: 1, rev: 42, generation: 1, snapshot: { sheets: ["head"] } })),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B2", "mine"]]))
    await flush()
    const edits = t.transport.edit.mock.calls.length
    const before = t.events.length
    expect(t.bridge.takeTheirs(t.refusals()[0].id)).toBe(true)
    await flush()
    expect(t.transport.edit.mock.calls.length).toBe(edits)
    expect(t.events.slice(before)).toEqual([{ type: "refusals", open: 0 }, { type: "recount", pending: 0 }])
    expect(t.toFrame().at(-1)).toEqual({ type: "snapshot", protocol: 1, rev: 42, generation: 1, snapshot: { sheets: ["head"] }, discarded: [] })
    expect(t.state()).toMatchObject({ pending: 0, refused: 0, trouble: undefined })
    expect(t.kills).toEqual([])
  })

  it("take theirs on one refusal keeps a later refusal of another kind open, its status, and the editor's document", async () => {
    const t = setup({
      edit: vi.fn(async (id: string): Promise<TransportResponse> => id.endsWith(":1") ? conflictOn("B2", "7") : { status: 403, body: { error: "effect_out_of_mandate" } }),
      commits: vi.fn(async (since: number) => feed(42, [remote(42, [["B2", "7"]])].filter(c => c.rev > since))),
      snapshot: vi.fn(async () => ok({ schema_version: 1, rev: 42, generation: 1, snapshot: { sheets: ["head"] } })),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B2", "mine"]]))
    await flush()
    t.send(mutation(2, 42, [["C9", "=RAND()"]]))
    await flush()
    const [first, second] = t.refusals()
    expect(second).toEqual({ id: 2, seq: 2, reason: "effect_out_of_mandate", state: "open", retryable: false, cells: [{ sheet: "Sheet1", cell: "C9", mine: "=RAND()" }] })
    expect(t.bridge.takeTheirs(first.id)).toBe(true)
    await flush()
    expect(t.refusals()).toEqual([second])
    expect(t.state().refused).toBe(1)
    expect(saveWordOf(t.state())).toBe("unsaved")
    expect(t.types()).not.toContain("snapshot")
    // A refusal that cannot succeed again is not retried, only discarded on the person's word.
    expect(t.bridge.keepMine(second.id)).toBe(false)
    expect(t.bridge.takeTheirs(second.id)).toBe(true)
    await flush()
    expect(t.refusals()).toEqual([])
    expect(t.state()).toMatchObject({ refused: 0, trouble: undefined })
    expect(t.types().at(-1)).toBe("snapshot")
  })

  it("take theirs while other edits are unsent resolves that refusal but reloads only once they are saved", async () => {
    const pending = deferred<TransportResponse>()
    let head = 42
    const t = setup({
      edit: vi.fn(async (id: string) => id === `${SID}:2` ? pending.promise : conflictOn("B2", "7")),
      commits: vi.fn(async (since: number) => feed(head, [remote(42, [["B2", "7"]]), own(43, 2, [["C9", "later"]])].filter(c => c.rev > since && c.rev <= head))),
      snapshot: vi.fn(async () => ok({ schema_version: 1, rev: head === 42 ? 41 : head, generation: 1, snapshot: { sheets: ["head"] } })),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B2", "mine"]]))
    await flush()
    t.send(mutation(2, 42, [["C9", "later"]]))
    await flush()
    const snapshots = t.transport.snapshot.mock.calls.length
    expect(t.bridge.takeTheirs(t.refusals()[0].id)).toBe(true)
    await flush()
    expect(t.refusals()).toEqual([])
    expect(t.transport.snapshot.mock.calls.length).toBe(snapshots)
    head = 43
    pending.resolve(receipt(2, 43))
    await flush()
    expect(t.types()).toContain("ack:43")
    expect(t.types().at(-1)).toBe("snapshot")
  })

  it("take theirs with a cell editor open resolves the refusal but leaves the editor's document alone", async () => {
    const t = setup({
      edit: vi.fn(async () => conflictOn("B2", "7")),
      commits: vi.fn(async (since: number) => feed(42, [remote(42, [["B2", "7"]])].filter(c => c.rev > since))),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B2", "mine"]]))
    await flush()
    t.send({ type: "draft", protocol: 1, cells: [{ sheet: "Sheet1", cell: "C3" }] })
    const snapshots = t.transport.snapshot.mock.calls.length
    expect(t.bridge.takeTheirs(t.refusals()[0].id)).toBe(true)
    await flush()
    expect(t.events.at(-1)).toEqual({ type: "refusals", open: 0 })
    expect(t.transport.snapshot.mock.calls.length).toBe(snapshots)
    expect(t.types().at(-1)).not.toBe("snapshot")
  })

  it("take theirs never discards an edit made while the head is being read", async () => {
    const head = deferred<TransportResponse>()
    let reading = false
    const t = setup({
      edit: vi.fn(async (id: string) => id === `${SID}:2` ? receipt(2, 43) : conflictOn("B2", "7")),
      commits: vi.fn(async (since: number) => feed(42, [remote(42, [["B2", "7"]])].filter(c => c.rev > since))),
      snapshot: vi.fn(async () => reading ? head.promise : ok({ schema_version: 1, rev: 41, generation: 1, snapshot: {} })),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B2", "mine"]]))
    await flush()
    reading = true
    t.bridge.takeTheirs(t.refusals()[0].id)
    t.send(mutation(2, 42, [["C9", "later"]]))
    head.resolve(ok({ schema_version: 1, rev: 42, generation: 1, snapshot: { sheets: [] } }))
    await flush()
    expect(t.types()).not.toContain("snapshot")
    expect(t.transport.edit.mock.calls.map(([id]) => id)).toContain(`${SID}:2`)
  })

  it("keep mine refused again reopens the same refusal with the new writer and tells the editor nothing it did not send", async () => {
    let head = 42
    const t = setup({
      edit: vi.fn(async (id: string) => id === `${SID}:h1` ? conflictOn("B2", "8", 43, "Bob") : conflictOn("B2", "7")),
      commits: vi.fn(async (since: number) => feed(head, [remote(42, [["B2", "7"]]), remote(43, [["B2", "8"]], "Bob")].filter(c => c.rev > since && c.rev <= head))),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B2", "mine"], ["C2", "also"]]))
    await flush()
    head = 43
    const [refusal] = t.refusals()
    t.bridge.keepMine(refusal.id)
    await flush()
    expect(t.types()).toEqual(["committed:42", "rejected", "committed:43"])
    expect(t.refusals()).toEqual([{ id: refusal.id, reason: "precondition_failed", state: "open", retryable: true, cells: [
      { sheet: "Sheet1", cell: "B2", mine: "mine", theirs: "8", rev: 43, actor_label: "Bob" },
      { sheet: "Sheet1", cell: "C2", mine: "also" },
    ] }])
    expect(t.state()).toMatchObject({ pending: 0, refused: 1 })
  })

  it("a keep-mine write in flight when a snapshot reload replaces the content comes back as an open refusal with its values", async () => {
    let gone = false
    const hold = deferred<TransportResponse>()
    const reloads: number[] = []
    const t = setup({
      edit: vi.fn(async (id: string): Promise<TransportResponse> => id === `${SID}:h1` ? hold.promise : conflictOn("B2", "7")),
      commits: vi.fn(async (since: number): Promise<TransportResponse> => gone ? { status: 410, body: { error: "snapshot_required", head_rev: 50 } } : feed(42, [remote(42, [["B2", "7"]])].filter(c => c.rev > since))),
      snapshot: vi.fn(async () => ok({ schema_version: 1, rev: gone ? 50 : 41, generation: 1, snapshot: {} })),
    }, { onReload: () => reloads.push(1) })
    await t.connect()
    t.send(mutation(1, 41, [["B2", "mine"], ["C2", "also"]]))
    await flush()
    const [refusal] = t.refusals()
    expect(t.bridge.keepMine(refusal.id)).toBe(true)
    await flush()
    gone = true
    t.bridge.notify({ rev: 50, generation: 1 })
    await flush()
    expect(t.types()).toContain("snapshot")
    hold.resolve({ status: 503, body: null })
    await flush()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(t.refusals()).toEqual([{ id: refusal.id, reason: "reloaded", state: "open", retryable: true, cells: [{ sheet: "Sheet1", cell: "B2", mine: "mine" }, { sheet: "Sheet1", cell: "C2", mine: "also" }] }])
    expect(t.state()).toMatchObject({ pending: 0, refused: 1 })
    expect(saveWordOf(t.state())).toBe("unsaved")
    // Not resent behind the person's back against content they have not seen.
    expect(t.transport.edit.mock.calls.filter(([id]) => id === `${SID}:h1`)).toHaveLength(1)
  })

  it("a queued keep-mine write dropped by a snapshot reload comes back as an open refusal", async () => {
    let gone = false
    const t = setup({
      edit: vi.fn(async (id: string): Promise<TransportResponse> => id === `${SID}:h1` ? { status: 503, body: null } : conflictOn("B2", "7")),
      commits: vi.fn(async (since: number): Promise<TransportResponse> => gone ? { status: 410, body: { error: "snapshot_required", head_rev: 50 } } : feed(42, [remote(42, [["B2", "7"]])].filter(c => c.rev > since))),
      snapshot: vi.fn(async () => ok({ schema_version: 1, rev: gone ? 50 : 41, generation: 1, snapshot: {} })),
      operation: vi.fn(async (): Promise<TransportResponse> => ({ status: 404, body: null })),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B2", "mine"]]))
    await flush()
    t.bridge.keepMine(t.refusals()[0].id)
    await flush() // 503: waiting in backoff
    expect(t.bridge.pending).toBe(1)
    gone = true
    t.bridge.notify({ rev: 50, generation: 1 })
    await flush()
    expect(t.bridge.pending).toBe(0)
    expect(t.refusals()).toEqual([expect.objectContaining({ reason: "reloaded", state: "open", cells: [{ sheet: "Sheet1", cell: "B2", mine: "mine" }] })])
    expect(t.state()).toMatchObject({ pending: 0, refused: 1 })
  })

  it("a keep-mine write in flight across a reload that the server applied after all settles its refusal", async () => {
    let gone = false
    const hold = deferred<TransportResponse>()
    const t = setup({
      edit: vi.fn(async (id: string): Promise<TransportResponse> => id === `${SID}:h1` ? hold.promise : conflictOn("B2", "7")),
      commits: vi.fn(async (since: number): Promise<TransportResponse> => gone ? { status: 410, body: { error: "snapshot_required", head_rev: 50 } } : feed(42, [remote(42, [["B2", "7"]])].filter(c => c.rev > since))),
      snapshot: vi.fn(async () => ok({ schema_version: 1, rev: gone ? 50 : 41, generation: 1, snapshot: {} })),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B2", "mine"]]))
    await flush()
    t.bridge.keepMine(t.refusals()[0].id)
    await flush()
    gone = true
    t.bridge.notify({ rev: 50, generation: 1 })
    await flush()
    hold.resolve(hostReceipt(`${SID}:h1`, 50))
    await flush()
    expect(t.refusals()).toEqual([])
    expect(t.state()).toMatchObject({ pending: 0, refused: 0 })
  })

  it("an edit refused as unconfirmed that the feed later shows committed is no longer an open refusal", async () => {
    let head = 41
    const t = setup({
      edit: vi.fn(async () => ok(null)),
      commits: vi.fn(async (since: number) => since === 41 && head === 42 ? feed(42, [own(42, 1, [["B3", "21"]])]) : feed(since, [])),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await vi.advanceTimersByTimeAsync(60_000)
    expect(t.refusals()).toEqual([expect.objectContaining({ seq: 1, reason: "not_confirmed", retryable: true })])
    head = 42
    t.bridge.notify({ rev: 42, generation: 1 })
    await flush()
    expect(t.refusals()).toEqual([])
    expect(t.state().refused).toBe(0)
  })

  it("counts open refusals as unsaved when the session ends", async () => {
    const t = setup({ edit: vi.fn(async () => conflictOn("B2", "7")), commits: vi.fn(async (since: number) => feed(42, [remote(42, [["B2", "7"]])].filter(c => c.rev > since))) })
    await t.connect()
    t.send(mutation(1, 41, [["B2", "mine"]]))
    await flush()
    t.bridge.close("disabled")
    expect(t.kills).toEqual([{ reason: "disabled", unsaved: 1 }])
  })

  it("tells the host when the editor got a fresh snapshot, and keeps the open refusals", async () => {
    const reloads: number[] = []
    let gone = false
    const t = setup({
      edit: vi.fn(async () => conflictOn("B2", "7")),
      commits: vi.fn(async (since: number): Promise<TransportResponse> => gone ? { status: 410, body: { error: "snapshot_required", head_rev: 50 } } : feed(42, [remote(42, [["B2", "7"]])].filter(c => c.rev > since))),
    }, { onReload: () => reloads.push(1) })
    await t.connect()
    t.send(mutation(1, 41, [["B2", "mine"]]))
    await flush()
    reloads.length = 0
    gone = true
    t.transport.snapshot.mockResolvedValueOnce(ok({ schema_version: 1, rev: 50, generation: 1, snapshot: {} }))
    t.bridge.notify({ rev: 50, generation: 1 })
    await flush()
    expect(t.types().at(-1)).toBe("snapshot")
    expect(reloads).toEqual([1])
    expect(t.refusals()).toEqual([expect.objectContaining({ seq: 1, state: "open" })])
    expect(t.state().refused).toBe(1)
  })

  it("holds at most BRIDGE_MAX_REFUSALS open refusals; past that the editor alone keeps the refused edit", async () => {
    const t = setup({ edit: vi.fn(async (): Promise<TransportResponse> => ({ status: 400, body: { error: "invalid_ops" } })) })
    await t.connect()
    for (let seq = 1; seq <= BRIDGE_MAX_REFUSALS + 2; seq++) { t.send(mutation(seq, 41, [["B2", `x${seq}`]])); await flush() }
    expect(t.refusals()).toHaveLength(BRIDGE_MAX_REFUSALS)
    expect(t.bridge.pending).toBe(0)
    expect(t.toFrame().at(-1)).toEqual({ type: "rejected", protocol: 1, seq: BRIDGE_MAX_REFUSALS + 2, code: "too_many_pending", current_rev: 41, cells: [] })
    expect(t.transport.edit).toHaveBeenCalledTimes(BRIDGE_MAX_REFUSALS)
  })

  it("refuses keep mine and take theirs for an unknown refusal or a session that is not live", async () => {
    const t = setup()
    expect(t.bridge.keepMine(1)).toBe(false)
    expect(t.bridge.takeTheirs(1)).toBe(false)
    await t.connect()
    expect(t.bridge.keepMine(1)).toBe(false)
    expect(t.bridge.takeTheirs(1)).toBe(false)
    expect(t.transport.edit).not.toHaveBeenCalled()
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

describe("catch-up after the editor already holds the head", () => {
  it("an agent change arrived, then Enter on that cell: the 409 is answered and later commits and acks still arrive", async () => {
    const conflict: TransportResponse = { status: 409, body: { error: "precondition_failed", head_rev: 42, snapshot_required: false, conflicts: [{ sheet: "Sheet1", cell: "B2", input: "7", rev: 42, actor_label: "Agent Ada" }] } }
    const history = [remote(42, [["B2", "7"]]), remote(43, [["C9", "x"]]), own(44, 2, [["D1", "late"]])]
    const t = setup({
      edit: vi.fn(async (id: string) => id.endsWith(":1") ? conflict : receipt(2, 44)),
      commits: vi.fn(async (since: number) => feed(Math.max(since, ...history.filter(c => c.rev <= headNow).map(c => c.rev)), history.filter(c => c.rev > since && c.rev <= headNow))),
    })
    let headNow = 42
    await t.connect()
    // The agent's change reaches the editor first.
    t.bridge.notify({ rev: 42, generation: 1 })
    await flush()
    expect(t.types()).toEqual(["committed:42"])
    // The person then presses Enter on the cell the agent changed, against the revision they opened it at.
    t.send(mutation(1, 41, [["B2", "mine"]]))
    await flush()
    expect(t.toFrame().at(-1)).toEqual({ type: "rejected", protocol: 1, seq: 1, code: "precondition_failed", current_rev: 42, cells: [{ sheet: "Sheet1", cell: "B2", input: "7", rev: 42, actor_label: "Agent Ada" }] })
    // Another agent commit: the feed must be read again and the editor must get it.
    headNow = 43
    t.bridge.notify({ rev: 43, generation: 1 })
    await flush()
    expect(t.types()).toEqual(["committed:42", "rejected", "committed:43"])
    // And a later receipt beyond the head is held, then acknowledged after the feed delivered the gap.
    headNow = 44
    t.send(mutation(2, 43, [["D1", "late"]]))
    await flush()
    expect(t.types()).toEqual(["committed:42", "rejected", "committed:43", "ack:44"])
    expect(t.bridge.confirmedRev).toBe(44)
    expect(t.kills).toEqual([])
  })
})

describe("the server's live-session contract", () => {
  const receiptFor = (mutationId: string, rev: number) => ok({ operation_key: { artifact_id: "page1", authority_id: "user:u1", mutation_id: mutationId }, outcome: "applied", rev, durability: "node" })
  const SID2 = "ws_session2"
  const expired: TransportResponse = { status: 401, body: { error: "session_expired" } }
  const reopened = (overrides: Partial<WorkbookSession> = {}, transport: Partial<WorkbookTransport> = {}) => vi.fn(async () => ({
    ok: true as const,
    session: { ...session, session_id: SID2, rev: 41, ...overrides },
    transport: {
      snapshot: async () => ok({ schema_version: 1, rev: 41, generation: 1, snapshot: {} }),
      edit: async (id: string) => receiptFor(id, 42),
      commits: async (since: number) => feed(since, []),
      operation: async (): Promise<TransportResponse> => ({ status: 404, body: { error: "Workbook not found" } }),
      closeSession: async () => {},
      ...transport,
    },
  }))

  it("401 session_expired: opens a new session once and sends the pending edits again under it, after checking the old ones", async () => {
    const edits: [string, string][] = []
    const operations: string[] = []
    const reopen = reopened({}, {
      edit: async (id: string, body: string) => { edits.push([id, body]); return receiptFor(id, 41 + edits.length) },
      operation: async (id: string): Promise<TransportResponse> => { operations.push(id); return { status: 404, body: { error: "Workbook not found" } } },
    })
    const t = setup({ edit: vi.fn(async () => expired) }, { reopen })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    t.send(mutation(2, 41, [["B4", "22"]]))
    await flush()
    expect(reopen).toHaveBeenCalledTimes(1)
    expect(t.kills).toEqual([])
    // The first edit reached the old session: its status is asked before it goes again.
    expect(operations).toEqual([`${SID}:1`])
    // Same frame seq, same base and ops; the server requires the new session's prefix.
    expect(edits).toEqual([
      [`${SID2}:1`, JSON.stringify({ session_id: SID2, mutation_id: `${SID2}:1`, base_rev: 41, generation: 1, ops: [{ kind: "cell.set", sheet: "Sheet1", cell: "B3", input: "21" }] })],
      [`${SID2}:2`, JSON.stringify({ session_id: SID2, mutation_id: `${SID2}:2`, base_rev: 41, generation: 1, ops: [{ kind: "cell.set", sheet: "Sheet1", cell: "B4", input: "22" }] })],
    ])
    expect(t.types()).toEqual(["ack:42", "ack:43"])
    expect(t.bridge.session.session_id).toBe(SID2)
    expect(t.bridge.pending).toBe(0)
  })

  it.each([409, 401, 404])("the editor's own expiry code (%i editor_session_expired) reopens too, never \"access lost\"", async status => {
    const reopen = reopened()
    const t = setup({ edit: vi.fn(async (): Promise<TransportResponse> => ({ status, body: { error: "editor_session_expired" } })) }, { reopen })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush()
    expect(reopen).toHaveBeenCalledTimes(1)
    expect(t.kills).toEqual([])
    expect(t.types()).toEqual(["ack:42"])
  })

  it("takes the old session's receipt instead of sending again when the expired edit had committed", async () => {
    let first = true
    const t = setup({ edit: vi.fn(async (): Promise<TransportResponse> => { if (first) { first = false; throw new TypeError("network") } return expired }) }, {
      reopen: reopened({}, {
        operation: async (id: string): Promise<TransportResponse> => id === `${SID}:1` ? ok({ state: "applied", receipt: receiptFor(`${SID}:1`, 42).body }) : { status: 404, body: null },
        edit: async () => { throw new Error("must not resend") },
      }),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush()
    t.bridge.online() // the retry: status 404 under the old session, then 401
    await flush()
    await flush()
    expect(t.types()).toEqual(["online", "ack:42"])
    expect(t.kills).toEqual([])
  })

  it("still recognises the old session's own revisions in the feed after reopening", async () => {
    const t = setup({ edit: vi.fn(async () => expired) }, {
      reopen: reopened({}, {
        commits: async (since: number) => since === 41 ? feed(42, [own(42, 1, [["B3", "21"]])]) : feed(since, []),
        operation: async (): Promise<TransportResponse> => ({ status: 200, body: { state: "in_progress" } }),
      }),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush()
    t.bridge.notify({ rev: 42, generation: 1 })
    await flush()
    expect(t.types()).toEqual(["ack:42"])
    expect(t.bridge.pending).toBe(0)
  })

  it("reopens at most once per expired session: a new session refused as expired before it was ever accepted ends the editor", async () => {
    const reopen = reopened({}, { edit: async () => expired })
    const t = setup({ edit: vi.fn(async () => expired) }, { reopen })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush(); await flush()
    expect(reopen).toHaveBeenCalledTimes(1)
    expect(t.kills).toEqual([{ reason: "expired", unsaved: 1 }])
  })

  it.each([
    ["the server will not open one", { ok: false as const }, "expired"],
    ["the editor was turned off", { ok: false as const, refusal: "disabled" as const }, "disabled"],
    ["access was lost", { ok: false as const, refusal: "access" as const }, "access"],
  ])("ends the editor when no new session can be opened (%s)", async (_, answer, reason) => {
    const t = setup({ edit: vi.fn(async () => expired) }, { reopen: vi.fn(async () => answer) })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush()
    expect(t.kills).toEqual([{ reason, unsaved: 1 }])
  })

  it.each([
    ["another generation", { generation: 2 }, "generation"],
    ["another bundle", { bundle_digest: "e".repeat(64) }, "disabled"],
  ] as const)("ends the editor when the new session names %s", async (_, overrides, reason) => {
    const t = setup({ edit: vi.fn(async () => expired) }, { reopen: reopened(overrides) })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await flush()
    expect(t.kills).toEqual([{ reason, unsaved: 1 }])
  })

  it("reopens when the heartbeat finds the session expired", async () => {
    const reopen = reopened()
    const t = setup({}, { reopen })
    await t.connect()
    t.bridge.sessionExpired()
    await flush()
    expect(reopen).toHaveBeenCalledTimes(1)
    expect(t.bridge.session.session_id).toBe(SID2)
    expect(t.kills).toEqual([])
  })

  it("take theirs on a session that expired unnoticed opens a new one for the reload instead of ending the editor", async () => {
    const reopen = reopened({}, { snapshot: async () => ok({ schema_version: 1, rev: 42, generation: 1, snapshot: { sheets: ["head"] } }) })
    let expiredNow = false
    const t = setup({
      edit: vi.fn(async (): Promise<TransportResponse> => ({ status: 409, body: { error: "precondition_failed", head_rev: 42, conflicts: [{ sheet: "Sheet1", cell: "B2", input: "7", rev: 42, actor_label: "Agent Ada" }] } })),
      commits: vi.fn(async (since: number) => feed(42, [remote(42, [["B2", "7"]])].filter(c => c.rev > since))),
      snapshot: vi.fn(async (): Promise<TransportResponse> => expiredNow ? expired : ok({ schema_version: 1, rev: 41, generation: 1, snapshot: {} })),
    }, { reopen })
    await t.connect()
    t.send(mutation(1, 41, [["B2", "mine"]]))
    await flush()
    expiredNow = true
    expect(t.bridge.takeTheirs(t.refusals()[0].id)).toBe(true)
    await flush(); await flush()
    expect(t.kills).toEqual([])
    expect(reopen).toHaveBeenCalledTimes(1)
    expect(t.bridge.session.session_id).toBe(SID2)
    expect(t.toFrame().at(-1)).toEqual(expect.objectContaining({ type: "snapshot", rev: 42 }))
  })

  it("410 snapshot_required from the feed rereads the snapshot without ending the editor", async () => {
    const t = setup({ commits: vi.fn(async (): Promise<TransportResponse> => ({ status: 410, body: { error: "snapshot_required", head_rev: 900, generation: 1, min_since: 600 } })) })
    await t.connect()
    t.transport.snapshot.mockResolvedValueOnce(ok({ schema_version: 1, rev: 900, generation: 1, snapshot: {} }))
    t.bridge.notify({ rev: 900, generation: 1 })
    await flush()
    expect(t.types()).toEqual(["snapshot"])
    expect(t.kills).toEqual([])
  })

  it("410 with an unreadable snapshot backs off instead of rereading the feed in a tight loop", async () => {
    let calls = 0
    const t = setup({ commits: vi.fn(async (since: number): Promise<TransportResponse> => ++calls > 50 ? feed(since, []) : { status: 410, body: { error: "snapshot_required", head_rev: 900, generation: 1, min_since: 600 } }) })
    await t.connect()
    t.transport.snapshot.mockResolvedValue({ status: 500, body: null })
    t.bridge.notify({ rev: 900, generation: 1 })
    await flush()
    expect(calls).toBe(1)
    expect(t.kills).toEqual([])
  })

  it("409 snapshot_required: never sends the same packet again; the editor learns the head, then the refusal", async () => {
    const t = setup({
      edit: vi.fn(async (): Promise<TransportResponse> => ({ status: 409, body: { error: "precondition_failed", head_rev: 42, snapshot_required: true, conflicts: [] } })),
      commits: vi.fn(async (since: number) => since === 41 ? feed(42, [remote(42, [["Z9", "1"]])]) : feed(since, [])),
    })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "21"]]))
    await vi.advanceTimersByTimeAsync(5_000)
    expect(t.transport.edit).toHaveBeenCalledTimes(1)
    expect(BRIDGE_MAX_REBASES).toBeGreaterThan(0)
    expect(t.types()).toEqual(["committed:42", "rejected"])
  })

  it("403 effect_out_of_mandate refuses the edit; it does not mean access was lost", async () => {
    const t = setup({ edit: vi.fn(async (id: string): Promise<TransportResponse> => id.endsWith(":1") ? { status: 403, body: { error: "effect_out_of_mandate" } } : receipt(2, 42)) })
    await t.connect()
    t.send(mutation(1, 41, [["B3", "=RAND()"]]))
    t.send(mutation(2, 41, [["B4", "2"]]))
    await flush()
    expect(t.kills).toEqual([])
    expect(t.toFrame()).toEqual([
      { type: "rejected", protocol: 1, seq: 1, code: "effect_out_of_mandate", current_rev: 41, cells: [] },
      { type: "ack", protocol: 1, seq: 2, rev: 42 },
    ])
  })

  it("fails closed on a session without a bundle pin: no port for any hello", async () => {
    const t = setup({}, { sessionOverride: { bundle_digest: "" } })
    t.hello({ type: "gridcraft:hello", protocol: 1, bundle: "" })
    await flush()
    expect(t.frame.postMessage).not.toHaveBeenCalled()
    expect(t.kills).toEqual([{ reason: "handshake", unsaved: 0 }])
  })

  it("parses only sessions the server issues: a positive revision and a plain lowercase hex bundle pin", () => {
    expect(parseWorkbookSession({ ...session, bundle_digest: DIGEST })).not.toBeNull()
    for (const bundle_digest of ["", `sha256:${DIGEST}`, DIGEST.toUpperCase(), DIGEST.slice(1), "sha256:fake-editor"]) expect(parseWorkbookSession({ ...session, bundle_digest })).toBeNull()
    // The server opens sessions only on an imported workbook (revision 1 or later); an edit at 0 is refused.
    expect(parseWorkbookSession({ ...session, rev: 0 })).toBeNull()
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
