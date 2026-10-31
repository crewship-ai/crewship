import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React, { useEffect } from "react"
import { render, act, waitFor } from "@testing-library/react"

// The first session check settles an identity nobody held before. Nothing
// mounted under the provider belongs to an earlier person, so the tree must
// stay mounted: text typed on /login, /signup or /shared-chat before the
// check answers was wiped by a remount, and the CI flows that type early
// failed with it. A change from one known person to another still remounts.

function okJSON(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}
const session = (id: string) => ({ user: { id, name: id, email: `${id}@example.test` }, expires: "2099-12-01" })

describe("AuthProvider first settle", () => {
  let mockFetch: ReturnType<typeof vi.fn>

  beforeEach(() => {
    // local-draft-seal keeps the admitted person at module scope; each case starts from a cold load.
    vi.resetModules()
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    vi.stubGlobal("BroadcastChannel", undefined)
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

  it("remounts the tree when the session changes to another person", async () => {
    mockFetch.mockResolvedValue(okJSON(session("u1")))
    const { counts, probe } = await mount()
    await waitFor(() => expect(probe.auth?.session?.user.id).toBe("u1"))
    mockFetch.mockResolvedValue(okJSON(session("u2")))
    await act(async () => { await probe.auth!.refresh() })
    await waitFor(() => expect(probe.auth?.session?.user.id).toBe("u2"))
    expect(counts.mounts).toBe(2)
  })
})
