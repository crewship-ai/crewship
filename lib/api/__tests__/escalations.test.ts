import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { escalationResolve } from "../escalations"

describe("escalationResolve", () => {
  let fetchMock: ReturnType<typeof vi.fn>

  beforeEach(() => {
    fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ({}) })
    vi.stubGlobal("fetch", fetchMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("rides workspace_id in the query string so RequireWorkspace passes", async () => {
    const out = await escalationResolve("ws_test", "esc_42", "approve", "ok from inbox")

    expect(out).toEqual({ ok: true })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toBe(
      "/api/v1/escalations/esc_42/resolve?workspace_id=ws_test",
    )
    expect(init.method).toBe("PATCH")
    expect(JSON.parse(init.body)).toMatchObject({ action: "approve", resolution: "ok from inbox" })
  })
})
