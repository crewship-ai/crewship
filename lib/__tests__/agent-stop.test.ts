import { describe, it, expect, vi, beforeEach } from "vitest"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

import { stopAgent } from "@/lib/agent-stop"

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

describe("stopAgent", () => {
  beforeEach(() => {
    apiFetch.mockReset()
  })

  it("POSTs to the stop route with the workspace in the query", async () => {
    apiFetch.mockResolvedValue(json(200, { id: "a 1", status: "STOPPED" }))
    await stopAgent("a 1", "ws/1")
    expect(apiFetch).toHaveBeenCalledWith(
      "/api/v1/agents/a%201/stop?workspace_id=ws%2F1",
      { method: "POST" },
    )
  })

  it("returns the confirmed status on 200", async () => {
    apiFetch.mockResolvedValue(json(200, { id: "a1", status: "STOPPED" }))
    expect(await stopAgent("a1", "ws1")).toEqual({ ok: true, status: "STOPPED" })
  })

  // internal/api/proxy.go AgentStop: the daemon answered but did not confirm.
  it("explains an unconfirmed stop and says the agent may still be running", async () => {
    apiFetch.mockResolvedValue(json(502, { error: "runtime stop not confirmed" }))
    expect(await stopAgent("a1", "ws1")).toEqual({
      ok: false,
      message: "The runtime didn't confirm the stop. The agent may still be running; check again in a moment.",
    })
  })

  it("explains an unreachable runtime", async () => {
    apiFetch.mockResolvedValue(json(502, { error: "runtime stop unavailable" }))
    expect(await stopAgent("a1", "ws1")).toEqual({
      ok: false,
      message: "The runtime can't be reached right now. The agent may still be running.",
    })
  })

  it("keeps the server's sentence for an unknown 502 and adds the running caveat", async () => {
    apiFetch.mockResolvedValue(new Response("<html>Bad Gateway</html>", { status: 502 }))
    const r = await stopAgent("a1", "ws1")
    expect(r.ok).toBe(false)
    expect(!r.ok && r.message).toMatch(/may still be running/)
  })

  it("passes other refusals through in the server's words", async () => {
    apiFetch.mockResolvedValue(json(403, { error: "Forbidden" }))
    expect(await stopAgent("a1", "ws1")).toEqual({ ok: false, message: "Forbidden" })
    apiFetch.mockResolvedValue(json(404, { error: "Agent not found" }))
    expect(await stopAgent("a1", "ws1")).toEqual({ ok: false, message: "Agent not found" })
  })

  it("reports a network failure instead of throwing", async () => {
    apiFetch.mockRejectedValue(new TypeError("Failed to fetch"))
    const r = await stopAgent("a1", "ws1")
    expect(r.ok).toBe(false)
    expect(!r.ok && r.message).toMatch(/could not reach the server/i)
  })
})
