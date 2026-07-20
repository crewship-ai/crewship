import { beforeEach, describe, expect, it, vi } from "vitest"

import { apiFetch } from "@/lib/api-fetch"
import {
  _resetAvatarBackfillForTest,
  avatarBackfillBudget,
  queueAvatarBackfill,
  resolveStoredAvatarSrc,
} from "@/lib/agent-avatar-persist"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

const h = vi.hoisted(() => ({ authMode: "cookie" as "cookie" | "bearer" }))
vi.mock("@/lib/server-base", () => ({
  getAuthMode: () => h.authMode,
  withServerBase: (p: string) => "https://server.test" + p,
}))

// A real avatar is a multi-KB SVG; the shape is all these tests need.
vi.mock("@/lib/agent-avatar", () => ({
  getAgentAvatarSVG: (seed: string) =>
    seed === "unloaded" ? null : `<svg xmlns="http://www.w3.org/2000/svg" data-seed="${seed}"/>`,
}))

const mockFetch = vi.mocked(apiFetch)

function ok() {
  return Promise.resolve({ ok: true, status: 200 } as Response)
}

beforeEach(() => {
  mockFetch.mockReset()
  h.authMode = "cookie"
  _resetAvatarBackfillForTest()
})

describe("resolveStoredAvatarSrc", () => {
  it("routes the stored URL through the configured server base", () => {
    expect(resolveStoredAvatarSrc("/api/v1/agents/a1/avatar?v=abc")).toBe(
      "https://server.test/api/v1/agents/a1/avatar?v=abc",
    )
  })

  // An <img> request carries no Authorization header, so in bearer mode
  // (desktop shell) the stored avatar would 401 and render broken. Falling
  // back to seed generation there is strictly better than today's behaviour,
  // never worse.
  it("declines the stored URL in bearer mode so the caller generates instead", () => {
    h.authMode = "bearer"
    expect(resolveStoredAvatarSrc("/api/v1/agents/a1/avatar?v=abc")).toBeNull()
  })

  it("returns null when there is nothing stored", () => {
    expect(resolveStoredAvatarSrc(null)).toBeNull()
    expect(resolveStoredAvatarSrc(undefined)).toBeNull()
  })
})

describe("queueAvatarBackfill", () => {
  it("uploads the generated SVG once and stores it write-once", async () => {
    mockFetch.mockImplementation(ok)
    await queueAvatarBackfill("ag-1", "alice", "thumbs")

    expect(mockFetch).toHaveBeenCalledTimes(1)
    const [url, init] = mockFetch.mock.calls[0]
    expect(url).toBe("/api/v1/agents/ag-1/avatar")
    expect(init?.method).toBe("PUT")
    expect(JSON.parse(String(init?.body)).svg).toContain('data-seed="alice"')
  })

  it("never uploads the same agent twice in one session", async () => {
    mockFetch.mockImplementation(ok)
    await queueAvatarBackfill("ag-1", "alice", "thumbs")
    await queueAvatarBackfill("ag-1", "alice", "thumbs")
    await queueAvatarBackfill("ag-1", "alice", "thumbs")
    expect(mockFetch).toHaveBeenCalledTimes(1)
  })

  // A roster can render hundreds of avatars at once. Without a budget the
  // first paint of a large workspace would fire hundreds of writes.
  it("caps how many uploads one page load may fire", async () => {
    mockFetch.mockImplementation(ok)
    const budget = avatarBackfillBudget()
    for (let i = 0; i < budget + 15; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs")
    }
    expect(mockFetch).toHaveBeenCalledTimes(budget)
  })

  // A viewer has no edit rights, so every attempt would 403. One is
  // enough to learn that; the rest would be pure noise in the log.
  it("stops trying for the whole session after a 403", async () => {
    mockFetch.mockResolvedValue({ ok: false, status: 403 } as Response)
    await queueAvatarBackfill("ag-1", "alice", "thumbs")
    await queueAvatarBackfill("ag-2", "bob", "thumbs")
    await queueAvatarBackfill("ag-3", "carol", "thumbs")
    expect(mockFetch).toHaveBeenCalledTimes(1)
  })

  // 409 means someone else already stored it — benign, and specific to that
  // agent, so it must not disable the whole session the way a 403 does.
  it("keeps going after a 409 conflict on one agent", async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, status: 409 } as Response)
    mockFetch.mockImplementation(ok)
    await queueAvatarBackfill("ag-1", "alice", "thumbs")
    await queueAvatarBackfill("ag-2", "bob", "thumbs")
    expect(mockFetch).toHaveBeenCalledTimes(2)
  })

  it("skips agents whose style has not finished loading", async () => {
    mockFetch.mockImplementation(ok)
    await queueAvatarBackfill("ag-1", "unloaded", "lorelei")
    expect(mockFetch).not.toHaveBeenCalled()
  })

  // The backfill is a background nicety; a failing network must never
  // surface as an unhandled rejection in a render path.
  it("swallows network errors", async () => {
    mockFetch.mockRejectedValue(new Error("offline"))
    await expect(queueAvatarBackfill("ag-1", "alice", "thumbs")).resolves.toBeUndefined()
  })

  it("does nothing without an agent id", async () => {
    mockFetch.mockImplementation(ok)
    await queueAvatarBackfill("", "alice", "thumbs")
    expect(mockFetch).not.toHaveBeenCalled()
  })
})
