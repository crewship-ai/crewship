import { createHash } from "node:crypto"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { commandRequest, createCommandSession } from "./wordcraft-command-session"
import { commandCanonicalJSON, commandOwnSelection, parseCommandResponse, type CommandResponse } from "./wordcraft-command-contract"
import { wordCraftRenderer } from "./wordcraft-renderer"

const fetcher = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetcher }))
vi.mock("@/lib/server-base", () => ({ getAuthMode: () => "cookie" }))
const hash = (raw: string) => "sha256:" + createHash("sha256").update(raw).digest("hex")
const fingerprint = "sha256:" + "b".repeat(64)
const selection = { anchor: { story: "body", path: [0], off: 2 }, focus: { story: "body", path: [0], off: 2 } }
const opened = (): CommandResponse => ({ view: "original-view", actor: "human:alice", format: "wordcraft-c1-v2", engine: "7ef2633a7e84956522d1ca47b99fe6feef16c1db", projection: { generation: "g1", revision: 0, ids: ["doc:g1:block:1"], native_json: '{"opaque":18446744073709551615}' }, outcome: null, editor: { bundle_digest: "a".repeat(64), url: `/_editor/b/${"a".repeat(64)}/index.html` } })
const proposal = JSON.stringify({ registry_id: "text.insert", command: "text.insert", params: { text: "č" }, target: { block_id: "doc:g1:block:1", unit: "utf8_bytes", start: 0, end: 0 } })
const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
function committed(raw: string): CommandResponse {
  const req = JSON.parse(raw), result = opened()
  result.projection = { ...result.projection, revision: 1, native_json: '{"confirmed":18446744073709551615}' }
  result.outcome = { op_id: req.op_id, input_digest: hash(raw), fingerprint, revision: 1, receipt: { state: "host_committed", op_id: req.op_id, revision: 1, actor: "human:alice", author: "alice", fingerprint, selection } }
  return result
}
const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve() }
function setup(write = true) {
  const project = vi.fn(), state = vi.fn(), stopped = vi.fn()
  const control = createCommandSession("ws", "page", opened(), write, { project, state, stopped })
  return { control, project, state, stopped }
}
beforeEach(() => {
  vi.useFakeTimers(); fetcher.mockReset()
  vi.spyOn(crypto.subtle, "digest").mockImplementation(async (_algorithm, input) => Uint8Array.from(createHash("sha256").update(Buffer.from(input as ArrayBuffer)).digest()).buffer)
})
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })

it("uses standard auth for every action; generic mutation retry is absent", async () => {
  fetcher.mockImplementation(() => Promise.resolve(response(opened())))
  for (const input of [{ action: "open" as const, write: true }, { action: "read" as const, view: "v" }, { action: "submit" as const, view: "v", raw: "EXACT original" }, { action: "outcome" as const, view: "v", raw: "EXACT original" }]) await commandRequest("ws", "page", input, new AbortController().signal)
  expect(fetcher.mock.calls.map(call => call[1].skipRefresh)).toEqual([false, false, false, false])
  expect(fetcher.mock.calls.every(call => call[0] === "/api/v1/pages/%40page/mooring/command-session?workspace_id=ws")).toBe(true)
  expect(JSON.parse(fetcher.mock.calls[2][1].body).raw).toBe("EXACT original")
})
it("captures one actual registry proposal, binds UTF8 target/colon ID and waits for native apply before Saved", async () => {
  const f = setup(); f.control.start()
  fetcher.mockImplementation((_url, init) => Promise.resolve(response(committed(JSON.parse(init.body).raw))))
  expect(f.control.submit(proposal, 0)).toBe(true)
  expect(f.control.submit(proposal, 0)).toBe(false)
  await vi.waitFor(() => expect(f.project).toHaveBeenCalledOnce())
  const raw = JSON.parse(fetcher.mock.calls[0][1].body).raw, original = JSON.parse(raw)
  expect(raw).toBe(commandCanonicalJSON(original))
  expect(original).toMatchObject({ participant: "human:alice", generation: "g1", base_revision: 0, command: "text.insert", params: { text: "č" }, target: JSON.parse(proposal).target })
  expect(original.op_id).toMatch(/^g1:[0-9a-f-]{36}$/)
  expect(f.project.mock.calls[0][0].native_json).toBe('{"confirmed":18446744073709551615}')
  expect(f.project.mock.calls[0][1]).toBe(JSON.stringify(selection))
  expect(f.state).not.toHaveBeenCalledWith("saved")
  f.control.applied(true, 1)
  expect(f.state).toHaveBeenLastCalledWith("saved")
  f.control.close()
})
it("retains exact unknown original and only explicit read-only reconciliation can resolve it", async () => {
  const f = setup(); f.control.start()
  fetcher.mockRejectedValueOnce(new Error("lost response"))
  expect(f.control.submit(proposal, 0)).toBe(true)
  await vi.waitFor(() => expect(f.state).toHaveBeenCalledWith("unknown"))
  const original = JSON.parse(fetcher.mock.calls[0][1].body).raw
  await vi.advanceTimersByTimeAsync(30_000)
  expect(fetcher).toHaveBeenCalledTimes(1)
  expect(f.control.submit(proposal, 0)).toBe(false)
  fetcher.mockResolvedValueOnce(response(opened()))
  await f.control.reconcile()
  expect(JSON.parse(fetcher.mock.calls[1][1].body)).toEqual({ action: "outcome", view: "original-view", raw: original })
  expect(f.project).not.toHaveBeenCalled(); expect(f.state).not.toHaveBeenCalledWith("saved")
  fetcher.mockResolvedValueOnce(response(committed(original)))
  await f.control.reconcile()
  expect(JSON.parse(fetcher.mock.calls[2][1].body).raw).toBe(original)
  expect(f.project).toHaveBeenCalledOnce()
  f.control.applied(true, 1); expect(f.state).toHaveBeenLastCalledWith("saved")
  expect(fetcher.mock.calls.filter(call => JSON.parse(call[1].body).action === "submit")).toHaveLength(1)
  f.control.close()
})
it.each(["op", "actor", "input", "revision", "generation", "selection", "missing-selection"])("refuses a mismatched %s receipt without releasing original or declaring Saved", async field => {
  const f = setup(); f.control.start()
  fetcher.mockImplementation((_url, init) => {
    const value = committed(JSON.parse(init.body).raw), out = value.outcome!
    if (field === "op") out.op_id = "other"
    if (field === "actor") out.receipt.actor = "human:mallory"
    if (field === "input") out.input_digest = "sha256:" + "c".repeat(64)
    if (field === "revision") value.projection.revision = 2
    if (field === "generation") value.projection.generation = "new-generation"
    if (field === "selection") out.receipt.selection = { ...selection, extra: "é".repeat(2048) }
    if (field === "missing-selection") delete out.receipt.selection
    return Promise.resolve(response(value))
  })
  f.control.submit(proposal, 0)
  await vi.waitFor(() => expect(f.state).toHaveBeenCalledWith("unknown"))
  expect(f.project).not.toHaveBeenCalled(); expect(f.state).not.toHaveBeenCalledWith("saved"); expect(f.control.canEdit()).toBe(false)
  f.control.close()
})
it("serializes peer projection consumption and never lets a peer acknowledge an own original", async () => {
  const f = setup(); f.control.start()
  const peer = opened(); peer.projection.revision = 1
  fetcher.mockResolvedValue(response(peer))
  await vi.advanceTimersByTimeAsync(2000)
  await vi.waitFor(() => expect(f.project).toHaveBeenCalledOnce())
  expect(f.project.mock.calls[0][1]).toBeNull()
  await vi.advanceTimersByTimeAsync(10_000); expect(fetcher).toHaveBeenCalledTimes(1)
  f.control.applied(false, 1)
  expect(f.state).not.toHaveBeenCalledWith("saved")
  await vi.advanceTimersByTimeAsync(2000); expect(fetcher).toHaveBeenCalledTimes(2)
  f.control.close()
})
it("ignores a cancelled idle read when the original command is captured, without replaying either", async () => {
  const f = setup(); f.control.start()
  let resolveRead!: (v: Response) => void
  fetcher.mockImplementationOnce(() => new Promise(resolve => { resolveRead = resolve }))
  await vi.advanceTimersByTimeAsync(2000)
  fetcher.mockRejectedValueOnce(new Error("unknown submit"))
  expect(f.control.submit(proposal, 0)).toBe(true)
  const peer = opened(); peer.projection.revision = 9
  resolveRead(response(peer)); await flush()
  expect(f.project).not.toHaveBeenCalled()
  await vi.waitFor(() => expect(f.state).toHaveBeenCalledWith("unknown"))
  expect(fetcher).toHaveBeenCalledTimes(2); expect(f.stopped).not.toHaveBeenCalled()
  f.control.close()
})
it("keeps reader input disabled and closes on a later delivery denial", async () => {
  const f = setup(false); f.control.start()
  expect(f.control.submit(proposal, 0)).toBe(false)
  expect(f.control.canEdit()).toBe(false)
  fetcher.mockResolvedValue(response({}, 403))
  await vi.advanceTimersByTimeAsync(2000); await flush()
  expect(f.stopped).toHaveBeenCalledOnce(); expect(f.state).not.toHaveBeenCalledWith("saved")
})
it("rejects a stale renderer cut and duplicate IDs without sending a mutation", () => {
  const f = setup(); f.control.start()
  expect(f.control.submit(proposal, 1)).toBe(false)
  expect(fetcher).not.toHaveBeenCalled(); f.control.close()
  const duplicate = opened(); duplicate.projection.ids.push(duplicate.projection.ids[0])
  expect(() => parseCommandResponse(duplicate)).toThrow("invalid_projection")
})
it("forwards native catalog DATA generically without a duplicate JS command/parameter allowlist", async () => {
  const f = setup(); f.control.start()
  const native = { registry_id: "format.italic", command: "format.italic", params: { native_parameter: [true, "opaque"] }, target: null }
  fetcher.mockImplementation((_url, init) => Promise.resolve(response(committed(JSON.parse(init.body).raw))))
  expect(f.control.submit(JSON.stringify(native), 0)).toBe(true)
  await vi.waitFor(() => expect(f.project).toHaveBeenCalledOnce())
  expect(JSON.parse(JSON.parse(fetcher.mock.calls[0][1].body).raw)).toMatchObject({ command: native.command, params: native.params, target: null, participant: "human:alice" })
  f.control.close()
})
it("a native alias without qualified actual selection stays unconfirmed instead of guessing a caret", async () => {
  const f = setup(); f.control.start()
  fetcher.mockImplementation((_url, init) => { const value = committed(JSON.parse(init.body).raw); delete value.outcome!.receipt.selection; return Promise.resolve(response(value)) })
  expect(f.control.submit(JSON.stringify({ registry_id: "edit.undo", command: "participant.undo", params: {}, target: null }), 0)).toBe(true)
  await vi.waitFor(() => expect(f.state).toHaveBeenCalledWith("unknown"))
  expect(f.project).not.toHaveBeenCalled(); expect(f.state).not.toHaveBeenCalledWith("saved")
  expect(JSON.parse(JSON.parse(fetcher.mock.calls[0][1].body).raw).command).toBe("participant.undo")
  f.control.close()
})
it("selects immutable formats fail closed with no alternate engine", () => {
  expect(wordCraftRenderer(undefined, false)).toBe("legacy")
  expect(wordCraftRenderer("mooring-causal-v1", true)).toBe("legacy")
  expect(wordCraftRenderer("wordcraft-c1-v2", true)).toBe("commands")
  for (const format of [null, "unknown", "wordcraft-c1-v2"]) expect(wordCraftRenderer(format, false)).toBe("unavailable")
})
it("does not infer selection from text length or accept wrong-author receipts", () => {
  const raw = commandCanonicalJSON({ op_id: "g1:test", generation: "g1", participant: "human:alice", base_revision: 0 })
  const value = committed(raw)
  value.outcome!.receipt.author = "mallory"
  expect(() => commandOwnSelection(value, raw, hash(raw))).toThrow("unconfirmed_original")
})
