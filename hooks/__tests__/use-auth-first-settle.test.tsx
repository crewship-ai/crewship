import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React, { useEffect } from "react"
import { render, act, waitFor } from "@testing-library/react"

// The first session check settles an identity nobody held before. Nothing
// mounted under the provider belongs to an earlier person, so the tree must
// stay mounted: text typed on /login, /signup or /shared-chat before the
// check answers was wiped by a remount, and the CI flows that type early
// failed with it. Signing in from a signed-out page and refreshing the same
// person keep it too; leaving a person (another one, or signed out) remounts.

function okJSON(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}
const session = (id: string) => ({ user: { id, name: id, email: `${id}@example.test` }, expires: "2099-12-01" })

class FakeBroadcastChannel {
  static instances: FakeBroadcastChannel[] = []
  postMessage = vi.fn()
  close = vi.fn()
  onmessage: ((ev: { data?: unknown }) => void) | null = null
  constructor() { FakeBroadcastChannel.instances.push(this) }
}

describe("AuthProvider first settle", () => {
  let mockFetch: ReturnType<typeof vi.fn>

  beforeEach(() => {
    // local-draft-seal keeps the admitted person at module scope; each case starts from a cold load.
    vi.resetModules()
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    FakeBroadcastChannel.instances = []
    vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel)
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  async function mount() {
    const { AuthProvider, useAuth } = await import("@/hooks/use-auth")
    const counts = { mounts: 0 }
    const probe: { auth?: ReturnType<typeof useAuth> } = {}
    function Child() {
      probe.auth = useAuth()
      useEffect(() => { counts.mounts++ }, [])
      return null
    }
    render(<AuthProvider><Child /></AuthProvider>)
    return { counts, probe }
  }

  it.each([
    ["signed in", session("u1"), "authenticated"],
    ["signed out", null, "unauthenticated"],
  ] as const)("keeps the tree mounted when the first check settles %s", async (_name, body, status) => {
    mockFetch.mockResolvedValue(okJSON(body))
    const { counts, probe } = await mount()
    await waitFor(() => expect(probe.auth?.status).toBe(status))
    expect(counts.mounts).toBe(1)
  })

  it.each([
    ["the same person again keeps the tree", session("u1"), session("u1"), 1],
    ["the first person after a signed-out page keeps the tree", null, session("u1"), 1],
    ["another person remounts the tree", session("u1"), session("u2"), 2],
    ["signed out remounts the tree", session("u1"), null, 2],
  ] as const)("a later refresh to %s", async (_name, first, later, mounts) => {
    mockFetch.mockResolvedValue(okJSON(first))
    const { counts, probe } = await mount()
    await waitFor(() => expect(probe.auth?.status).toBe(first ? "authenticated" : "unauthenticated"))
    mockFetch.mockResolvedValue(okJSON(later))
    await act(async () => { await probe.auth!.refresh() })
    await waitFor(() => expect(probe.auth?.session?.user.id ?? null).toBe(later?.user.id ?? null))
    expect(counts.mounts).toBe(mounts)
  })

  it("remounts when another tab signed the person out and the same person is back", async () => {
    // The sign-out closed that person's draft handles, so the editors must reopen.
    mockFetch.mockResolvedValue(okJSON(session("u1")))
    const { counts, probe } = await mount()
    await waitFor(() => expect(probe.auth?.session?.user.id).toBe("u1"))
    const channel = FakeBroadcastChannel.instances.find(c => c.onmessage)!
    await act(async () => { channel.onmessage!({ data: { type: "signout", human: "u1" } }) })
    await waitFor(() => expect(counts.mounts).toBe(2))
    expect(probe.auth?.session?.user.id).toBe("u1")
  })
})
