import { describe, expect, it, vi } from "vitest"
import { startWordCraftChild, wordCraftBundleFromPath, type WordCraftChildSDK, type WordCraftNativeWeb } from "../wordcraft-child"

describe("pinned bundle routes", () => {
  const digest = "a".repeat(64)
  it("accepts the host's same-origin editor route", () => {
    expect(wordCraftBundleFromPath(`/_editor/b/${digest}/index.html`)).toBe(digest)
  })
  it("accepts the host's dedicated-origin editor route", () => {
    expect(wordCraftBundleFromPath(`/b/${digest}/index.html`)).toBe(digest)
  })
  it.each([
    `/unknown/b/${digest}/index.html`,
    `//b/${digest}/index.html`,
    `/b/${digest.slice(1)}/index.html`,
    `/b/${digest.toUpperCase()}/index.html`,
    `/b/${digest}/index.html/extra`,
    `/b/${digest}/index.html?module=other`,
    `/b/${digest}/%69ndex.html`,
  ])("rejects an unapproved path %s", (path) => {
    expect(() => wordCraftBundleFromPath(path)).toThrow("invalid bundle identity")
  })
})

// Transport/controller fixtures only. Rust validates actual native/protocol data
// in separate native+Chromium proofs; these fakes grant no authority.
function setup(historyError?: string) {
  const parent = { postMessage: vi.fn() }
  let connect: ((event: MessageEvent) => void) | undefined
  const window = { parent, addEventListener: (_: "message", callback: (e: MessageEvent) => void) => { connect = callback }, removeEventListener: vi.fn() }
  const sent: string[] = []
  const port = { onmessage: null as ((event: { data: string }) => void) | null, postMessage: (raw: string) => sent.push(raw), start: vi.fn(), close: vi.fn() }
  let callbacks!: Parameters<WordCraftNativeWeb["start_collaborative"]>[2]
  const handle = { project_native: vi.fn(), stop: vi.fn() }
  let localNative = '{"opaque":18446744073709551615,"native":"original"}'
  let count = 0
  const cuts: number[][] = []
  const update = vi.fn()
  const ownUpdate = vi.fn()
  const proposal = (id: string) => ({ operation_id: () => id, human_payload_json: (cut: Uint8Array) => {
    cuts.push([...cut]); return JSON.stringify({ kind: "humancrdt.update", operation_id: id, origin: "17", update: "AA==", authored_frontier: "AA==", targets: ["p_one"], authored_checkpoint_state_digest: `sha256:${"a".repeat(64)}` })
  } })
  class Draft {
    native_json = () => localNative
    stable_ids_json = () => '["p_one"]'
    optimistic_checkpoint = () => Uint8Array.of(count)
    observe_native_selection = vi.fn(() => ({}))
    validate_native_transition_selected = vi.fn()
    native_transition_selected(id: string, _before: string, after: string) { localNative = after; count++; return proposal(id) }
    undo = vi.fn((id: string) => { if (historyError) throw historyError; count++; return proposal(id) })
    redo = vi.fn((id: string) => { if (historyError) throw historyError; count++; return proposal(id) })
    apply_verified_document_update = update
    apply_verified_own_document_update = ownUpdate
  }
  const requests: string[] = []
  class Pending {
    constructor(_joined: string, _basis: string, readonly raw: string) { requests.push(raw) }
    original_request_json = () => this.raw
    own_operation_for_host_peer_cut = (_from: string, _raw: string) => JSON.parse(this.raw).changes[0].operations[0].op_id
    check_host_receipt(raw: string) {
      const receipt = JSON.parse(raw)
      if (receipt.request_id !== JSON.parse(this.raw).request_id || receipt.invalid) throw new Error("invalid_receipt")
      return raw
    }
  }
  const sdk: WordCraftChildSDK = {
    validate_host_bootstrap: vi.fn(() => Uint8Array.of(1)),
    validate_host_peer_document_update: vi.fn((_joined, expectedFrom, raw) => {
      const frame = JSON.parse(raw)
      if (frame.from_revision !== expectedFrom) throw new Error("invalid_cut")
      return frame.data
    }),
    WordCraftModel: { restore: vi.fn(() => ({ is_read_only: () => false })) },
    WordCraftLocalDraft: Draft, WordCraftPendingCommit: Pending,
    bind_agent_change: vi.fn((_scope, raw) => raw),
  }
  const native = { start_collaborative: vi.fn((_raw: string, _ids: string, cb: typeof callbacks) => { callbacks = cb; return handle }) }
  const close = startWordCraftChild(sdk, native, "a".repeat(64), () => Promise.resolve(), window)
  connect?.({ source: parent, data: { type: "wordcraft:connect", protocol: 1, bundle: "a".repeat(64) }, ports: [port] } as unknown as MessageEvent)
  const receive = (value: unknown) => port.onmessage?.({ data: typeof value === "string" ? value : JSON.stringify(value) })
  const joined = { type: "session_joined", scope: { workspace_id: "w", artifact_id: "p", application_id: "wordcraft" }, session_id: "s", participant_ref: "p", generation: "1", versions: {} }
  const bootstrap = { protocol: 1, type: "wordcraft:bootstrap", joined_json: JSON.stringify(joined), checkpoint_json: '{"revision":"10","opaque":"untouched"}', assigned_origin: "17", access: "writer", display_name: "Crewship page" }
  const emit = (after: string) => {
    const before = localNative
    callbacks.before(before, "p_one", "0", "0", "text.insert")
    expect(callbacks.validate(before, after)).toBe(true)
    expect(callbacks.authored(before, after)).toBe(true)
  }
  return { receive, bootstrap, sdk, native, handle, port, sent, requests, cuts, emit, close, update, ownUpdate, callbacks: () => callbacks }
}

describe("isolated WordApp child original transport", () => {
  it.each([undefined, "", "x".repeat(513), "bad\nname", "bad\u0085name"])("refuses invalid display metadata before SDK/native init: %s", (display_name) => {
    const s = setup(); s.receive({ ...s.bootstrap, display_name })
    expect(s.sdk.validate_host_bootstrap).not.toHaveBeenCalled()
    expect(s.native.start_collaborative).not.toHaveBeenCalled()
  })

  it("Rust checks bootstrap before native init and preserves native strings", async () => {
    const s = setup(); s.receive(s.bootstrap); await Promise.resolve()
    expect(s.sdk.validate_host_bootstrap).toHaveBeenCalledWith(s.bootstrap.joined_json, s.bootstrap.checkpoint_json)
    expect(s.native.start_collaborative.mock.calls[0][2].display_name).toBe("Crewship page")
    expect(s.native.start_collaborative.mock.calls[0][0]).toContain("18446744073709551615")
    expect(s.sent.map(raw => JSON.parse(raw).type)).toEqual(["wordcraft:ready"])
  })
  it("captures each immutable cut and emits Draft before exact submit; ACK A never advances B basis to batch head", () => {
    const s = setup(); s.receive(s.bootstrap); s.emit("native A"); s.emit("native A+B")
    expect(s.cuts).toEqual([[1], [2]])
    const submit = s.sent.map(raw => JSON.parse(raw)).filter(frame => frame.type === "wordcraft:submit")
    expect(submit.map(frame => frame.raw)).toEqual(s.requests)
    for (const frame of submit) {
      const index = s.sent.findIndex(raw => JSON.parse(raw).raw === frame.raw)
      expect(JSON.parse(s.sent[index - 1])).toMatchObject({ type: "wordcraft:status", state: "draft" })
    }
    const a = JSON.parse(s.requests[0]); const b = JSON.parse(s.requests[1])
    expect(b.changes[0].operations[0].dependencies).toEqual([a.changes[0].operations[0].op_id])
    s.receive({ protocol: 1, type: "wordcraft:host-frame", raw: JSON.stringify({ type: "commit_result", request_id: a.request_id, revision: "99", changes: [{ status: "saved" }] }) })
    s.emit("native A+B+C")
    expect(JSON.parse(s.requests[2]).changes[0].operations[0].base_revision).toBe("10")
    expect(s.sent.map(raw => JSON.parse(raw).state)).not.toContain("saved")
  })
  it("ordered verified peer data reaches SAME producer once and only actual delivered cut moves base", () => {
    const s = setup(); s.receive(s.bootstrap); s.emit("native A")
    const raw = JSON.stringify({ type: "document_update", from_revision: "10", to_revision: "11", data: "closed-app-data" })
    const frame = { protocol: 1, type: "wordcraft:host-frame", raw }
    s.receive(frame); s.receive(frame)
    expect(s.update).toHaveBeenCalledTimes(1)
    expect(s.handle.project_native).toHaveBeenCalledTimes(1)
    s.emit("native A+B")
    expect(JSON.parse(s.requests[1]).changes[0].operations[0].base_revision).toBe("11")
  })
  it("unknown receipt/duplicate private keys fail closed without constructing another producer", () => {
    const s = setup(); s.receive(s.bootstrap); s.emit("native A")
    s.receive('{"protocol":1,"type":"wordcraft:host-frame","raw":"{}","raw":"{}"}')
    expect(s.handle.stop).toHaveBeenCalledTimes(1)
    expect(s.port.close).toHaveBeenCalledTimes(1)
    expect(s.callbacks().can_edit()).toBe(false)
    expect(s.native.start_collaborative).toHaveBeenCalledTimes(1)
    expect(s.requests).toHaveLength(1)
  })
  it("classifies a selected own cut only through retained Rust original membership and never as ACK", () => {
    const s = setup(); s.receive(s.bootstrap); s.emit("native A")
    const own = JSON.parse(s.requests[0]).changes[0]
    const raw = JSON.stringify({ type: "document_update", from_revision: "10", to_revision: "11", data: "own-data",
      receipt: { revision: "99", changes: [{ change_id: own.change_id, status: "saved", last_revision: "11" }] } })
    s.receive({ protocol: 1, type: "wordcraft:host-frame", raw })
    expect(s.ownUpdate).toHaveBeenCalledWith("own-data", own.operations[0].op_id)
    expect(s.update).not.toHaveBeenCalled()
    expect(s.requests).toHaveLength(1)
    expect(s.sent.map(raw => JSON.parse(raw).state)).not.toContain("saved")
  })
  it("local native projection failure propagates UnsavedDraft and retains source originals", () => {
    const s = setup(); s.receive(s.bootstrap); s.emit("native A")
    const original = s.requests[0]
    expect(() => (s.callbacks() as unknown as { failed(): void }).failed()).not.toThrow()
    expect(s.callbacks().can_edit()).toBe(false)
    expect(s.port.close).toHaveBeenCalledTimes(1)
    expect(s.requests).toEqual([original])
    expect(s.sent.map(raw => JSON.parse(raw)).at(-1)).toMatchObject({ type: "wordcraft:status", state: "unsaved-draft" })
    expect(s.sent.map(raw => JSON.parse(raw).state)).not.toContain("saved")
  })
  it.each(["edit.undo", "edit.redo"])("reports a local history refusal for %s without replacing the draft or submitting", command => {
    const s = setup("unsupported_delete_undo"); s.receive(s.bootstrap); s.emit("native A")
    const original = s.requests[0]
    const before = s.sent.length
    let refusal: unknown
    try { s.callbacks().history("native A", command) } catch (cause) { refusal = cause }
    // Preserve the exact SDK exception: the native bridge classifies this as
    // a refusal before mutation, not an uncertain capture requiring shutdown.
    expect(refusal).toBe("unsupported_delete_undo")
    expect(s.sent.slice(before).map(raw => JSON.parse(raw))).toEqual([
      { protocol: 1, type: "wordcraft:status", state: "history-refused", reason: "unsupported_delete_undo" },
    ])
    expect(s.callbacks().can_edit()).toBe(true)
    expect(s.handle.stop).not.toHaveBeenCalled()
    expect(s.port.close).not.toHaveBeenCalled()
    expect(s.requests).toEqual([original])
    expect(s.native.start_collaborative).toHaveBeenCalledTimes(1)
    // A later valid edit still uses the original producer and local state.
    s.emit("native A+B")
    expect(s.requests).toHaveLength(2)
    expect(s.cuts).toEqual([[1], [2]])
    expect(s.sent.map(raw => JSON.parse(raw).state)).not.toContain("saved")
  })
  it("invalid bootstrap cannot initialize a native editor", () => {
    const s = setup(); vi.mocked(s.sdk.validate_host_bootstrap).mockImplementation(() => { throw new Error("invalid") })
    s.receive(s.bootstrap)
    expect(s.native.start_collaborative).not.toHaveBeenCalled()
    expect(s.port.close).toHaveBeenCalledTimes(1)
  })
  it("bounds inbound strings before Rust decoding and retains only the existing aggregate original-byte budget", () => {
    const oversized = setup()
    oversized.receive("x".repeat(2097153))
    expect(oversized.sdk.validate_host_bootstrap).not.toHaveBeenCalled()
    expect(oversized.port.close).toHaveBeenCalledTimes(1)

    const s = setup(); s.receive(s.bootstrap)
    vi.mocked(s.sdk.bind_agent_change).mockImplementation((_scope, raw) => {
      const value = JSON.parse(raw)
      // Transport-size fixture only; actual Rust payload validation is separate.
      value.operations[0].payload.update = "A".repeat(140000)
      return JSON.stringify(value)
    })
    s.emit("native A")
    s.callbacks().before("native A", "p_one", "0", "0", "text.insert")
    expect(s.callbacks().authored("native A", "native A+B")).toBe(false)
    expect(s.requests).toHaveLength(1)
    expect(s.callbacks().can_edit()).toBe(false)
    expect(s.sent.map(raw => JSON.parse(raw).state)).not.toContain("saved")
  })
})


it("pauses native input on interrupted delivery without replacing draft or undo",async()=>{
 const r=setup();r.receive(r.bootstrap);await Promise.resolve()
 expect(r.callbacks().can_edit()).toBe(true)
 r.receive({protocol:1,type:"wordcraft:transport-health",state:"checking"})
 expect(r.callbacks().can_edit()).toBe(false)
 expect(r.handle.stop).not.toHaveBeenCalled()
 expect(r.handle.project_native).not.toHaveBeenCalled()
 r.receive({protocol:1,type:"wordcraft:transport-health",state:"connected"})
 expect(r.callbacks().can_edit()).toBe(true)
 expect(r.native.start_collaborative).toHaveBeenCalledTimes(1)
 r.close.close()
})

it("transport recovery cannot upgrade a reader or revive a terminal producer", () => {
  const s = setup(); s.receive({ ...s.bootstrap, access: "reader" })
  const health = (state: string) => s.receive({ protocol: 1, type: "wordcraft:transport-health", state })
  health("checking"); health("connected")
  expect(s.callbacks().can_edit()).toBe(false)
  expect(() => s.callbacks().before("native", "p_one", "0", "0", "text.insert")).toThrow()
  s.receive({ protocol: 1, type: "wordcraft:stop", reason: "revoked" })
  health("connected")
  expect(s.callbacks().can_edit()).toBe(false)
  expect(s.native.start_collaborative).toHaveBeenCalledTimes(1)
  expect(s.handle.stop).toHaveBeenCalledTimes(1)
})
it("transport pause refuses capture, validation and history without altering originals", () => {
  const s = setup(); s.receive(s.bootstrap); s.emit("native A")
  const originals = [...s.requests]
  s.receive({ protocol: 1, type: "wordcraft:transport-health", state: "checking" })
  expect(() => s.callbacks().before("native A", "p_one", "0", "0", "text.insert")).toThrow()
  expect(s.callbacks().validate("native A", "new")).toBe(false)
  expect(s.callbacks().authored("native A", "new")).toBe(false)
  expect(() => s.callbacks().history("native A", "edit.undo")).toThrow()
  expect(s.requests).toEqual(originals)
  expect(s.handle.stop).not.toHaveBeenCalled()
  expect(s.handle.project_native).not.toHaveBeenCalled()
})
