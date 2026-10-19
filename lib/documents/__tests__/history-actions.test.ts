import { afterEach, describe, expect, it, vi } from "vitest"
import { createHttpHistoryActions } from "../history-actions"
import { createHttpProposalClient } from "../proposals"

const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
afterEach(() => fetchMock.mockReset())
const reply = (status: number, body: unknown = {}) => fetchMock.mockResolvedValue({ ok: status < 400, status, json: async () => body })

describe("history action client", () => {
  const actions = createHttpHistoryActions("w", "page")
  it("reverts through its own endpoint with a request ID", async () => {
    reply(200, { rev: 9 })
    expect(await actions.revertChange({ checkpointId: "c", expectedRev: 8, requestId: "r2" })).toEqual({ status: "done", rev: 9 })
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/pages/%40page/document/revert")
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ workspace_id: "w", change_id: "c", expected_rev: 8, request_id: "r2" })
  })
  it.each([
    [404, undefined, { status: "denied" }], [403, undefined, { status: "denied" }],
    [409, { conflicts: ["a"] }, { status: "conflict", blocks: ["a"] }], [409, {}, { status: "stale" }],
    [410, undefined, { status: "retention" }], [503, undefined, { status: "unavailable" }], [500, undefined, { status: "unknown" }], [502, undefined, { status: "unknown" }], [400, undefined, { status: "error" }],
  ])("maps HTTP %s", async (code, body, expected) => { reply(code, body); expect(await actions.revertChange({ checkpointId: "c", expectedRev: 1, requestId: "r" })).toEqual(expected) })
  it("does not trust a success without a revision", async () => { reply(200, {}); expect(await actions.revertChange({ checkpointId: "c", expectedRev: 1, requestId: "r" })).toEqual({ status: "unknown" }) })
  it("treats a network failure as an unknown outcome", async () => { fetchMock.mockRejectedValue(new Error("down")); expect(await actions.revertChange({ checkpointId: "c", expectedRev: 1, requestId: "r" })).toEqual({ status: "unknown" }) })
  it("sends the request ID as header and body", async () => {
    reply(200, { rev: 2 }); await actions.revertChange({ checkpointId: "c", expectedRev: 1, requestId: "key-1" })
    expect(fetchMock.mock.calls[0][1].headers["Idempotency-Key"]).toBe("key-1")
    expect(JSON.parse(fetchMock.mock.calls[0][1].body).request_id).toBe("key-1")
  })
})

describe("proposal client", () => {
  it("accepts with the expected revision and maps a conflict", async () => {
    const client = createHttpProposalClient("w", "page")
    reply(409); expect(await client.accept("p1", 4, "rq")).toEqual({ status: "conflict" })
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/pages/%40page/document/proposals/p1/accept?workspace_id=w")
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ expected_rev: 4, request_id: "rq" })
    reply(200, { rev: 7 }); expect(await client.accept("p1", 4, "rq")).toEqual({ status: "done", rev: 7 })
  })
})
