import { describe, it, expect, beforeEach, vi, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react"
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
