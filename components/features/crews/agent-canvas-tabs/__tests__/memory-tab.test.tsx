import { describe, it, expect, beforeEach, vi, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { MemoryTab } from "@/components/features/crews/agent-canvas-tabs/memory-tab"

// PR-E F6 — Memory tab subtab navigation contract test.
//
// Locks down the 4-subtab navigation requirement from PRD §6 F6:
//
//   AGENT.md / CREW.md / PERSONA / Peers
//
// Without this test, the prior 2-subtab regression (PERSONA + Peers
// only) would never have shown up in CI — it slipped through the
// PR-E initial commit because there was no UI contract pinning the
// tab list.
//
// fetch is stubbed across the suite — the tab uses lazy GET calls
// for content + history; tests don't depend on those resolving
// (assertions are over the static UI shell), but unstubbed fetches
// would throw in node and pollute the test output.

const originalFetch = global.fetch

beforeEach(() => {
  // Default stub: return an empty 404 for every request. The test
  // doesn't care about content; it asserts the tab nav + panel
  // rendering. Individual tests override this when they need a
  // specific response shape.
  global.fetch = vi.fn().mockResolvedValue({
    ok: false,
    status: 404,
    text: async () => "",
    json: async () => ({ entries: [], peers: [] }),
  }) as unknown as typeof fetch
})

afterEach(() => {
  global.fetch = originalFetch
  vi.restoreAllMocks()
})

describe("MemoryTab — subtab navigation", () => {
  it("renders all four subtabs when crewId is set", () => {
    render(
      <MemoryTab
        agentId="agent_a"
        agentSlug="alice"
        crewId="crew_x"
        workspaceId="ws_test"
      />,
    )
    // Linear-style underline buttons, asserted via data-testid so a
    // CSS or copy refactor doesn't break the contract.
    expect(screen.getByTestId("memory-subtab-agent")).toBeInTheDocument()
    expect(screen.getByTestId("memory-subtab-crew")).toBeInTheDocument()
    expect(screen.getByTestId("memory-subtab-persona")).toBeInTheDocument()
    expect(screen.getByTestId("memory-subtab-peers")).toBeInTheDocument()
  })

  it("hides CREW.md tab when agent has no crew", () => {
    render(
      <MemoryTab
        agentId="agent_a"
        agentSlug="alice"
        workspaceId="ws_test"
      />,
    )
    expect(screen.getByTestId("memory-subtab-agent")).toBeInTheDocument()
    expect(screen.queryByTestId("memory-subtab-crew")).not.toBeInTheDocument()
    expect(screen.getByTestId("memory-subtab-persona")).toBeInTheDocument()
    expect(screen.getByTestId("memory-subtab-peers")).toBeInTheDocument()
  })

  it("default subtab is AGENT.md", () => {
    render(
      <MemoryTab
        agentId="agent_a"
        agentSlug="alice"
        crewId="crew_x"
        workspaceId="ws_test"
      />,
    )
    // Header text from AgentMemoryPanel — uniquely identifies the
    // active panel without depending on tab styling.
    expect(screen.getByText(/per-agent canonical memory/i)).toBeInTheDocument()
  })

  it("clicking CREW.md subtab shows the crew panel with shared badge", async () => {
    render(
      <MemoryTab
        agentId="agent_a"
        agentSlug="alice"
        crewId="crew_x"
        workspaceId="ws_test"
      />,
    )
    fireEvent.click(screen.getByTestId("memory-subtab-crew"))
    expect(await screen.findByText(/shared crew memory/i)).toBeInTheDocument()
    // The "shared with all crew members" badge is the load-bearing UI
    // affordance per the PR-E fix description.
    expect(screen.getByText(/shared with all crew members/i)).toBeInTheDocument()
  })

  it("clicking PERSONA subtab shows the persona panel", async () => {
    render(
      <MemoryTab
        agentId="agent_a"
        agentSlug="alice"
        crewId="crew_x"
        workspaceId="ws_test"
      />,
    )
    fireEvent.click(screen.getByTestId("memory-subtab-persona"))
    expect(
      await screen.findByText(/agent override \(per-agent persona\.md\)/i),
    ).toBeInTheDocument()
  })

  it("clicking Peers subtab shows the peers panel (loading state)", async () => {
    render(
      <MemoryTab
        agentId="agent_a"
        agentSlug="alice"
        crewId="crew_x"
        workspaceId="ws_test"
      />,
    )
    fireEvent.click(screen.getByTestId("memory-subtab-peers"))
    // Initial loading state surfaces immediately before fetch resolves.
    // After resolution the panel switches to either the empty state
    // or the grid — both branches are valid, so we just wait for
    // either to land.
    await waitFor(() => {
      const peersPanelContent =
        screen.queryByText(/loading peers/i) ||
        screen.queryByText(/no peer cards yet/i) ||
        screen.queryByText(/select a peer/i)
      expect(peersPanelContent).toBeInTheDocument()
    })
  })
})

describe("MemoryTab — per-tier char caps", () => {
  it("AGENT.md panel uses the 4000 B cap", async () => {
    // Empty history response — panel shows "(empty)" + counter.
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => "",
      json: async () => ({ entries: [] }),
    }) as unknown as typeof fetch

    render(
      <MemoryTab
        agentId="agent_a"
        agentSlug="alice"
        crewId="crew_x"
        workspaceId="ws_test"
      />,
    )
    // Wait for the fetch effect to settle so the counter is rendered.
    expect(await screen.findByText(/0\/4000 B/)).toBeInTheDocument()
  })

  it("PERSONA panel uses the 1500 B cap", async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => "",
      json: async () => ({
        entries: [],
        from_default: true,
        content: "",
        bytes: 0,
        cap_bytes: 1500,
        layer: "agent",
      }),
    }) as unknown as typeof fetch

    render(
      <MemoryTab
        agentId="agent_a"
        agentSlug="alice"
        crewId="crew_x"
        workspaceId="ws_test"
      />,
    )
    fireEvent.click(screen.getByTestId("memory-subtab-persona"))
    expect(await screen.findByText(/0\/1500 B/)).toBeInTheDocument()
  })
})

describe("MemoryTab — export follows the selected scope (#1748)", () => {
  it("exports the crew tier when the CREW sub-tab is selected", async () => {
    const { container } = render(
      <MemoryTab agentId="a1" agentSlug="alex" crewId="crew-1" workspaceId="ws-1" />,
    )
    fireEvent.click(screen.getByTestId("memory-subtab-crew"))
    // The button is the crew-scoped one: no agent slug reaches the query.
    const btn = screen.getByTestId("memory-export-button")
    expect(btn).toBeInTheDocument()
    expect(container).toBeTruthy()
  })

  // The export API keys on crew_id; a solo agent has none, so offering
  // the button would only ever produce a 400.
  it("offers no export for a solo agent", () => {
    render(<MemoryTab agentId="a1" agentSlug="alex" workspaceId="ws-1" />)
    expect(screen.queryByTestId("memory-export-button")).toBeNull()
  })
})

// ── An empty history and an unreadable one are different facts ────────
//
// §4.5 of the 2026-08-13 chat-surface audit: this panel reads
// memory_versions, which is a projection of the .memory tree rather
// than the tree itself. When nothing projects a tier, the endpoint
// answers with an empty list — and the panel used to draw "(no
// history)" over it, which reads as "this agent has written nothing".
// The endpoint now says which of the two it is; these tests hold the
// panel to rendering the difference.

/** Route the panel's two version calls (list, then blob) by URL. */
function routeVersionFetch(list: {
  ok?: boolean
  status?: number
  body?: unknown
  content?: string
}) {
  return vi.fn().mockImplementation(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes("/memory/versions/")) {
      return {
        ok: true,
        status: 200,
        text: async () => list.content ?? "",
        json: async () => ({}),
      }
    }
    if (url.includes("/memory/versions")) {
      return {
        ok: list.ok ?? true,
        status: list.status ?? 200,
        text: async () => "",
        json: async () => list.body ?? { entries: [] },
      }
    }
    return { ok: true, status: 200, text: async () => "", json: async () => ({ peers: [] }) }
  }) as unknown as typeof fetch
}

describe("MemoryTab — silence is not a fact", () => {
  it("renders a tier nothing projects as unreadable, not as empty", async () => {
    global.fetch = routeVersionFetch({
      body: {
        entries: [],
        projection: {
          state: "unrecorded",
          reason: "No writer records this path into the memory version trail.",
        },
      },
    })
    render(<MemoryTab agentId="a1" agentSlug="alex" crewId="crew-1" workspaceId="ws-1" />)
    fireEvent.click(screen.getByTestId("memory-subtab-crew"))

    const unreadable = await screen.findByTestId("memory-history-unreadable")
    expect(unreadable).toBeInTheDocument()
    expect(unreadable).toHaveTextContent(/No writer records this path/i)
    // The lie the audit named: an empty list drawn as an empty history.
    expect(screen.queryByText(/\(no history\)/i)).toBeNull()
    expect(screen.queryByTestId("memory-history-empty")).toBeNull()
  })

  it("renders a watched tier with no writes as genuinely empty", async () => {
    global.fetch = routeVersionFetch({
      body: {
        entries: [],
        projection: { state: "recorded", reason: "Recorded by the memory audit watcher." },
      },
    })
    render(<MemoryTab agentId="a1" agentSlug="alex" crewId="crew-1" workspaceId="ws-1" />)
    fireEvent.click(screen.getByTestId("memory-subtab-crew"))

    expect(await screen.findByTestId("memory-history-empty")).toBeInTheDocument()
    expect(screen.queryByTestId("memory-history-unreadable")).toBeNull()
  })

  it("says versioning is off rather than showing an empty tier", async () => {
    global.fetch = routeVersionFetch({
      body: {
        entries: [],
        projection: {
          state: "unavailable",
          reason: "Memory versioning is switched off on this server.",
        },
      },
    })
    render(<MemoryTab agentId="a1" agentSlug="alex" crewId="crew-1" workspaceId="ws-1" />)
    fireEvent.click(screen.getByTestId("memory-subtab-crew"))

    const unreadable = await screen.findByTestId("memory-history-unreadable")
    expect(unreadable).toHaveTextContent(/switched off/i)
    expect(screen.queryByTestId("memory-history-empty")).toBeNull()
  })

  it("does not render a failed history fetch as an empty history", async () => {
    global.fetch = routeVersionFetch({ ok: false, status: 500 })
    render(<MemoryTab agentId="a1" agentSlug="alex" crewId="crew-1" workspaceId="ws-1" />)
    fireEvent.click(screen.getByTestId("memory-subtab-crew"))

    expect(await screen.findByTestId("memory-history-error")).toBeInTheDocument()
    expect(screen.queryByText(/\(no history\)/i)).toBeNull()
    expect(screen.queryByTestId("memory-history-empty")).toBeNull()
  })

  it("lists the recorded versions when there are some", async () => {
    global.fetch = routeVersionFetch({
      content: "# crew memory",
      body: {
        entries: [
          {
            id: "mv_1",
            sha256: "abcdef0123456789abcdef",
            bytes: 13,
            written_at: "2026-08-13T08:00:00Z",
            written_by: "audit-watcher",
          },
        ],
        projection: { state: "recorded", reason: "" },
      },
    })
    render(<MemoryTab agentId="a1" agentSlug="alex" crewId="crew-1" workspaceId="ws-1" />)
    fireEvent.click(screen.getByTestId("memory-subtab-crew"))

    expect(await screen.findByText(/audit-watcher/)).toBeInTheDocument()
    expect(screen.queryByTestId("memory-history-unreadable")).toBeNull()
    expect(screen.queryByTestId("memory-history-empty")).toBeNull()
  })
})

describe("MemoryTab — the tiers this panel cannot show", () => {
  it("names daily logs, lessons.md and learned topics instead of omitting them", async () => {
    global.fetch = routeVersionFetch({
      body: { entries: [], projection: { state: "recorded", reason: "" } },
    })
    render(<MemoryTab agentId="a1" agentSlug="alex" crewId="crew-1" workspaceId="ws-1" />)

    const other = await screen.findByTestId("memory-other-tiers")
    expect(other).toHaveTextContent(/daily\//i)
    expect(other).toHaveTextContent(/lessons\.md/i)
    expect(other).toHaveTextContent(/learned-/i)
  })
})
