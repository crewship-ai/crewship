import { beforeEach, describe, expect, it, vi } from "vitest"

import { apiFetch } from "@/lib/api-fetch"
import {
  _resetAvatarBackfillForTest,
  avatarBackfillBudget,
  queueAvatarBackfill,
  resolveStoredAvatarSrc,
} from "@/lib/agent-avatar-persist"

// Mirrors MAX_CONSECUTIVE_REFUSALS in the module under test. Kept local
// rather than exported: the exact number is an implementation detail, but a
// test asserting "stops after a run" has to know where the run ends.
const MAX_CONSECUTIVE_REFUSALS_FOR_TEST = 5

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

  // A VIEWER can edit nothing, so every attempt 403s. Stop asking rather
  // than firing one refused write per avatar on the page.
  it("stops trying for the session after a run of refusals", async () => {
    mockFetch.mockResolvedValue({ ok: false, status: 403 } as Response)
    for (let i = 0; i < 12; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs")
    }
    expect(mockFetch).toHaveBeenCalledTimes(MAX_CONSECUTIVE_REFUSALS_FOR_TEST)
  })

  // Edit rights are per agent, not per workspace: a MANAGER may write to
  // agents in crews they lead and be refused on everyone else's. Latching on
  // the first 403 would disable backfill for the agents they CAN persist —
  // the exact role the feature most needs to reach.
  it("keeps going when refusals are interleaved with successes", async () => {
    let call = 0
    mockFetch.mockImplementation(() => {
      call++
      // Refused, refused, allowed, repeating — never a long enough run to trip.
      return Promise.resolve(
        call % 3 === 0 ? ({ ok: true, status: 200 } as Response) : ({ ok: false, status: 403 } as Response),
      )
    })
    for (let i = 0; i < 9; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs")
    }
    expect(mockFetch).toHaveBeenCalledTimes(9)
  })

  // Refused and failed writes store nothing, so they must not consume the
  // budget the successful ones need.
  it("does not spend budget on refused or failed writes", async () => {
    const budget = avatarBackfillBudget()
    let call = 0
    mockFetch.mockImplementation(() => {
      call++
      // One 403 (never a run), then all successes.
      return Promise.resolve(
        call === 1 ? ({ ok: false, status: 403 } as Response) : ({ ok: true, status: 200 } as Response),
      )
    })
    for (let i = 0; i < budget + 5; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs")
    }
    // The refused one didn't count, so a full budget of stores still fits.
    expect(mockFetch).toHaveBeenCalledTimes(budget + 1)
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
