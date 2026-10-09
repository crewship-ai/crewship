import { beforeEach, describe, expect, it, vi } from "vitest"

import { apiFetch } from "@/lib/api-fetch"
import {
  avatarInputs,
  backfillAgentAvatars,
  resolveStoredAvatarSrc,
  storeAgentAvatar,
} from "@/lib/agent-avatar-persist"

/** The workspace the writes are scoped to. */
const WS = "ws-1"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

const h = vi.hoisted(() => ({
  authMode: "cookie" as "cookie" | "bearer",
  /** Styles whose collection is "still loading" until preloadAvatarStyle resolves. */
  loaded: new Set<string>(["bottts-neutral"]),
  /** Make preload a no-op that never makes the style resident. */
  preloadFails: false,
}))

vi.mock("@/lib/server-base", () => ({
  getAuthMode: () => h.authMode,
  withServerBase: (p: string) => "https://server.test" + p,
}))

// A real avatar is a multi-KB SVG; the shape is all these tests need. The
// lazy-loading contract is modelled: getAgentAvatarSVG returns null until
// preloadAvatarStyle has made the style resident.
vi.mock("@/lib/agent-avatar", () => ({
  DEFAULT_AVATAR_STYLE: "bottts-neutral",
  AVATAR_STYLES: { "bottts-neutral": {}, lorelei: {}, thumbs: {} },
  preloadAvatarStyle: async (style: string) => {
    if (!h.preloadFails) h.loaded.add(style)
  },
  getAgentAvatarSVG: (seed: string, style: string) =>
    h.loaded.has(style) ? `<svg xmlns="http://www.w3.org/2000/svg" data-seed="${seed}" data-style="${style}"/>` : null,
}))

const mockFetch = vi.mocked(apiFetch)

function status(code: number, body: unknown = {}, headers: Record<string, string> = {}) {
  return Promise.resolve(
    new Response(JSON.stringify(body), { status: code, headers: { "Content-Type": "application/json", ...headers } }),
  )
}

beforeEach(() => {
  mockFetch.mockReset()
  h.authMode = "cookie"
  h.loaded = new Set(["bottts-neutral"])
  h.preloadFails = false
})

describe("resolveStoredAvatarSrc", () => {
  it("routes the stored URL through the configured server base", () => {
    expect(resolveStoredAvatarSrc("/api/v1/agents/a1/avatar?v=abc")).toBe(
      "https://server.test/api/v1/agents/a1/avatar?v=abc",
    )
  })

  // An <img> request carries no Authorization header, so in bearer mode
  // (desktop shell) the stored avatar would 401 and render broken.
  it("declines the stored URL in bearer mode so the caller generates instead", () => {
    h.authMode = "bearer"
    expect(resolveStoredAvatarSrc("/api/v1/agents/a1/avatar?v=abc")).toBeNull()
  })

  it("returns null when there is nothing stored", () => {
    expect(resolveStoredAvatarSrc(null)).toBeNull()
    expect(resolveStoredAvatarSrc(undefined)).toBeNull()
  })
})

describe("avatarInputs", () => {
  // The stored face must be the one the dashboard draws, or storing it would
  // change what everybody sees.
  it.each([
    ["own seed and style", { id: "a", slug: "s", avatar_seed: "seed", avatar_style: "thumbs" }, undefined, { seed: "seed", style: "thumbs" }],
    ["slug when there is no seed", { id: "a", slug: "s" }, undefined, { seed: "s", style: "bottts-neutral" }],
    ["the crew's style from the row", { id: "a", slug: "s", crew: { avatar_style: "lorelei" } }, undefined, { seed: "s", style: "lorelei" }],
    ["the crew's style passed in", { id: "a", slug: "s" }, "lorelei", { seed: "s", style: "lorelei" }],
    ["own style over the crew's", { id: "a", slug: "s", avatar_style: "thumbs", crew: { avatar_style: "lorelei" } }, undefined, { seed: "s", style: "thumbs" }],
    ["the default for an unknown style", { id: "a", slug: "s", avatar_style: "gone" }, undefined, { seed: "s", style: "bottts-neutral" }],
  ])("uses %s", (_name, agent, crewStyle, want) => {
    expect(avatarInputs(agent, crewStyle)).toEqual(want)
  })
})

describe("storeAgentAvatar", () => {
  it("PUTs the generated render, scoped to the workspace", async () => {
    mockFetch.mockReturnValue(status(200))
    await expect(storeAgentAvatar({ id: "ag/1", slug: "alice" }, "ws 1&2")).resolves.toBe("stored")
    expect(mockFetch).toHaveBeenCalledTimes(1)
    const [url, init] = mockFetch.mock.calls[0]
    expect(url).toBe("/api/v1/agents/ag%2F1/avatar?workspace_id=ws%201%262")
    expect(init?.method).toBe("PUT")
    expect(JSON.parse(String(init?.body))).toEqual({
      svg: '<svg xmlns="http://www.w3.org/2000/svg" data-seed="alice" data-style="bottts-neutral"/>',
    })
  })

  // Write-once on the server: storing the loading placeholder would freeze the
  // wrong face for good, so the store waits for the collection instead.
  it("waits for a lazily loaded style rather than storing a placeholder", async () => {
    mockFetch.mockReturnValue(status(200))
    await expect(storeAgentAvatar({ id: "a1", slug: "alice", avatar_style: "lorelei" }, WS)).resolves.toBe("stored")
    expect(JSON.parse(String(mockFetch.mock.calls[0][1]?.body)).svg).toContain('data-style="lorelei"')
  })

  it("sends nothing when the style never becomes resident", async () => {
    h.preloadFails = true
    await expect(storeAgentAvatar({ id: "a1", slug: "alice", avatar_style: "lorelei" }, WS)).resolves.toBe("failed")
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("sends nothing for an agent that already has a stored render", async () => {
    await expect(storeAgentAvatar({ id: "a1", slug: "alice", avatar_url: "/x" }, WS)).resolves.toBe("present")
    expect(mockFetch).not.toHaveBeenCalled()
  })

  // #2196: the PUT sits behind wsCtx and is refused without a workspace.
  it.each([null, undefined, ""])("sends nothing without a workspace (%s)", async (ws) => {
    await expect(storeAgentAvatar({ id: "a1", slug: "alice" }, ws)).resolves.toBe("failed")
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it.each([
    [409, "present"],
    [403, "refused"],
    [404, "refused"],
    [400, "failed"],
    [503, "failed"],
  ] as const)("maps %i to %s", async (code, outcome) => {
    mockFetch.mockReturnValue(status(code))
    await expect(storeAgentAvatar({ id: "a1", slug: "alice" }, WS)).resolves.toBe(outcome)
  })

  // A render is an optimisation; a save that succeeded must not turn into an
  // error because storing its face did not.
  it("never rejects on a transport error", async () => {
    mockFetch.mockRejectedValue(new TypeError("Failed to fetch"))
    await expect(storeAgentAvatar({ id: "a1", slug: "alice" }, WS)).resolves.toBe("failed")
  })
})

describe("backfillAgentAvatars", () => {
  function route(pages: unknown[][], put: (url: string) => number = () => 200) {
    const total = pages.reduce((n, p) => n + p.length, 0)
    mockFetch.mockImplementation((url, init) => {
      const u = String(url)
      if (init?.method === "PUT") return status(put(u))
      const offset = Number(new URL(u, "https://x").searchParams.get("offset"))
      let seen = 0
      for (const page of pages) {
        if (seen === offset) return status(200, page, { "X-Total-Count": String(total) })
        seen += page.length
      }
      return status(200, [], { "X-Total-Count": String(total) })
    })
  }

  it("stores every agent without a render and counts what happened", async () => {
    route(
      [
        [
          { id: "a1", slug: "a1" },
          { id: "a2", slug: "a2", avatar_url: "/stored" },
        ],
        [
          { id: "a3", slug: "a3" },
          { id: "a4", slug: "a4" },
        ],
      ],
      (url) => (url.includes("/a3/") ? 403 : url.includes("/a4/") ? 500 : 200),
    )
    await expect(backfillAgentAvatars(WS)).resolves.toEqual({ total: 4, stored: 1, present: 1, refused: 1, failed: 1 })
    const puts = mockFetch.mock.calls.filter(([, init]) => init?.method === "PUT").map(([url]) => String(url))
    expect(puts).toEqual([
      "/api/v1/agents/a1/avatar?workspace_id=ws-1",
      "/api/v1/agents/a3/avatar?workspace_id=ws-1",
      "/api/v1/agents/a4/avatar?workspace_id=ws-1",
    ])
  })

  it("limits the list to one crew when asked", async () => {
    route([[]])
    await backfillAgentAvatars(WS, { crewId: "crew 1" })
    expect(String(mockFetch.mock.calls[0][0])).toBe("/api/v1/agents?workspace_id=ws-1&limit=500&offset=0&crew_id=crew+1")
  })

  it("throws when the agent list cannot be read", async () => {
    mockFetch.mockReturnValue(status(403))
    await expect(backfillAgentAvatars(WS)).rejects.toThrow("Agents could not be listed (403)")
  })
})
