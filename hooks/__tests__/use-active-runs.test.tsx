import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"
import { apiFetch } from "@/lib/api-fetch"
import { useActiveRuns } from "@/hooks/use-active-runs"

// The Activity bar builds a per-row href from two feeds. Agent (chat) runs
// and routine (pipeline) runs must NOT share the same href: an agent run id
// has no pipeline trace, so /activity?run=<id> 404s ("Trace unavailable").
// Agent rows must deep-link to the agent's chat instead (#846).

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(),
}))

// useActiveRuns subscribes to WS events; stub the realtime layer so the hook
// mounts without a websocket provider.
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: () => {},
}))

function okJSON(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    text: async () => JSON.stringify(body),
    json: async () => body,
  } as unknown as Response
}

// Route each mocked call by URL: the agent feed vs the routine feed.
function mockFeeds(agentRows: unknown[], routineRows: unknown[]) {
  vi.mocked(apiFetch).mockImplementation((input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes("/api/v1/runs?")) return Promise.resolve(okJSON({ data: agentRows }))
    if (url.includes("/pipelines/runs/active")) return Promise.resolve(okJSON(routineRows))
    return Promise.resolve(okJSON({}))
  })
}

describe("useActiveRuns href per kind", () => {
  beforeEach(() => {
    vi.mocked(apiFetch).mockReset()
  })

  it("routes agent runs to the agent chat, not the pipeline trace", async () => {
    mockFeeds(
      [{ id: "a1", status: "RUNNING", agent_name: "Casey", agent_slug: "casey" }],
      [],
    )
    const { result } = renderHook(() => useActiveRuns("ws-1"))

    await waitFor(() => expect(result.current.runs.length).toBe(1))
    const agent = result.current.runs.find((r) => r.kind === "agent")
    expect(agent).toBeDefined()
    expect(agent!.href).toBe("/chat/casey")
  })

  it("keeps routine runs on the pipeline trace canvas", async () => {
    mockFeeds([], [{ id: "r1", pipeline_slug: "digest", pipeline_name: "Digest" }])
    const { result } = renderHook(() => useActiveRuns("ws-1"))

    await waitFor(() => expect(result.current.runs.length).toBe(1))
    const routine = result.current.runs.find((r) => r.kind === "routine")
    expect(routine).toBeDefined()
    expect(routine!.href).toBe("/activity?run=r1")
  })

  it("encodes agent slugs that need escaping", async () => {
    mockFeeds(
      [{ id: "a2", status: "RUNNING", agent_name: "Doc Grabber", agent_slug: "sběrač dokladů" }],
      [],
    )
    const { result } = renderHook(() => useActiveRuns("ws-1"))

    await waitFor(() => expect(result.current.runs.length).toBe(1))
    const agent = result.current.runs.find((r) => r.kind === "agent")
    expect(agent!.href).toBe(`/chat/${encodeURIComponent("sběrač dokladů")}`)
  })

  it("falls back to /crews when an agent run has no slug", async () => {
    mockFeeds([{ id: "a3", status: "RUNNING", agent_name: "Nameless" }], [])
    const { result } = renderHook(() => useActiveRuns("ws-1"))

    await waitFor(() => expect(result.current.runs.length).toBe(1))
    const agent = result.current.runs.find((r) => r.kind === "agent")
    // Never /activity?run=<id> (404s for agent ids) and never /chat
    // (no page in the static export) — /crews is a real route.
    expect(agent!.href).toBe("/crews")
  })
})
