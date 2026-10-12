import { beforeEach, describe, expect, it, vi } from "vitest"
import { escalationResolve, escalationSupplyCredential } from "../api/escalations"
import { apiFetch } from "../api-fetch"
vi.mock("../api-fetch", () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
beforeEach(() => { fetchMock.mockReset() })

describe("escalation lifecycle requests", () => {
  it.each(["approve", "reject"] as const)("sends %s to the source lifecycle with encoded workspace scope", async (action) => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    expect(await escalationResolve("id/with space", action, "Operator decision", "workspace&other")).toEqual({ ok: true })
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/escalations/id%2Fwith%20space/resolve?workspace_id=workspace%26other", {
      method: "PATCH", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ action, resolution: "Operator decision" }),
    })
  })
  it("sends credential material only to supply and preserves returned access metadata", async () => {
    const credential = { id: "c", name: "TEST_TOKEN", handle_only: true, granted: false }
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ credential })))
    expect(await escalationSupplyCredential("id/a", "synthetic-test-value", "ws?", { name: "TEST_TOKEN", type: "api_key", securityLevel: 3 })).toEqual({ ok: true, credential })
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/escalations/id%2Fa/supply?workspace_id=ws%3F", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ value: "synthetic-test-value", name: "TEST_TOKEN", type: "api_key", security_level: 3 }),
    })
  })
  it.each(["{}", "null", "malformed"])("accepts a successful supply with optional unreadable/absent metadata: %s", async body => {
    fetchMock.mockResolvedValue(new Response(body))
    expect(await escalationSupplyCredential("e", "value", "w")).toEqual({ ok: true, credential: null })
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({ value: "value" })
  })
  const actions = {
    Resolve: () => escalationResolve("e", "reject", "why", "w"),
    Supply: () => escalationSupplyCredential("e", "value", "w", { name: "", type: "", securityLevel: 0 }),
  }
  for (const [label, action] of Object.entries(actions)) {
    it.each([
      ['{"error":"No longer pending"}', "No longer pending"],
      ["{}", `${label} failed (409)`],
      ["null", `${label} failed (409)`],
      ["invalid JSON", `${label} failed (409)`],
    ])(`${label} preserves refusal and HTTP status for %s`, async (body, error) => {
      fetchMock.mockResolvedValue(new Response(body, { status: 409 }))
      expect(await action()).toEqual({ ok: false, error, status: 409 })
      expect(fetchMock).toHaveBeenCalledTimes(1)
    })
    it.each([new Error("offline"), "transport closed"])(`${label} reports transport failure without claiming an HTTP response`, async error => {
      fetchMock.mockRejectedValue(error)
      expect(await action()).toEqual({ ok: false, error: error instanceof Error ? error.message : error, status: 0 })
    })
  }
})
