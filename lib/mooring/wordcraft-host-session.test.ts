import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { connectWordCraftHost, openWordCraftSession, type WordCraftOpening } from "./wordcraft-host-session"

const fetch = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetch }))
vi.mock("@/lib/server-base", () => ({ getAuthMode: () => "cookie", resolveWsBase: () => "wss://studio.example" }))
const scope = { workspace_id: "workspace", artifact_id: "page", application_id: "wordcraft" }
const versions = { protocol_version: "0.4.0", model_version: "0.2.0", engine_version: "0.10.0" }
const opening: WordCraftOpening = {
  session_id: "session", participant_ref: "human", connection_ref: "original-connection", ticket: "private-ticket", origin: "17", checkpoint_revision: "0", versions,
  sdk_prefix: `/mooring-sdk/generated/${"a".repeat(64)}`, editor: { url: `/_editor/b/${"b".repeat(64)}/index.html`, bundle_digest: "b".repeat(64) },
}
class Socket {
  static OPEN = 1
  static current: Socket
  readyState = 1
  sent: string[] = []
  onopen: (() => void) | null = null
  onclose: ((event?: { code: number }) => void) | null = null
  onerror: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  constructor(readonly url: string) { Socket.current = this }
  send(raw: string) { this.sent.push(raw) }
  close() { this.readyState = 3 }
  receive(value: object) { this.onmessage?.({ data: JSON.stringify(value) }) }
}
// MOCK Rust boundary: these tests verify the parent call site and lifecycle,
// not cryptographic/schema/SQL/Rust execution qualification.
class Pending {
  static check = vi.fn()
  constructor(readonly joined: string, readonly basis: string, readonly raw: string) {}
  original_request_json() { return this.raw }
  check_host_receipt(raw: string) { Pending.check(raw); return raw }
  free() {}
}
function rig() {
  const callbacks = { bootstrap: vi.fn(), hostFrame: vi.fn(), state: vi.fn(), stopped: vi.fn() }
  const host = connectWordCraftHost("workspace", "page", opening, true, { WordCraftPendingCommit: Pending }, callbacks)
  const socket = Socket.current
  socket.onopen?.()
  const join = JSON.parse(socket.sent[0])
  socket.receive({ type: "session_joined", request_id: join.request_id, scope, session_id: "session", participant_ref: "human", generation: "2", permission_version: "1", versions, gate_id: "gate" })
  const read = JSON.parse(socket.sent[1])
  socket.receive({ type: "checkpoint_result", request_id: read.request_id, checkpoint: { scope, versions, revision: "0", generation: "2", state: "exact-base64" } })
  host.ready()
  return { host, socket, callbacks }
}
const submit = (id: string) => ` {"type":"submit_changes","request_id":"${id}","scope":${JSON.stringify(scope)},"session_id":"session","versions":${JSON.stringify(versions)},"changes":[{"change_id":"${id}","operations":[{"op_id":"${id}"}]}]} \n`
const receipt = (id: string) => ({ type: "commit_result", request_id: id, scope, generation: "2", revision: "1", changes: [{ change_id: id, status: "saved" }] })
beforeEach(() => { vi.stubGlobal("WebSocket", Socket); Pending.check.mockReset(); fetch.mockReset() })
afterEach(() => vi.unstubAllGlobals())
describe("original parent-only WordCraft connection", () => {
  it("retains exact bytes and does not clear later B when A is confirmed", () => {
    const { host, socket, callbacks } = rig()
    expect(socket.url).toBe("wss://studio.example/api/v1/pages/%40page/mooring/ws?workspace_id=workspace&connection_ref=original-connection")
    expect(callbacks.bootstrap.mock.calls[0][0]).toMatchObject({ assigned_origin: "17", access: "writer" })
    expect(callbacks.bootstrap.mock.calls[0][0]).not.toHaveProperty("ticket")
    expect(callbacks.state).not.toHaveBeenCalledWith("saved")
    host.draft(); host.submit(submit("A")); host.draft()
    socket.receive(receipt("A"))
    expect(callbacks.state).toHaveBeenLastCalledWith("draft")
    expect(callbacks.state).not.toHaveBeenCalledWith("saved")
    host.submit(submit("B")); host.submit(submit("B"))
    expect(socket.sent.slice(-2)).toEqual([submit("B"), submit("B")])
    socket.receive(receipt("B"))
    expect(Pending.check).toHaveBeenCalledTimes(2)
    expect(callbacks.state).toHaveBeenLastCalledWith("saved")
    host.close()
  })
  it("never displays Saved for an unknown or rejected Rust receipt", () => {
    for (const mode of ["foreign-request", "rust-rejection"]) {
      const { host, socket, callbacks } = rig()
      host.draft(); host.submit(submit("A"))
      if (mode === "rust-rejection") Pending.check.mockImplementationOnce(() => { throw new Error("invalid_receipt") })
      socket.receive(receipt(mode === "foreign-request" ? "other" : "A"))
      expect(callbacks.state).not.toHaveBeenCalledWith("saved")
      expect(callbacks.state).toHaveBeenLastCalledWith("unsaved-draft")
      expect(callbacks.stopped).toHaveBeenCalled()
      host.close()
    }
  })
  it("peer delivery does not acknowledge local pending work", () => {
    const { host, socket, callbacks } = rig()
    host.draft(); host.submit(submit("A"))
    socket.receive({ type: "document_update", request_id: "peer", scope, session_id: "session", generation: "2", from_revision: "0", to_revision: "1", data: "actual-host-data", receipt: receipt("peer") })
    expect(callbacks.hostFrame).toHaveBeenCalledTimes(1)
    expect(Pending.check).not.toHaveBeenCalled()
    expect(callbacks.state).toHaveBeenLastCalledWith("pending")
    host.close()
  })
  it("closes on rewrites of the same original instead of retrying new bytes", () => {
    const { host, socket, callbacks } = rig()
    host.draft(); host.submit(submit("A")); host.submit(submit("A") + " ")
    expect(socket.sent.filter(raw => raw === submit("A"))).toHaveLength(1)
    expect(callbacks.stopped).toHaveBeenCalled()
    expect(callbacks.state).not.toHaveBeenCalledWith("saved")
    host.close()
  })
  it("fails closed when the authenticated release has no qualified editor bundle", async () => {
    const { editor: _editor, ...missing } = opening
    fetch.mockResolvedValue(new Response(JSON.stringify(missing), { status: 200 }))
    await expect(openWordCraftSession("workspace", "page", true, new AbortController().signal)).rejects.toThrow("editor_unavailable")
    expect(fetch).toHaveBeenCalledWith("/api/v1/pages/%40page/mooring/session?workspace_id=workspace", expect.objectContaining({ skipRefresh: true, body: '{"write":true}' }))
  })
})

describe("authenticated authorization continuity", () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it("bounds an unanswered authorization request and preserves unknown originals", async () => {
    fetch.mockImplementation(() => new Promise(() => {}))
    const { host, socket, callbacks } = rig()
    try {
      host.draft(); host.submit(submit("pending-timeout"))
      const sent = [...socket.sent]
      await vi.advanceTimersByTimeAsync(240_000 + 12_000)
      expect(fetch).toHaveBeenCalledTimes(1)
      expect(callbacks.stopped).toHaveBeenCalledTimes(1)
      expect(callbacks.state).toHaveBeenLastCalledWith("unsaved-draft")
      expect(callbacks.state).not.toHaveBeenCalledWith("saved")
      expect(socket.sent).toEqual(sent)
      await vi.advanceTimersByTimeAsync(10 * 60_000)
      expect(fetch).toHaveBeenCalledTimes(1)
    } finally { host.close() }
  })

  it("fails closed on a rejected request and cancels scheduled work on close", async () => {
    fetch.mockRejectedValue(new Error("network unavailable"))
    const { host, callbacks } = rig()
    host.draft()
    await vi.advanceTimersByTimeAsync(240_000)
    expect(callbacks.stopped).toHaveBeenCalledTimes(1)
    expect(callbacks.state).toHaveBeenLastCalledWith("unsaved-draft")
    host.close()
    await vi.advanceTimersByTimeAsync(10 * 60_000)
    expect(fetch).toHaveBeenCalledTimes(1)
    fetch.mockReset()
    const second = rig()
    second.host.close()
    await vi.advanceTimersByTimeAsync(10 * 60_000)
    expect(fetch).not.toHaveBeenCalled()
  })

  it("crosses several original five-minute horizons without replacing the connection or producer", async () => {
    fetch.mockResolvedValue(new Response(null, { status: 204 }))
    const { host, socket, callbacks } = rig()
    try {
      host.draft()
      const originalFrames = [...socket.sent]
      await vi.advanceTimersByTimeAsync(16 * 60_000)
      expect(fetch.mock.calls.length).toBeGreaterThanOrEqual(3)
      for (const [url, init] of fetch.mock.calls) {
        expect(url).toBe("/api/v1/pages/%40page/mooring/session/reauthorize?workspace_id=workspace")
        expect(init).toMatchObject({ method: "POST", skipRefresh: true, cache: "no-store", redirect: "error" })
        expect(JSON.parse(init.body)).toEqual({ connection_ref: opening.connection_ref })
      }
      expect(Socket.current).toBe(socket)
      expect(socket.sent).toEqual(originalFrames)
      expect(callbacks.bootstrap).toHaveBeenCalledTimes(1)
      expect(callbacks.state).toHaveBeenLastCalledWith("draft")
      expect(callbacks.stopped).not.toHaveBeenCalled()
    } finally { host.close() }
  })

  it("retains an in-flight original with an unobserved result instead of replaying it or claiming Saved", async () => {
    fetch.mockResolvedValue(new Response(null, { status: 204 }))
    const { host, socket, callbacks } = rig()
    try {
      host.draft(); host.submit(submit("unobserved"))
      const sent = [...socket.sent]
      await vi.advanceTimersByTimeAsync(11 * 60_000)
      expect(fetch.mock.calls.length).toBeGreaterThanOrEqual(2)
      expect(socket.sent).toEqual(sent)
      expect(callbacks.state).toHaveBeenLastCalledWith("pending")
      expect(callbacks.state).not.toHaveBeenCalledWith("saved")
      socket.receive(receipt("unobserved"))
      expect(Pending.check).toHaveBeenCalledTimes(1)
      expect(callbacks.state).toHaveBeenLastCalledWith("saved")
    } finally { host.close() }
  })

  it("does not restore revoked access or consume a newer draft while refreshing", async () => {
    fetch.mockResolvedValueOnce(new Response(null, { status: 204 })).mockResolvedValue(new Response(null, { status: 403 }))
    const { host, socket, callbacks } = rig()
    try {
      host.draft(); host.submit(submit("in-flight")); host.draft()
      const sent = [...socket.sent]
      await vi.advanceTimersByTimeAsync(11 * 60_000)
      expect(fetch).toHaveBeenCalledTimes(2)
      expect(callbacks.stopped).toHaveBeenCalledTimes(1)
      expect(callbacks.state).toHaveBeenLastCalledWith("unsaved-draft")
      expect(callbacks.state).not.toHaveBeenCalledWith("saved")
      expect(socket.sent).toEqual(sent)
      expect(callbacks.bootstrap).toHaveBeenCalledTimes(1)
      expect(Socket.current).toBe(socket)
    } finally { host.close() }
  })

  it("cannot revive a stopped original when a refresh response arrives late", async () => {
    let resolve!: (value: Response) => void
    fetch.mockImplementation(() => new Promise<Response>(done => { resolve = done }))
    const { host, socket, callbacks } = rig()
    try {
      host.draft(); host.submit(submit("unknown"))
      await vi.advanceTimersByTimeAsync(295_000)
      expect(fetch).toHaveBeenCalledTimes(1)
      socket.onclose?.()
      const states = callbacks.state.mock.calls.length, frames = [...socket.sent]
      resolve(new Response(null, { status: 204 }))
      await vi.advanceTimersByTimeAsync(10 * 60_000)
      expect(fetch).toHaveBeenCalledTimes(1)
      expect(callbacks.state.mock.calls).toHaveLength(states)
      expect(callbacks.state).toHaveBeenLastCalledWith("unsaved-draft")
      expect(socket.sent).toEqual(frames)
      expect(callbacks.bootstrap).toHaveBeenCalledTimes(1)
    } finally { host.close() }
  })
})

describe("aggregate artifact storage refusal", () => {
  it.each([[4009, "storage_full"], [1006, "connection_unavailable"], [undefined, "connection_unavailable"]])("close code %s stops with %s and keeps the local draft", (code, reason) => {
    const { host, socket, callbacks } = rig()
    host.draft(); host.submit(submit("A"))
    socket.onclose?.(code === undefined ? undefined : { code })
    expect(callbacks.stopped).toHaveBeenCalledWith(reason)
    expect(callbacks.state).toHaveBeenLastCalledWith("unsaved-draft")
    host.close()
  })
})

it("does not let a known Saved operation under a new request clear a later draft", () => {
  const { host, socket, callbacks } = rig()
  host.draft(); host.submit(submit("A")); socket.receive(receipt("A"))
  expect(callbacks.state).toHaveBeenLastCalledWith("saved")
  host.draft()
  const alias = JSON.parse(submit("A")); alias.request_id = "A2"
  host.submit(JSON.stringify(alias))
  socket.receive({ ...receipt("A2"), changes: [{ change_id: "A", status: "saved" }] })
  expect(callbacks.state.mock.calls.at(-1)?.[0]).not.toBe("saved")
  expect(socket.sent.some(raw => JSON.parse(raw).request_id === "A2")).toBe(false)
  host.close()
})

// Synthetic transport + MOCK Rust receipt boundary, as in the lifecycle tests
// above. These exercise Pending correlation only, never SQL/Saved qualification.
const pendingNotice = (request: string, changes: string[] = [request]) => ({
  type: "changes_received", request_id: request, scope, session_id: "session", change_ids: changes, state: "pending",
})
it("keeps a valid runtime Pending diagnostic live until the unchanged original receipt arrives", () => {
  const { host, socket, callbacks } = rig()
  host.draft(); host.submit(submit("P1-pending"))
  socket.receive(pendingNotice("P1-pending"))
  expect(callbacks.stopped).not.toHaveBeenCalled()
  expect(callbacks.state).toHaveBeenLastCalledWith("pending")
  expect(callbacks.state).not.toHaveBeenCalledWith("saved")
  expect(Pending.check).not.toHaveBeenCalled()
  expect(callbacks.hostFrame).not.toHaveBeenCalled() // child does not consume this diagnostic
  host.submit(submit("P1-pending")) // Pending did not consume exact original retry
  socket.receive(pendingNotice("P1-pending"))
  socket.receive(receipt("P1-pending"))
  expect(Pending.check).toHaveBeenCalledTimes(1)
  expect(callbacks.state).toHaveBeenLastCalledWith("saved")
  host.close()
})
it("does not clear a later unsent Draft when an older Pending diagnostic is received", () => {
  const { host, socket, callbacks } = rig()
  host.draft(); host.submit(submit("P1-older")); host.draft()
  socket.receive(pendingNotice("P1-older"))
  expect(callbacks.stopped).not.toHaveBeenCalled()
  expect(callbacks.state).toHaveBeenLastCalledWith("draft")
  expect(Pending.check).not.toHaveBeenCalled()
  expect(callbacks.hostFrame).not.toHaveBeenCalled()
  host.close()
})
for (const mode of ["request", "scope", "session", "member", "order", "duplicate", "state", "extra"]) {
  it(`refuses an uncorrelated runtime Pending diagnostic: ${mode}`, () => {
    const { host, socket, callbacks } = rig()
    const original = JSON.parse(submit("P1-group"))
    original.changes.push({change_id:"P1-second",operations:[{op_id:"P1-second"}]})
    host.draft(); host.submit(JSON.stringify(original))
    const notice = pendingNotice("P1-group", ["P1-group", "P1-second"])
    if (mode === "request") notice.request_id = "other"
    if (mode === "scope") notice.scope = { ...scope, artifact_id: "other" }
    if (mode === "session") notice.session_id = "other"
    if (mode === "member") notice.change_ids = ["P1-group", "other"]
    if (mode === "order") notice.change_ids.reverse()
    if (mode === "duplicate") notice.change_ids = ["P1-group", "P1-group"]
    if (mode === "state") notice.state = "saved"
    if (mode === "extra") Object.assign(notice, { acknowledged: true })
    socket.receive(notice)
    expect(callbacks.stopped).toHaveBeenCalled()
    expect(callbacks.state).toHaveBeenLastCalledWith("unsaved-draft")
    expect(callbacks.state).not.toHaveBeenCalledWith("saved")
    expect(Pending.check).not.toHaveBeenCalled()
    expect(callbacks.hostFrame).not.toHaveBeenCalled()
    host.close()
  })
}

it("does not treat a correlated synchronized diagnostic as durable Saved", () => {
  const { host, socket, callbacks } = rig()
  host.draft(); host.submit(submit("synchronized"))
  socket.receive({ ...pendingNotice("synchronized"), state: "synchronized" })
  expect(callbacks.stopped).not.toHaveBeenCalled()
  expect(callbacks.state).toHaveBeenLastCalledWith("pending")
  expect(callbacks.state).not.toHaveBeenCalledWith("saved")
  expect(Pending.check).not.toHaveBeenCalled()
  expect(callbacks.hostFrame).not.toHaveBeenCalled()
  host.close()
})
it("refuses extra scope fields in a Pending diagnostic", () => {
  const { host, socket, callbacks } = rig()
  host.draft(); host.submit(submit("scope-extra"))
  socket.receive({ ...pendingNotice("scope-extra"), scope: { ...scope, acknowledged: true } })
  expect(callbacks.stopped).toHaveBeenCalled()
  expect(callbacks.state).toHaveBeenLastCalledWith("unsaved-draft")
  expect(Pending.check).not.toHaveBeenCalled()
  host.close()
})

it("initializes the stable Page through the existing scoped native session path",async()=>{
 fetch.mockResolvedValue({ok:true,text:async()=>JSON.stringify(opening)})
 await openWordCraftSession("workspace","page",true,new AbortController().signal,"exact-checkpoint")
 expect(fetch.mock.calls[0][0]).toBe("/api/v1/pages/%40page/mooring/session?workspace_id=workspace")
 expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({write:true,checkpoint_base64:"exact-checkpoint"})
})
