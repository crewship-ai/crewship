import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactNode } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { parseCraftLease } from "../craft/lease-client"
import { useCraftAvailable, useCraftLeaseState } from "../craft/use-craft-lease"

vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "me" } } }) }))

beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_CHAT_CRAFT_LEASE", "true") })
afterEach(() => { cleanup(); vi.unstubAllEnvs(); vi.restoreAllMocks() })
const wrap = ({ children }: { children: ReactNode }) => <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider>

// The #4374 lease status: generation is a decimal string (null before the first acquisition);
// handoff_requested_by lists the pending requests.
const held = {
  holder: { actor_id: "user:pavel", display_name: "Pavel Srba", kind: "human" }, mine: false, generation: "4",
  expires_at: "2026-10-27T18:00:15Z", can_write: true, acquired_at: null, handoff_requested_kind: null, handoff_requested_by: [],
}
const session = { session_token: "secret-token", session_id: "pub-1", application_id: "app", page_id: "page-1" }

type Route = (init: RequestInit | undefined) => Response | Promise<Response>
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } })
/** Routes by "METHOD path-suffix"; anything unknown is the plain 404 of a backend without the routes. */
function serve(routes: Record<string, unknown | Route>) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = new URL(String(input), "http://x").pathname
    const method = init?.method ?? "GET"
    const key = Object.keys(routes).sort((a, b) => b.length - a.length).find(route => { const [m, p] = route.split(" "); return m === method && path.endsWith(p) })
    if (!key) return new Response("404 page not found", { status: 404, headers: { "content-type": "text/plain" } })
    const value = routes[key]
    return typeof value === "function" ? (value as Route)(init) : json(200, value)
  })
}
const calls = (spy: ReturnType<typeof serve>) => spy.mock.calls.map(([input, init]) => ({
  call: `${init?.method ?? "GET"} ${new URL(String(input), "http://x").pathname}`,
  url: String(input),
  session: new Headers(init?.headers).get("X-Craft-Session"),
  body: init?.body,
}))

describe("craft lease state (#4374 contract)", () => {
  it("with the flag off nothing is requested at all", async () => {
    vi.stubEnv("NEXT_PUBLIC_CHAT_CRAFT_LEASE", "false")
    const spy = serve({ "GET /craft/availability": { enabled: true } })
    const { result } = renderHook(() => useCraftLeaseState("w", "doc"), { wrapper: wrap })
    await new Promise(resolve => setTimeout(resolve, 20))
    expect(spy).not.toHaveBeenCalled()
    expect(result.current.status).toBeUndefined()
  })

  it("an unavailable Page asks for nothing beyond availability", async () => {
    const spy = serve({ "GET /craft/availability": { enabled: false } })
    const { result } = renderHook(() => ({ available: useCraftAvailable("w", "doc"), state: useCraftLeaseState("w", "doc") }), { wrapper: wrap })
    await waitFor(() => expect(spy).toHaveBeenCalled())
    await new Promise(resolve => setTimeout(resolve, 20))
    expect(result.current.available).toBe(false)
    expect(calls(spy).map(c => c.call)).toEqual(["GET /api/v1/pages/doc/craft/availability"])
  })

  it("admits once, then reads the lease with the private session header and never puts the token in a URL", async () => {
    const spy = serve({ "GET /craft/availability": { enabled: true }, "POST /craft/sessions": () => json(201, session), "GET /craft/lease": held })
    const { result } = renderHook(() => useCraftLeaseState("w", "doc"), { wrapper: wrap })
    await waitFor(() => expect(result.current.status?.holder?.display_name).toBe("Pavel Srba"))
    const seen = calls(spy)
    expect(seen.map(c => c.call)).toEqual(["GET /api/v1/pages/doc/craft/availability", "POST /api/v1/pages/doc/craft/sessions", "GET /api/v1/pages/doc/craft/lease"])
    expect(seen[1].body).toBe("{}")
    expect(seen[2].session).toBe("secret-token")
    expect(seen[2].url).toContain("workspace_id=w")
    expect(seen.some(c => c.url.includes("secret-token"))).toBe(false)
    expect(JSON.stringify(result.current)).not.toContain("secret-token")
  })

  it("requests a handoff over the same admission with an empty body", async () => {
    const spy = serve({ "GET /craft/availability": { enabled: true }, "POST /craft/sessions": () => json(201, session), "GET /craft/lease": held,
      "POST /craft/lease/request-handoff": () => json(201, { request_id: "req-1", generation: "4" }) })
    const { result } = renderHook(() => useCraftLeaseState("w", "doc"), { wrapper: wrap })
    await waitFor(() => expect(result.current.status).toBeDefined())
    await act(async () => { await result.current.requestHandoff.mutateAsync() })
    const post = calls(spy).find(c => c.call.endsWith("/request-handoff"))!
    expect(post.session).toBe("secret-token")
    expect(post.body).toBe("{}")
    expect(calls(spy).filter(c => c.call.endsWith("/sessions"))).toHaveLength(1)
    await waitFor(() => expect(result.current.requestedGeneration).toBe("4"))
  })

  it("re-admits once when the server no longer knows the admission (restart answers 403)", async () => {
    let tokens = 0
    let leaseReads = 0
    const spy = serve({
      "GET /craft/availability": { enabled: true },
      "POST /craft/sessions": () => json(201, { ...session, session_token: `t${++tokens}` }),
      "GET /craft/lease": (init: RequestInit | undefined) => (++leaseReads, new Headers(init?.headers).get("X-Craft-Session") === "t2" ? json(200, held) : json(403, { error: "native Craft access denied" })),
    })
    const { result } = renderHook(() => useCraftLeaseState("w", "doc"), { wrapper: wrap })
    await waitFor(() => expect(result.current.status?.generation).toBe("4"))
    expect(tokens).toBe(2)
    expect(leaseReads).toBe(2)
    expect(calls(spy).filter(c => c.call.endsWith("/sessions"))).toHaveLength(2)
  })

  it("never offers to take the lease from chat: there is no acquire call", async () => {
    const spy = serve({ "GET /craft/availability": { enabled: true }, "POST /craft/sessions": () => json(201, session), "GET /craft/lease": { ...held, holder: null, generation: null } })
    const { result } = renderHook(() => useCraftLeaseState("w", "doc"), { wrapper: wrap })
    await waitFor(() => expect(result.current.status).toBeDefined())
    expect(result.current.status?.holder).toBeNull()
    expect(calls(spy).some(c => /\/craft\/lease(\/acquire)?$/.test(c.call) && c.call.startsWith("POST"))).toBe(false)
  })

  it("reads the holder as me when the lease is held by my own account in the editor", async () => {
    serve({ "GET /craft/availability": { enabled: true }, "POST /craft/sessions": () => json(201, session), "GET /craft/lease": { ...held, holder: { actor_id: "user:me", display_name: "Me", kind: "human" } } })
    const { result } = renderHook(() => useCraftLeaseState("w", "doc"), { wrapper: wrap })
    await waitFor(() => expect(result.current.status).toBeDefined())
    expect(result.current.heldByMe).toBe(true)
  })

  it("stops polling after a failed read instead of opening a new admission every interval", async () => {
    let tokens = 0
    serve({ "GET /craft/availability": { enabled: true }, "POST /craft/sessions": () => json(201, { ...session, session_token: `t${++tokens}` }),
      "GET /craft/lease": () => json(403, { error: "native Craft access denied" }) })
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      renderHook(() => useCraftLeaseState("w", "doc"), { wrapper: wrap })
      await waitFor(() => expect(tokens).toBe(2))
      await vi.advanceTimersByTimeAsync(20_000)
      expect(tokens).toBe(2)
    } finally { vi.useRealTimers() }
  })

  it("knows my own pending request from the lease even after a reload", async () => {
    serve({ "GET /craft/availability": { enabled: true }, "POST /craft/sessions": () => json(201, session),
      "GET /craft/lease": { ...held, handoff_requested_by: [{ request_id: "r1", actor_id: "user:me", display_name: "Me", kind: "human" }] } })
    const { result } = renderHook(() => useCraftLeaseState("w", "doc"), { wrapper: wrap })
    await waitFor(() => expect(result.current.status).toBeDefined())
    expect(result.current.requestedByMe).toBe(true)
  })
})

describe("craft lease parsing", () => {
  it("reads string generations, null before the first acquisition, and the requests list", () => {
    expect(parseCraftLease(held)).toMatchObject({ generation: "4", holder: { display_name: "Pavel Srba" }, requests: [] })
    expect(parseCraftLease({ ...held, holder: null, generation: null })).toMatchObject({ holder: null, generation: null })
    expect(parseCraftLease({ ...held, handoff_requested_by: [{ request_id: "r1", actor_id: "user:me", display_name: "Me", kind: "human" }] })?.requests).toHaveLength(1)
  })
  it("refuses numeric or malformed generations and an unreadable holder", () => {
    expect(parseCraftLease({ ...held, generation: 4 })).toBeNull()
    expect(parseCraftLease({ ...held, generation: "04" })).toBeNull()
    expect(parseCraftLease({ ...held, generation: "-1" })).toBeNull()
    expect(parseCraftLease({ ...held, holder: { actor_id: "a", kind: "robot" } })).toBeNull()
  })
})
