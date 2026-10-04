import { beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { inboxBulk } from "@/lib/api/inbox"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
beforeEach(() => { fetchMock.mockReset() })

describe("bulk inbox transitions", () => {
  it.each(["unread", "read", "resolved"] as const)("posts the selected ids for %s and returns partial server counts", async (state) => {
    const result = { updated: 1, skipped: 1, skipped_ids: ["waitpoint"], not_found: 1, state }
    fetchMock.mockResolvedValue(new Response(JSON.stringify(result)))
    expect(await inboxBulk("workspace /?&", ["message", "waitpoint", "missing"], state)).toEqual({ ok: true, result })
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/inbox/bulk?workspace_id=workspace%20%2F%3F%26", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ids: ["message", "waitpoint", "missing"], state }),
    })
  })

  it("includes the selected resolution action without changing the caller's id list", async () => {
    const ids = ["message"]
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ updated: 1 })))
    await inboxBulk("workspace", ids, "resolved", "archived")
    expect(JSON.parse(fetchMock.mock.calls[0][1]!.body as string)).toEqual({ ids, state: "resolved", resolved_action: "archived" })
    expect(ids).toEqual(["message"])
  })

  it("preserves the server's explanation of a rejected transition", async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ error: "ids required" }), { status: 400 }))
    expect(await inboxBulk("workspace", [], "read")).toEqual({ ok: false, error: "ids required" })
  })

  it.each(["invalid JSON", "null", "{}"])("provides a status-bearing fallback for an unavailable error explanation (%s)", async (body) => {
    fetchMock.mockResolvedValue(new Response(body, { status: 503 }))
    expect(await inboxBulk("workspace", ["message"], "read")).toEqual({ ok: false, error: "Bulk action failed (503)" })
  })

  it.each([new Error("offline"), "connection closed"])("returns a failed result when transport rejects (%s)", async (failure) => {
    fetchMock.mockRejectedValue(failure)
    expect(await inboxBulk("workspace", ["message"], "read")).toEqual({ ok: false, error: failure instanceof Error ? failure.message : failure })
  })

  it("does not report success when a successful response has invalid JSON", async () => {
    const response = new Response("{}")
    vi.spyOn(response, "json").mockRejectedValue(new Error("invalid response"))
    fetchMock.mockResolvedValue(response)
    expect(await inboxBulk("workspace", ["message"], "read")).toEqual({ ok: false, error: "invalid response" })
  })
})
