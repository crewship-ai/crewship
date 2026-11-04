import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { apiFetch, AUTH_EVENT, _resetRefreshInflightForTesting } from "../api-fetch"
import { setWorkspaceRequestSession, setWorkspaceRequestAvailability, WorkspaceUnavailableError } from "../workspace-availability"

// The actual boundary modules run here. Only fetch/cookie observation and the
// observed binding transition are modeled; this is not a browser/Go test.
function deferred<T>() {
 let resolve!: (value: T) => void
 const promise = new Promise<T>(done => { resolve = done })
 return { promise, resolve }
}
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })
const expired = vi.fn()
const broadcasts: unknown[] = []
const oldPayload = { full_name: "Alice intended name" }
type Outcome = { response: Response | null; error: unknown }
const settled = (pending: Promise<Response>): Promise<Outcome> => pending.then(response => ({ response, error: null }), error => ({ response: null, error }))
function bind(human: string) {
 setWorkspaceRequestSession(human)
 setWorkspaceRequestAvailability("shared-workspace", true)
}
beforeEach(() => {
 window.localStorage.clear()
 delete window.__CREWSHIP_SERVER_BASE__
 delete window.__CREWSHIP_TOKEN__
 _resetRefreshInflightForTesting()
 bind("Alice")
 expired.mockClear(); broadcasts.length = 0
 window.addEventListener(AUTH_EVENT, expired)
 vi.stubGlobal("navigator", { locks: undefined })
 vi.stubGlobal("BroadcastChannel", class { postMessage(message: unknown) { broadcasts.push(message) } close() {} })
})
afterEach(() => {
 window.removeEventListener(AUTH_EVENT, expired)
 _resetRefreshInflightForTesting()
 setWorkspaceRequestSession(null); setWorkspaceRequestAvailability(null, true)
 vi.unstubAllGlobals()
})

async function duringRefresh(target: string, change: "human" | "aba" | "workspace" | "none", refreshStatus = 200) {
 let cookieHuman = "Alice"
 const entered = deferred<void>(), reply = deferred<Response>()
 const seen: { url: string; method: string; cookieHuman: string; body: unknown }[] = []
 vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url
  expect(init?.credentials).toBe("include")
  if (url === "/api/auth/token/refresh") { entered.resolve(undefined); return reply.promise }
  seen.push({ url, method: init?.method ?? "GET", cookieHuman, body: typeof init?.body === "string" ? JSON.parse(init.body) : null })
  return seen.length === 1 ? json(401, { error: "session_expired" }) : json(200, { modeledAcceptedAs: cookieHuman })
 }))
 const pending = settled(apiFetch(target, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify(oldPayload) }))
 await entered.promise
 if (change === "human" || change === "aba") { cookieHuman = "Bob"; bind("Bob") }
 if (change === "aba") { cookieHuman = "Alice"; bind("Alice") }
 if (change === "workspace") setWorkspaceRequestAvailability("other-workspace", true)
 reply.resolve(json(refreshStatus, refreshStatus === 401 ? { error: "session_revoked" } : {}))
 const result = await pending
 expect(seen[0]).toEqual({ url: target, method: "PATCH", cookieHuman: "Alice", body: oldPayload })
 return { result, seen }
}
function expectRetired(outcome: Outcome) {
 expect(expired).not.toHaveBeenCalled()
 expect(broadcasts).toEqual([])
 expect(outcome.error).toMatchObject({ name: "RequestSessionChangedError", code: "REQUEST_SESSION_CHANGED" })
}

it("retires the old global profile PATCH after another human is bound", async () => {
 const { result, seen } = await duringRefresh("/api/v1/users/me", "human")
 expect(seen.slice(1)).toEqual([])
 expectRetired(result)
})
it("keeps the workspace request fence as a positive control", async () => {
 const { result, seen } = await duringRefresh("/api/v1/pages/shared?workspace_id=shared-workspace", "human")
 expect(seen.slice(1)).toEqual([])
 expect(result.error).toBeInstanceOf(WorkspaceUnavailableError)
})
it("retires an old global request across human ABA", async () => {
 const { result, seen } = await duringRefresh("/api/v1/users/me", "aba")
 expect(seen.slice(1)).toEqual([])
 expectRetired(result)
})
it("does not emit a logout for Bob from Alice's failed refresh", async () => {
 const { result, seen } = await duringRefresh("/api/v1/users/me", "human", 401)
 expect(seen.slice(1)).toEqual([])
 expectRetired(result)
})
it("still retries global profile changes across a workspace-only switch", async () => {
 const { result, seen } = await duringRefresh("/api/v1/users/me", "workspace")
 expect(result.response?.status).toBe(200)
 expect(seen).toHaveLength(2)
 expect(seen[1]).toEqual({ url: "/api/v1/users/me", method: "PATCH", cookieHuman: "Alice", body: oldPayload })
 expect(expired).not.toHaveBeenCalled()
})
it("still emits expiration when the same bound human's refresh fails", async () => {
 const { result } = await duringRefresh("/api/v1/users/me", "none", 401)
 expect(result.response?.status).toBe(401)
 expect(expired).toHaveBeenCalledTimes(1)
 expect(broadcasts).toHaveLength(1)
})
it("retires a terminal401 that arrives after a human switch", async () => {
 const initial = deferred<Response>(), entered = deferred<void>()
 const network = vi.fn(() => { entered.resolve(undefined); return initial.promise })
 vi.stubGlobal("fetch", network)
 const pending = settled(apiFetch("/api/v1/users/me", { method: "PATCH", body: JSON.stringify(oldPayload) }))
 await entered.promise; bind("Bob"); initial.resolve(json(401, { error: "session_revoked" }))
 expectRetired(await pending)
 expect(network).toHaveBeenCalledTimes(1)
})
it("retires a terminal reason read that finishes after a human switch", async () => {
 const reason = deferred<unknown>(), entered = deferred<void>()
 const response = json(401, {})
 Object.defineProperty(response, "clone", { value: () => ({ json: () => { entered.resolve(undefined); return reason.promise } }) })
 const network = vi.fn(async () => response)
 vi.stubGlobal("fetch", network)
 const pending = settled(apiFetch("/api/v1/users/me", { method: "PATCH", body: JSON.stringify(oldPayload) }))
 await entered.promise; bind("Bob"); reason.resolve({ error: "session_revoked" })
 expectRetired(await pending)
 expect(network).toHaveBeenCalledTimes(1)
})
