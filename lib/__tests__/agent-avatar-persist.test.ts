import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

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

// Mirrors MAX_CONSECUTIVE_FAILURES: the same idea for answers that are not a
// permission refusal — a 400 from an endpoint that will 400 for every agent.
const MAX_CONSECUTIVE_FAILURES_FOR_TEST = 5

/** The workspace the backfill writes are scoped to. */
const WS = "ws-1"

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
    await queueAvatarBackfill("ag-1", "alice", "thumbs", WS)

    expect(mockFetch).toHaveBeenCalledTimes(1)
    const [url, init] = mockFetch.mock.calls[0]
    expect(url).toBe(`/api/v1/agents/ag-1/avatar?workspace_id=${WS}`)
    expect(init?.method).toBe("PUT")
    expect(JSON.parse(String(init?.body)).svg).toContain('data-seed="alice"')
  })

  it("never uploads the same agent twice in one session", async () => {
    mockFetch.mockImplementation(ok)
    await queueAvatarBackfill("ag-1", "alice", "thumbs", WS)
    await queueAvatarBackfill("ag-1", "alice", "thumbs", WS)
    await queueAvatarBackfill("ag-1", "alice", "thumbs", WS)
    expect(mockFetch).toHaveBeenCalledTimes(1)
  })

  // A roster can render hundreds of avatars at once. Without a budget the
  // first paint of a large workspace would fire hundreds of writes.
  it("caps how many uploads one page load may fire", async () => {
    mockFetch.mockImplementation(ok)
    const budget = avatarBackfillBudget()
    for (let i = 0; i < budget + 15; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs", WS)
    }
    expect(mockFetch).toHaveBeenCalledTimes(budget)
  })

  // A VIEWER can edit nothing, so every attempt 403s. Stop asking rather
  // than firing one refused write per avatar on the page.
  it("stops trying for the session after a run of refusals", async () => {
    mockFetch.mockResolvedValue({ ok: false, status: 403 } as Response)
    for (let i = 0; i < 12; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs", WS)
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
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs", WS)
    }
    expect(mockFetch).toHaveBeenCalledTimes(9)
  })

  // A refusal stores nothing and is bounded by its own run-of-refusals latch,
  // so it must not consume the budget the successful writes need. (A failure
  // that is NOT a refusal is different — see the 400 case below.)
  it("does not spend budget on refused writes", async () => {
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
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs", WS)
    }
    // The refused one didn't count, so a full budget of stores still fits.
    expect(mockFetch).toHaveBeenCalledTimes(budget + 1)
  })

  // 409 means someone else already stored it — benign, and specific to that
  // agent, so it must not disable the whole session the way a 403 does.
  it("keeps going after a 409 conflict on one agent", async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, status: 409 } as Response)
    mockFetch.mockImplementation(ok)
    await queueAvatarBackfill("ag-1", "alice", "thumbs", WS)
    await queueAvatarBackfill("ag-2", "bob", "thumbs", WS)
    expect(mockFetch).toHaveBeenCalledTimes(2)
  })

  it("skips agents whose style has not finished loading", async () => {
    mockFetch.mockImplementation(ok)
    await queueAvatarBackfill("ag-1", "unloaded", "lorelei", WS)
    expect(mockFetch).not.toHaveBeenCalled()
  })

  // The backfill is a background nicety; a failing network must never
  // surface as an unhandled rejection in a render path.
  it("swallows network errors", async () => {
    mockFetch.mockRejectedValue(new Error("offline"))
    await expect(queueAvatarBackfill("ag-1", "alice", "thumbs", WS)).resolves.toBeUndefined()
  })

  // #2196: the PUT is routed through wsCtx, which resolves the workspace from
  // the query string, a {workspaceId} path segment, or X-Workspace-ID. This
  // path has no workspace segment and apiFetch sets no header, so without the
  // query param every write 400s at the middleware — which is exactly what
  // shipped, for months, while this suite was green.
  it("scopes the write to the caller's workspace", async () => {
    mockFetch.mockImplementation(ok)
    await queueAvatarBackfill("ag-1", "alice", "thumbs", WS)

    const [url] = mockFetch.mock.calls[0]
    expect(String(url)).toBe(`/api/v1/agents/ag-1/avatar?workspace_id=${WS}`)
  })

  it("escapes a workspace id that needs it", async () => {
    mockFetch.mockImplementation(ok)
    await queueAvatarBackfill("ag/1", "alice", "thumbs", "ws 1&2")

    const [url] = mockFetch.mock.calls[0]
    expect(String(url)).toBe("/api/v1/agents/ag%2F1/avatar?workspace_id=ws%201%262")
  })

  // The read side already refuses to hand out a URL it knows will 400
  // (agentAvatarURL in internal/api/agents_avatar.go). The write side now
  // applies the same rule: no workspace, no request.
  it("makes no request at all without a workspace id", async () => {
    mockFetch.mockImplementation(ok)
    await queueAvatarBackfill("ag-1", "alice", "thumbs", null)
    await queueAvatarBackfill("ag-2", "alice", "thumbs", undefined)
    await queueAvatarBackfill("ag-3", "alice", "thumbs", "")
    expect(mockFetch).not.toHaveBeenCalled()
  })

  // ...and skipping does not burn the agent's one attempt: the workspace store
  // resolves asynchronously, so the first render of a roster can legitimately
  // have no id yet.
  it("leaves the agent retryable once the workspace id arrives", async () => {
    mockFetch.mockImplementation(ok)
    await queueAvatarBackfill("ag-1", "alice", "thumbs", null)
    await queueAvatarBackfill("ag-1", "alice", "thumbs", WS)

    // Exactly one write, and it is the one that carries the workspace — not
    // the workspace-less attempt with the second call swallowed by `attempted`.
    expect(mockFetch).toHaveBeenCalledTimes(1)
    expect(String(mockFetch.mock.calls[0][0])).toBe(
      `/api/v1/agents/ag-1/avatar?workspace_id=${WS}`,
    )
  })

  // #2196, second half: a 400 is not a refusal, and refunding the budget for
  // it meant a permanently-broken endpoint never spent any budget — so a
  // roster re-attempted the FULL per-load allowance on every load, forever.
  // A run of non-403 failures has to stop the same way a run of 403s does.
  it("stops re-attempting after a run of failed writes", async () => {
    mockFetch.mockResolvedValue({
      ok: false,
      status: 400,
      json: () => Promise.resolve({ error: "workspace_id is required" }),
    } as unknown as Response)
    const budget = avatarBackfillBudget()
    for (let i = 0; i < budget + 15; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs", WS)
    }
    expect(mockFetch).toHaveBeenCalledTimes(MAX_CONSECUTIVE_FAILURES_FOR_TEST)
  })

  // A single blip in a healthy session is not a broken endpoint.
  it("keeps going when a failure is followed by a success", async () => {
    let call = 0
    mockFetch.mockImplementation(() => {
      call++
      return Promise.resolve(
        call % 2 === 1 ? ({ ok: false, status: 500 } as Response) : ({ ok: true, status: 200 } as Response),
      )
    })
    for (let i = 0; i < 10; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs", WS)
    }
    expect(mockFetch).toHaveBeenCalledTimes(10)
  })

  // Same reasoning for a transport failure: an endpoint that throws for every
  // agent must not be asked once per avatar on the page.
  it("stops re-attempting after a run of network errors", async () => {
    mockFetch.mockRejectedValue(new Error("offline"))
    for (let i = 0; i < 20; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs", WS)
    }
    expect(mockFetch).toHaveBeenCalledTimes(MAX_CONSECUTIVE_FAILURES_FOR_TEST)
  })

  // The latch is not evidence for the budget half of #2196: the two "stops
  // re-attempting" cases above both settle at MAX_CONSECUTIVE_FAILURES, which
  // trips identically whether a failure spends its budget or refunds it —
  // restoring the refund leaves them green. Interleaving successes keeps the
  // failure run permanently below the latch, so the budget is the only thing
  // left that can stop the loop: one call per attempt until it runs out.
  //
  // The number is the whole point. Spending gives `budget` calls; refunding
  // means only the successes cost anything, so it takes twice as many calls to
  // exhaust the same budget.
  it("spends the budget on a failed write, not only on a stored one", async () => {
    let call = 0
    mockFetch.mockImplementation(() => {
      call++
      const stored = call % 2 === 0
      return Promise.resolve({ ok: stored, status: stored ? 200 : 500 } as Response)
    })
    const budget = avatarBackfillBudget()
    for (let i = 0; i < budget * 4; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs", WS)
    }
    expect(mockFetch).toHaveBeenCalledTimes(budget)
  })

  // Same discrimination for the other refund the catch block used to make.
  // A transport error costs a request like any other, so it spends too.
  it("spends the budget on a transport error as well", async () => {
    let call = 0
    mockFetch.mockImplementation(() => {
      call++
      return call % 2 === 0
        ? Promise.resolve({ ok: true, status: 200 } as Response)
        : Promise.reject(new Error("offline"))
    })
    const budget = avatarBackfillBudget()
    for (let i = 0; i < budget * 4; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs", WS)
    }
    expect(mockFetch).toHaveBeenCalledTimes(budget)
  })

  it("does nothing without an agent id", async () => {
    mockFetch.mockImplementation(ok)
    await queueAvatarBackfill("", "alice", "thumbs", WS)
    expect(mockFetch).not.toHaveBeenCalled()
  })
})

// #2203 — apiFetch synthesizes a 503 whenever the token-refresh endpoint is
// transiently unavailable (lib/api-fetch.ts ~195-209): a 5xx, a network
// throw, or its own 10s abort. That is a routine event during an API restart
// or a deploy window, not evidence the avatar endpoint itself is broken — so
// a run of them must not cost the SAME permanent latch a run of 400s earns.
// #2199's own review found the latch masks the budget in every test that
// trips it, so these assert the request COUNT at each stage rather than only
// "stopped vs not stopped".
describe("queueAvatarBackfill — failure-class discrimination (#2203)", () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("stops for a run of 503s, then resumes for brand-new agents once the transient window elapses", async () => {
    mockFetch.mockResolvedValue({ ok: false, status: 503 } as Response)
    for (let i = 0; i < 5; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs", WS)
    }
    expect(mockFetch).toHaveBeenCalledTimes(5)

    // Backend "recovers" immediately, but inside the same minute the run of
    // 503s must still hold the rail shut — this has to be a window, not a
    // fluke pass on the very next call.
    mockFetch.mockClear()
    mockFetch.mockImplementation(ok)
    for (let i = 5; i < 25; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs", WS)
    }
    expect(mockFetch).not.toHaveBeenCalled()

    // The window elapses — new agents (never a retry of the ones already
    // marked attempted) must be allowed through again, unlike a #2196-style
    // permanent stop.
    const future = Date.now() + 61_000
    vi.spyOn(Date, "now").mockReturnValue(future)
    const budget = avatarBackfillBudget()
    for (let i = 25; i < 25 + budget + 5; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs", WS)
    }
    // #2199's spend-on-failure rule still applies to the 5 transient
    // failures above, so only the remainder of the per-load budget is left
    // for this stage — the point is that it is nonzero and requests resume,
    // not that the whole budget rolls over.
    expect(mockFetch).toHaveBeenCalledTimes(budget - 5)
  })

  // A network throw is the other transient path (offline, aborted
  // navigation, server restart) and gets the same time-based treatment.
  it("stops for a run of transport errors, then resumes once the transient window elapses", async () => {
    mockFetch.mockRejectedValue(new Error("offline"))
    for (let i = 0; i < 5; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs", WS)
    }
    expect(mockFetch).toHaveBeenCalledTimes(5)

    vi.spyOn(Date, "now").mockReturnValue(Date.now() + 61_000)
    mockFetch.mockClear()
    mockFetch.mockImplementation(ok)
    await queueAvatarBackfill("ag-new", "seed-new", "thumbs", WS)
    expect(mockFetch).toHaveBeenCalledTimes(1)
  })

  // The 400 case is the one #2196 needed: the endpoint itself is broken, and
  // the stop must stay permanent — the transient window elapsing must not
  // quietly reopen it.
  it("keeps a run of 400s permanently stopped even after the transient window would have elapsed", async () => {
    mockFetch.mockResolvedValue({ ok: false, status: 400 } as Response)
    for (let i = 0; i < 5; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs", WS)
    }
    expect(mockFetch).toHaveBeenCalledTimes(5)

    vi.spyOn(Date, "now").mockReturnValue(Date.now() + 120_000)
    mockFetch.mockClear()
    for (let i = 5; i < 25; i++) {
      await queueAvatarBackfill(`ag-${i}`, `seed-${i}`, "thumbs", WS)
    }
    expect(mockFetch).not.toHaveBeenCalled()
  })
})
