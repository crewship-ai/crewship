import { describe, it, expect, vi, beforeEach } from "vitest"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

import { stopAgent, stopSuccessMessage } from "@/lib/agent-stop"

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
    expect(await stopAgent("a1", "ws1")).toEqual({ ok: true, status: "STOPPED", alreadyStopped: false })
  })

  // #2879: success for an agent that was already idle; a stable `code` on
  // each refusal, and the copy follows the code, not the sentence.
  it("reports an already-stopped agent as success", async () => {
    apiFetch.mockResolvedValue(json(200, { id: "a1", status: "STOPPED", outcome: "already_stopped" }))
    expect(await stopAgent("a1", "ws1")).toEqual({ ok: true, status: "STOPPED", alreadyStopped: true })
  })

  it("words stop_not_confirmed by its code whatever the sentence", async () => {
    apiFetch.mockResolvedValue(json(502, { error: "something new", code: "stop_not_confirmed" }))
    expect(await stopAgent("a1", "ws1")).toEqual({
      ok: false,
      code: "stop_not_confirmed",
      message: "The runtime didn't confirm the stop. The agent may still be running; check again in a moment.",
    })
  })

  it("words runtime_unavailable by its code", async () => {
    apiFetch.mockResolvedValue(json(502, { error: "anything", code: "runtime_unavailable" }))
    expect(await stopAgent("a1", "ws1")).toEqual({
      ok: false,
      code: "runtime_unavailable",
      message: "The runtime can't be reached right now. The agent may still be running.",
    })
  })

  it("names the success for each outcome", () => {
    expect(stopSuccessMessage({ ok: true, status: "STOPPED", alreadyStopped: true })).toBe("Agent was not running")
    expect(stopSuccessMessage({ ok: true, status: "STOPPED", alreadyStopped: false })).toBe("Agent stopped")
  })

  // internal/api/proxy.go AgentStop before codes: the sentence alone still
  // selects the same copy and code.
  it("explains an unconfirmed stop and says the agent may still be running", async () => {
    apiFetch.mockResolvedValue(json(502, { error: "runtime stop not confirmed" }))
    expect(await stopAgent("a1", "ws1")).toEqual({
      ok: false,
      code: "stop_not_confirmed",
      message: "The runtime didn't confirm the stop. The agent may still be running; check again in a moment.",
    })
  })

  it("explains an unreachable runtime", async () => {
    apiFetch.mockResolvedValue(json(502, { error: "runtime stop unavailable" }))
    expect(await stopAgent("a1", "ws1")).toEqual({
      ok: false,
      code: "runtime_unavailable",
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
