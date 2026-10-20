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
  // The wire is the snake_case contract in FE-REQ-F3-4, not the UI model.
  const wire = { id: "p1", author: "Research agent", run_id: "run-1", target: { block_ids: ["a", "b"] }, base_rev: 4, status: "open", created_at: "2026-10-19T10:00:00Z" }
  it("reads the listed proposals from the snake_case contract", async () => {
    reply(200, { proposals: [wire, { ...wire, id: "p2", run_id: undefined, target: "document" }] })
    expect(await createHttpProposalClient("w", "page").list()).toEqual([
      { id: "p1", author: "Research agent", runId: "run-1", target: { kind: "blocks", blockIds: ["a", "b"] }, baseRev: 4, status: "open", createdAt: "2026-10-19T10:00:00Z" },
      { id: "p2", author: "Research agent", target: { kind: "document" }, baseRev: 4, status: "open", createdAt: "2026-10-19T10:00:00Z" },
    ])
  })
  it("reads one proposal with its base and proposed content", async () => {
    const base = { type: "doc", content: [] }, proposed = { type: "doc", content: [{ type: "paragraph" }] }
    reply(200, { ...wire, base_content: base, proposed_content: proposed })
    expect(await createHttpProposalClient("w", "page").read("p1")).toMatchObject({ id: "p1", baseRev: 4, baseContent: base, proposedContent: proposed })
  })
  it.each([[{ ...wire, base_rev: "4" }], [{ ...wire, status: "weird" }], [{ ...wire, target: { block_ids: [1] } }], [{ ...wire, target: { block_ids: [] } }], [{ ...wire, id: 7 }]])("rejects a malformed proposal %#", async bad => {
    reply(200, { proposals: [bad] })
    await expect(createHttpProposalClient("w", "page").list()).rejects.toThrow("proposals unreadable")
  })
})
