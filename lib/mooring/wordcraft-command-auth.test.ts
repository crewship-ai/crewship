/** Real platform apiFetch + C1 request wrapper; fetch is the network fixture. */
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { _resetRefreshInflightForTesting } from "@/lib/api-fetch"
import { commandRequest } from "./wordcraft-command-session"

const opened = { view: "view", actor: "human:alice", format: "wordcraft-c1-v2", engine: "7ef2633a7e84956522d1ca47b99fe6feef16c1db", projection: { generation: "g1", revision: 0, ids: ["doc:g1:block:1"], native_json: "{}" }, outcome: null, editor: { bundle_digest: "a".repeat(64), url: `/_editor/b/${"a".repeat(64)}/index.html` } }
const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } })
const network = vi.fn()
beforeEach(() => { _resetRefreshInflightForTesting(); network.mockReset(); vi.stubGlobal("fetch", network) })
afterEach(() => { vi.unstubAllGlobals(); _resetRefreshInflightForTesting() })
it.each(["open", "read", "submit", "outcome"] as const)("refreshes canonical pre-handler401 once for %s with exactly the same request bytes", async action => {
  network.mockResolvedValueOnce(reply({ error: "session_expired" }, 401)).mockResolvedValueOnce(reply({ ok: true })).mockResolvedValueOnce(reply(opened))
  const input = action === "open" ? { action, write: true } : { action, view: "view", ...(action === "read" ? {} : { raw: 'EXACT original \n {"params":{"text":"č"}}' }) }
  const result = await commandRequest("ws", "page", input, new AbortController().signal)
  expect(result.view).toBe("view")
  expect(network).toHaveBeenCalledTimes(3)
  expect(network.mock.calls[1][0]).toBe("/api/auth/token/refresh")
  expect(network.mock.calls[1][1]).toMatchObject({ method: "POST", credentials: "include" })
  expect(network.mock.calls[0][0]).toBe(network.mock.calls[2][0])
  expect(network.mock.calls[0][1].body).toBe(network.mock.calls[2][1].body)
})
it.each(["network", 409, 503] as const)("never retries an unknown/post-admission %s submit", async outcome => {
  if (outcome === "network") network.mockRejectedValueOnce(new Error("unknown network outcome"))
  else network.mockResolvedValueOnce(reply({ error: "not confirmed" }, outcome))
  await expect(commandRequest("ws", "page", { action: "submit", view: "view", raw: "IMMUTABLE original" }, new AbortController().signal)).rejects.toThrow()
  expect(network).toHaveBeenCalledOnce()
})
it("does not refresh revoked credentials or retry a second401", async () => {
  network.mockResolvedValueOnce(reply({ error: "session_revoked" }, 401))
  await expect(commandRequest("ws", "page", { action: "read", view: "view" }, new AbortController().signal)).rejects.toThrow("access_unavailable")
  expect(network).toHaveBeenCalledOnce()
  network.mockReset()
  network.mockResolvedValueOnce(reply({ error: "session_expired" }, 401)).mockResolvedValueOnce(reply({ ok: true })).mockResolvedValueOnce(reply({ error: "session_expired" }, 401))
  await expect(commandRequest("ws", "page", { action: "submit", view: "view", raw: "original" }, new AbortController().signal)).rejects.toThrow("access_unavailable")
  expect(network).toHaveBeenCalledTimes(3)
})
