// Review R7: an instance admin need not belong to any workspace. With no
// workspace the lists are still read — without one — instead of the page
// sitting on "loading" forever.
import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

import { usePeople } from "../use-people"

const res = (body: unknown) => ({ ok: true, status: 200, json: async () => body, headers: { get: () => "instance" } })

beforeEach(() => {
  h.apiFetch.mockReset()
  h.apiFetch.mockImplementation(async (u: string) =>
    String(u).startsWith("/api/v1/admin/users") ? res([{ id: "u1", email: "a@x", memberships: [] }]) : res([{ id: "ws-1", name: "W" }]))
})

describe("usePeople", () => {
  it("reads the lists without a workspace once there is none to wait for", async () => {
    const { result } = renderHook(() => usePeople(null, false))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(h.apiFetch).toHaveBeenCalledWith("/api/v1/admin/users")
    expect(h.apiFetch).toHaveBeenCalledWith("/api/v1/admin/workspaces")
    expect(result.current.people).toHaveLength(1)
  })

  it("waits while the workspace is still being resolved", () => {
    renderHook(() => usePeople(null, true))
    expect(h.apiFetch).not.toHaveBeenCalled()
  })

  it("asks within the workspace when there is one", async () => {
    renderHook(() => usePeople("ws-1", false))
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalledWith("/api/v1/admin/users?workspace_id=ws-1"))
  })
})

describe("usePeople actions without a workspace", () => {
  it("unlocks an account for an instance admin who belongs to no workspace", async () => {
    const { result } = renderHook(() => usePeople(null, false))
    await waitFor(() => expect(result.current.loading).toBe(false))
    h.apiFetch.mockClear()
    h.apiFetch.mockResolvedValue({ ok: true, status: 204, json: async () => ({}) , headers: { get: () => null } })
    await result.current.actions.unlock("u1", "Unlocked")
    expect(h.apiFetch).toHaveBeenCalledWith("/api/v1/admin/users/u1/unlock", expect.objectContaining({ method: "POST" }))
  })
})
