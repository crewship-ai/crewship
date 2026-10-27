import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactNode } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { parseCraftLease, parseCraftProposal } from "../craft/lease-client"
import { useCraftAvailable, useCraftLease, useCraftLeaseActions, useCraftProposalActions, useCraftProposals } from "../craft/use-craft-lease"

beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_CHAT_CRAFT_LEASE", "true") })
afterEach(() => { cleanup(); vi.unstubAllEnvs(); vi.restoreAllMocks() })
const wrap = ({ children }: { children: ReactNode }) => <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider>

const lease = { holder: { actor_id: "a1", display_name: "Agent Ada", kind: "agent" }, mine: false, generation: 4, can_write: true, handoff_requested_by: null }
const proposal = { id: "pr1", author: { actor_id: "a1", display_name: "Agent Ada", kind: "agent" }, base_version: 2, summary: "Tighten copy", created_at: "2026-10-27T10:00:00Z", state: "open" }

/** Routes by path; anything unknown is the plain 404 of a backend without the routes. */
function serve(routes: Record<string, unknown>) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async input => {
    const path = new URL(String(input), "http://x").pathname
    const key = Object.keys(routes).find(route => path.endsWith(route))
    return key ? new Response(JSON.stringify(routes[key]), { status: 200, headers: { "content-type": "application/json" } })
      : new Response("404 page not found", { status: 404, headers: { "content-type": "text/plain" } })
  })
}
const paths = (spy: ReturnType<typeof serve>) => spy.mock.calls.map(([input, init]) => `${(init as RequestInit | undefined)?.method ?? "GET"} ${new URL(String(input), "http://x").pathname}`)

describe("craft lease hooks", () => {
  it("availability 404 reads as off and no lease or proposal request fires", async () => {
    const spy = serve({})
    const { result } = renderHook(() => ({ available: useCraftAvailable("w", "deck"), lease: useCraftLease("w", "deck"), proposals: useCraftProposals("w", "deck") }), { wrapper: wrap })
    await waitFor(() => expect(spy).toHaveBeenCalled())
    await new Promise(resolve => setTimeout(resolve, 20))
    expect(result.current.available).toBe(false)
    expect(result.current.lease.fetchStatus).toBe("idle")
    expect(paths(spy)).toEqual(["GET /api/v1/pages/deck/craft/availability"])
  })

  it("with the flag off nothing is requested at all", async () => {
    vi.stubEnv("NEXT_PUBLIC_CHAT_CRAFT_LEASE", "false")
    const spy = serve({ "/craft/availability": { enabled: true } })
    const { result } = renderHook(() => useCraftLease("w", "deck"), { wrapper: wrap })
    await new Promise(resolve => setTimeout(resolve, 20))
    expect(spy).not.toHaveBeenCalled()
    expect(result.current.data).toBeUndefined()
  })

  it("loads the lease and proposals once availability says enabled", async () => {
    const spy = serve({ "/craft/availability": { enabled: true }, "/craft/lease": lease, "/craft/proposals": [proposal] })
    const { result } = renderHook(() => ({ lease: useCraftLease("w", "deck"), proposals: useCraftProposals("w", "deck") }), { wrapper: wrap })
    await waitFor(() => expect(result.current.lease.data?.holder?.display_name).toBe("Agent Ada"))
    await waitFor(() => expect(result.current.proposals.data).toHaveLength(1))
    expect(String(spy.mock.calls.find(([input]) => String(input).includes("/craft/lease"))![0])).toContain("workspace_id=w")
  })

  it("actions post to the lease and proposal routes", async () => {
    const spy = serve({ "/craft/lease/request-handoff": {}, "/craft/lease": { ...lease, mine: true }, "/accept": proposal, "/reject": proposal })
    const { result } = renderHook(() => ({ lease: useCraftLeaseActions("w", "deck"), proposals: useCraftProposalActions("w", "deck") }), { wrapper: wrap })
    await act(async () => {
      await result.current.lease.requestHandoff.mutateAsync()
      await result.current.lease.acquire.mutateAsync()
      await result.current.proposals.accept.mutateAsync("pr1")
      await result.current.proposals.reject.mutateAsync("p/2")
    })
    expect(paths(spy)).toEqual([
      "POST /api/v1/pages/deck/craft/lease/request-handoff",
      "POST /api/v1/pages/deck/craft/lease",
      "POST /api/v1/pages/deck/craft/proposals/pr1/accept",
      "POST /api/v1/pages/deck/craft/proposals/p%2F2/reject",
    ])
  })
})

describe("craft parsing", () => {
  it("reads a free lease and refuses an unreadable holder", () => {
    expect(parseCraftLease({ holder: null, mine: false, generation: 0, can_write: true })).toEqual({ holder: null, mine: false, generation: 0, can_write: true })
    expect(parseCraftLease({ ...lease, holder: { actor_id: "a", kind: "robot" } })).toBeNull()
    expect(parseCraftLease({ ...lease, generation: -1 })).toBeNull()
  })
  it("drops proposals outside the contract", () => {
    expect(parseCraftProposal(proposal)).toEqual(proposal)
    expect(parseCraftProposal({ ...proposal, state: "maybe" })).toBeNull()
  })
})
