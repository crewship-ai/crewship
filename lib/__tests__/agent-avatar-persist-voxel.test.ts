import { beforeEach, describe, expect, it, vi } from "vitest"

// #3789 — voxel agents never store a render. DiceBear 10 emits markup the
// server's stored-avatar allowlist refuses (an XML comment and <use href>),
// and a stored <img> render would freeze the animation. They are always drawn
// live from the exactly pinned DiceBear 10 packages instead, so the store and
// the backfill skip them without a request and without counting a failure.

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/server-base", () => ({ getAuthMode: () => "cookie", withServerBase: (p: string) => p }))

import { apiFetch } from "@/lib/api-fetch"
import { avatarInputs, backfillAgentAvatars, storeAgentAvatar } from "@/lib/agent-avatar-persist"

const mockFetch = vi.mocked(apiFetch)

beforeEach(() => mockFetch.mockReset())

describe("voxel agents and the stored render", () => {
  it("an agent with no style and no crew style is drawn with the new default", () => {
    expect(avatarInputs({ id: "a", slug: "filip" })).toEqual({ seed: "filip", style: "voxel-bot" })
  })

  it("an agent pinned to the old default keeps it", () => {
    expect(avatarInputs({ id: "a", slug: "filip", avatar_style: "bottts-neutral" })).toEqual({ seed: "filip", style: "bottts-neutral" })
  })

  it.each(["voxel-bot", "voxel-art", null])("does not store a render for %s", async (style) => {
    const outcome = await storeAgentAvatar({ id: "a", slug: "filip", avatar_style: style }, "ws-1")
    expect(outcome).toBe("live")
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("the backfill counts voxel agents as live, not failed", async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(JSON.stringify([
        { id: "a", slug: "a", avatar_style: "voxel-bot" },
        { id: "b", slug: "b", avatar_style: "voxel-art" },
      ]), { status: 200, headers: { "Content-Type": "application/json", "X-Total-Count": "2" } }),
    )
    const result = await backfillAgentAvatars("ws-1")
    expect(result).toMatchObject({ total: 2, live: 2, failed: 0, stored: 0 })
    expect(mockFetch).toHaveBeenCalledTimes(1)
  })
})
