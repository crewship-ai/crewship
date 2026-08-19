import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// =============================================================================
// The chat page was the only layout in the product that never asked how wide
// the screen was.
//
// ChatPanel has had a complete mobile mode since it was written — `mobilePanel`
// with a full-screen chat / files / more branch and a `variant="mobile"`
// composer — and nothing ever passed the prop. Meanwhile this page hardcoded
// `gridTemplateColumns: "240px 1fr"`, so on a 390px phone the session list ate
// 60% of the width and the conversation lived in what was left. Every other
// layout branches on useIsMobile(); crews-layout.tsx is the reference — grid
// collapses to "1fr" and the explorer becomes an overlay drawer — and this
// page now does the same thing rather than inventing a second pattern.
//
// Also covered here: the two props the page never passed to ChatPanel.
// `suggested_prompts` (the agent's own chips) had nowhere to arrive, and
// `agentRole` had never been passed AT ALL, so getSuggestions() saw undefined
// and every agent in the product showed the `default` chip pack regardless of
// what it does for a living.
// =============================================================================

let searchParams = new URLSearchParams()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn(), push: vi.fn(), back: vi.fn(), forward: vi.fn(), prefetch: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => searchParams,
  useParams: () => ({}),
  usePathname: () => "/",
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

vi.mock("@/components/features/chat/sessions-sidebar", () => ({
  SessionsSidebar: ({ activeSessionId }: { activeSessionId: string | null }) => (
    <div data-testid="sessions-sidebar" data-active={activeSessionId ?? ""}>sidebar</div>
  ),
}))

// ChatPanel is mocked down to the props under test — the real one opens a
// WebSocket and mounts the whole turn pipeline.
vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: ({ mobilePanel, agentRole, suggestedPrompts }: {
    mobilePanel?: string
    agentRole?: string | null
    suggestedPrompts?: string | null
  }) => (
    <div
      data-testid="chat-panel"
      data-mobile-panel={mobilePanel ?? "(undefined)"}
      data-agent-role={agentRole ?? "(null)"}
      data-suggested-prompts={suggestedPrompts ?? "(null)"}
    />
  ),
}))

import { ChatPageClient } from "../chat-page-client"
import { getSuggestions } from "@/lib/agent-suggestions"

const agent = {
  id: "agent-1",
  name: "Filip",
  slug: "filip",
  status: "IDLE",
  role_title: "Data Analyst",
  agent_role: "AGENT",
  avatar_seed: "filip",
  avatar_style: null,
  suggested_prompts: null as string | null,
  crew: { name: "Research", slug: "research", avatar_style: null },
}

const sessions = [
  { id: "sess-1", title: "Yesterday", status: "ACTIVE", message_count: 3, started_at: new Date().toISOString(), ended_at: null, origin: "UI" },
]

let mqlListeners: Array<() => void> = []

function setViewport(width: number) {
  Object.defineProperty(window, "innerWidth", { value: width, writable: true, configurable: true })
  for (const l of mqlListeners) l()
}

function grid(): HTMLElement {
  return screen.getByTestId("chat-layout-grid")
}

function panelProp(name: string): string | null {
  return screen.getByTestId("chat-panel").getAttribute(name)
}

describe("<ChatPageClient> — mobile layout", () => {
  beforeEach(() => {
    searchParams = new URLSearchParams()
    mqlListeners = []
    vi.stubGlobal("matchMedia", vi.fn(() => ({
      matches: window.innerWidth < 768,
      addEventListener: (_e: string, h: () => void) => { mqlListeners.push(h) },
      removeEventListener: (_e: string, h: () => void) => { mqlListeners = mqlListeners.filter((x) => x !== h) },
    })))
    Object.defineProperty(window, "location", {
      configurable: true,
      writable: true,
      value: { ...window.location, pathname: "/chat/filip", search: "" },
    })
    global.fetch = vi.fn((url: string) => {
      const u = String(url)
      if (u.includes("/chats")) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(sessions) }) as unknown as Promise<Response>
      }
      if (u.includes("/api/v1/agents")) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([agent]) }) as unknown as Promise<Response>
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}) }) as unknown as Promise<Response>
    }) as unknown as typeof fetch
  })

  afterEach(() => {
    vi.restoreAllMocks()
    agent.suggested_prompts = null
  })

  // The only assertion in this file that had to move when the agent tree
  // landed. It used to read "keeps today's fixed 240px session column": 240px
  // was this page's own number, and the shared sidebar-kit width is 280. The
  // mobile cases below are untouched — the drawer and the tab strip are
  // exactly the behaviour the tree steps aside for.
  it("desktop (1280px) gives the shared 280px tree its own column", async () => {
    setViewport(1280)
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    expect(grid().style.gridTemplateColumns).toBe("280px 1fr")
    expect(screen.getByTestId("chat-tree-sidebar")).toBeInTheDocument()
    // The flat session list is the phone drawer now, and nothing else.
    expect(screen.queryByTestId("sessions-sidebar")).not.toBeInTheDocument()
    // Desktop mode is signalled by the ABSENCE of mobilePanel — that is what
    // makes ChatPanel fall through to its split view.
    expect(panelProp("data-mobile-panel")).toBe("(undefined)")
    expect(screen.queryByRole("tablist", { name: /panel/i })).not.toBeInTheDocument()
  })

  it("mobile (390px) does not render the fixed 240px grid", async () => {
    setViewport(390)
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    expect(grid().style.gridTemplateColumns).not.toBe("240px 1fr")
    expect(grid().style.gridTemplateColumns).toBe("1fr")
  })

  it("mobile opens on the chat panel, with the session list behind a control", async () => {
    setViewport(390)
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    // The page is a chat page: the conversation is what the user came for.
    expect(panelProp("data-mobile-panel")).toBe("chat")
    // The 240px column is gone, so the session list must be reachable.
    expect(screen.queryByTestId("sessions-sidebar")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /sessions/i }))
    expect(screen.getByTestId("sessions-sidebar")).toBeInTheDocument()
  })

  it("mobile reaches the other panels through a tab strip", async () => {
    setViewport(390)
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    fireEvent.click(screen.getByRole("tab", { name: /files/i }))
    expect(panelProp("data-mobile-panel")).toBe("files")

    fireEvent.click(screen.getByRole("tab", { name: /more/i }))
    expect(panelProp("data-mobile-panel")).toBe("more")

    fireEvent.click(screen.getByRole("tab", { name: /chat/i }))
    expect(panelProp("data-mobile-panel")).toBe("chat")

    // One ChatPanel throughout. Each mounted panel opens its own WebSocket
    // (chat-panel.tsx), so a second one on screen — or a per-panel instance
    // per tab — would be a second socket per agent.
    expect(screen.getAllByTestId("chat-panel")).toHaveLength(1)
  })
})

describe("<ChatPageClient> — per-agent suggestions reach ChatPanel", () => {
  beforeEach(() => {
    searchParams = new URLSearchParams()
    mqlListeners = []
    vi.stubGlobal("matchMedia", vi.fn(() => ({
      matches: false,
      addEventListener: (_e: string, h: () => void) => { mqlListeners.push(h) },
      removeEventListener: () => {},
    })))
    Object.defineProperty(window, "innerWidth", { value: 1280, writable: true, configurable: true })
    Object.defineProperty(window, "location", {
      configurable: true,
      writable: true,
      value: { ...window.location, pathname: "/chat/filip", search: "" },
    })
    global.fetch = vi.fn((url: string) => {
      const u = String(url)
      if (u.includes("/chats")) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(sessions) }) as unknown as Promise<Response>
      }
      if (u.includes("/api/v1/agents")) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([agent]) }) as unknown as Promise<Response>
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}) }) as unknown as Promise<Response>
    }) as unknown as typeof fetch
  })

  afterEach(() => {
    vi.restoreAllMocks()
    agent.suggested_prompts = null
  })

  it("passes the agent's own suggested_prompts through", async () => {
    agent.suggested_prompts = "What changed this week?\nDraft the Monday summary"
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    expect(panelProp("data-suggested-prompts")).toBe("What changed this week?\nDraft the Monday summary")
    // …and that is what the chips resolve to.
    expect(getSuggestions(panelProp("data-agent-role"), panelProp("data-suggested-prompts")).empty)
      .toEqual(["What changed this week?", "Draft the Monday summary"])
  })

  it("passes role_title as the role, so an unconfigured agent gets its ROLE pack — not `default`", async () => {
    agent.suggested_prompts = null
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    // role_title is the field whose values map onto the pack keys:
    // getSuggestions lowercases and underscores it, so "Data Analyst" resolves
    // to `data_analyst`. agent_role would not — its whole value space is
    // {AGENT, LEAD} (internal/api/agents.go:191), neither of which is a pack.
    expect(panelProp("data-agent-role")).toBe("Data Analyst")
    expect(panelProp("data-suggested-prompts")).toBe("(null)")

    const pack = getSuggestions(panelProp("data-agent-role"), null)
    expect(pack.empty).toEqual(getSuggestions("Data Analyst").empty)
    expect(pack.empty).not.toEqual(getSuggestions(null).empty)
    expect(pack.empty[0]).toBe("Explore the latest dataset")
  })
})
