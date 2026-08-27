import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"

// =============================================================================
// The thirteenth agent, and the draft it used to open on top of a real history.
//
// The fan-out that builds the conversation list is capped at AGENT_FANOUT_CAP
// (12) requests. The page this replaced could afford that cap because
// `/chat/<agent>` fetched its own agent's sessions separately and asked the
// fan-out to skip it — the agent you were looking at was never the one dropped.
//
// The rewrite deleted that second fetch and made the fan-out the only source,
// but kept the cap and stopped naming the agent. So for agent 13+ the page got
// `threadsByAgent[id] === undefined`, which it cannot tell apart from "this
// agent has no conversations" — and its auto-open branch mints a NEW draft in
// that case. `crewship open <agent>` and every crews/dashboard "Open chat" link
// build exactly that URL.
//
// `ensureSlug` sorts the named agent to the front before the slice, so the cap
// can drop anything except the agent that was asked for.
// =============================================================================

const apiFetchMock = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetchMock(...args),
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1", loading: false }),
}))

import { useChatTreeData, AGENT_FANOUT_CAP } from "../chat-tree-data"

/** Twenty agents, newest first — the order `GET /agents` returns. */
const roster = Array.from({ length: 20 }, (_, i) => ({
  id: `agent-${i + 1}`,
  name: `Agent ${i + 1}`,
  slug: `agent-${i + 1}`,
  status: "IDLE",
}))

/** The agent that is comfortably past the cap. */
const LATE = roster[16] // agent-17

function ok(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) } as Response)
}

/** Which agent ids the fan-out actually asked about. */
function askedAgentIds(): string[] {
  return apiFetchMock.mock.calls
    .map((c) => String(c[0]))
    .map((u) => /\/api\/v1\/agents\/([^/]+)\/chats/.exec(u)?.[1])
    .filter((v): v is string => !!v)
}

describe("useChatTreeData — the capped fan-out must not drop the agent the URL named", () => {
  beforeEach(() => {
    apiFetchMock.mockReset()
    apiFetchMock.mockImplementation((url: string) => {
      const u = String(url)
      const m = /\/api\/v1\/agents\/([^/]+)\/chats/.exec(u)
      if (m) {
        // Every agent has exactly one conversation, including the late one.
        return ok([
          {
            id: `sess-${m[1]}`,
            title: null,
            status: "ACTIVE",
            message_count: 2,
            started_at: "2026-08-20T10:00:00Z",
            last_activity_at: "2026-08-20T10:00:00Z",
          },
        ])
      }
      if (/\/api\/v1\/agents\?/.test(u)) return ok(roster)
      return ok({})
    })
  })

  afterEach(() => vi.restoreAllMocks())

  it("still asks about no more than the cap", async () => {
    const { result } = renderHook(() => useChatTreeData({ ensureSlug: LATE.slug }))
    await waitFor(() => expect(result.current.threadsLoaded).toBe(true))
    expect(askedAgentIds()).toHaveLength(AGENT_FANOUT_CAP)
  })

  it("fetches the named agent's conversations even though it sits past the cap", async () => {
    const { result } = renderHook(() => useChatTreeData({ ensureSlug: LATE.slug }))
    await waitFor(() => expect(result.current.threadsLoaded).toBe(true))

    expect(askedAgentIds()).toContain(LATE.id)
    // The page's auto-open branch reads exactly this. Undefined here is what
    // made it mint a draft over a real conversation.
    expect(result.current.threadsByAgent[LATE.id]).toHaveLength(1)
  })

  it("drops it when nobody named it — the cap is still a cap", async () => {
    const { result } = renderHook(() => useChatTreeData())
    await waitFor(() => expect(result.current.threadsLoaded).toBe(true))

    expect(askedAgentIds()).not.toContain(LATE.id)
    expect(result.current.threadsByAgent[LATE.id]).toBeUndefined()
  })

  it("keeps the roster's own order for everyone else", async () => {
    const { result } = renderHook(() => useChatTreeData({ ensureSlug: LATE.slug }))
    await waitFor(() => expect(result.current.threadsLoaded).toBe(true))

    // The named agent is promoted to the front; the remaining eleven are the
    // first eleven of the roster, still in creation-recency order.
    expect(askedAgentIds()).toEqual([
      LATE.id,
      ...roster.slice(0, AGENT_FANOUT_CAP - 1).map((a) => a.id),
    ])
  })

  it("leaves the listed roster untouched — promotion orders the fetch, not the column", async () => {
    const { result } = renderHook(() => useChatTreeData({ ensureSlug: LATE.slug }))
    await waitFor(() => expect(result.current.threadsLoaded).toBe(true))

    expect(result.current.agents?.map((a) => a.id)).toEqual(roster.map((a) => a.id))
  })
})
