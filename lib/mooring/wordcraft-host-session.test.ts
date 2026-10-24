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
  onclose: (() => void) | null = null
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
const submit = (id: string) => ` {"type":"submit_changes","request_id":"${id}","scope":${JSON.stringify(scope)},"session_id":"session","versions":${JSON.stringify(versions)},"changes":[]} \n`
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
