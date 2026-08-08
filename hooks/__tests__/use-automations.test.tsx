// The automation read hook.
//
// Both pages that use it treat automations as a SECONDARY fact: a routine
// whose rules could not be loaded must still render, and an issue whose
// automation fetch 403s (the ordinary outcome for a member on an ADMIN-gated
// surface) must not show an error where its properties belong. That is the
// behaviour under test — not the happy path, which is three lines.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

import { apiFetch } from "@/lib/api-fetch"
import { useAutomations } from "@/hooks/use-automations"

const mockFetch = vi.mocked(apiFetch)

function ok(body: unknown) {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}
function status(code: number) {
  return { ok: false, status: code, json: async () => ({}) } as unknown as Response
}

beforeEach(() => {
  mockFetch.mockReset()
})
afterEach(() => {
  vi.clearAllMocks()
})

describe("useAutomations", () => {
  it("does not fetch without a workspace", async () => {
    const { result } = renderHook(() => useAutomations(null))
    await waitFor(() => expect(result.current.automations).toEqual([]))
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("unwraps the envelope the API answers with", async () => {
    mockFetch.mockResolvedValue(ok({ automations: [{ id: "a1" }], count: 1 }))
    const { result } = renderHook(() => useAutomations("ws1"))
    await waitFor(() => expect(result.current.automations).toHaveLength(1))
    expect(result.current.error).toBeNull()
    expect(mockFetch).toHaveBeenCalledWith("/api/v1/automations", expect.anything())
  })

  it("returns an empty list — not a throw — when the caller may not read them", async () => {
    // 403 is the routine outcome for a non-admin. The page carries on without
    // the automations section rather than failing around it.
    mockFetch.mockResolvedValue(status(403))
    const { result } = renderHook(() => useAutomations("ws1"))
    await waitFor(() => expect(result.current.error).toBe("automations: 403"))
    expect(result.current.automations).toEqual([])
  })

  it("survives a body that is not the shape the API promises", async () => {
    // An `automations` key that is not an array would otherwise reach .filter
    // in the consumers and take the whole page down.
    mockFetch.mockResolvedValue(ok({ automations: "not-an-array" }))
    const { result } = renderHook(() => useAutomations("ws1"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.automations).toEqual([])
  })

  it("reports a transport failure without leaving stale rules on screen", async () => {
    mockFetch.mockRejectedValue(new Error("offline"))
    const { result } = renderHook(() => useAutomations("ws1"))
    await waitFor(() => expect(result.current.error).toBe("offline"))
    expect(result.current.automations).toEqual([])
  })
})
