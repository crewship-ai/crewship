import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor, fireEvent, cleanup } from "@testing-library/react"

// =============================================================================
// /chat/<agent> is the focused view of the tree.
//
// Seven agents, six of them with no threads and a role line each, is a lot of
// furniture around one conversation. The route already knows which agent is in
// hand, so the tree reads it rather than growing a mode of its own:
//
//   /chat          → every agent
//   /chat/<slug>   → that agent, expanded, with its conversations
//
// and the way back to everybody is a row in the tree, not a browser button.
// Nothing here may cost a request: focusing is a render, and the deep link
// chatnotify emits (/chat/<slug>?session=<id>) has to keep landing where it
// always did.
// =============================================================================

let searchParams = new URLSearchParams()
const mockPush = vi.fn()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn(), push: mockPush, back: vi.fn(), forward: vi.fn(), prefetch: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => searchParams,
  useParams: () => ({}),
  usePathname: () => "/",
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: ({ seed }: { seed?: string }) => <span data-testid="agent-avatar" data-seed={seed} />,
}))

vi.mock("@/components/features/chat/sessions-sidebar", () => ({
  SessionsSidebar: () => <div data-testid="sessions-sidebar" />,
}))

// The real panel opens a WebSocket per mount; this stands in for it so the
// assertions can say which session is on screen.
vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: ({ sessionId }: { sessionId: string }) => (
    <div data-testid="chat-panel" data-session-id={sessionId} />
  ),
}))

import { ChatPageClient } from "../chat-page-client"

const agents = [
  {
    id: "agent-1", name: "Filip", slug: "filip", status: "IDLE", role_title: "Data Analyst",
    avatar_seed: "filip", avatar_style: null, crew_id: "crew-1",
    crew: { name: "Research", slug: "research", avatar_style: null },
  },
  {
    id: "agent-2", name: "Bob", slug: "bob", status: "IDLE", role_title: "Engineer",
    avatar_seed: "bob", avatar_style: null, crew_id: "crew-1",
  },
]

function minutesAgo(m: number): string {
  return new Date(Date.now() - m * 60_000).toISOString()
}

// Filip's own list, handed over oldest-active first on purpose: "the newest
// conversation" is a question about activity, not about the order a response
// happened to arrive in.
const sessions = [
  { id: "sess-old", title: "Last month", status: "ACTIVE", message_count: 3, started_at: minutesAgo(4000), ended_at: null, last_activity_at: minutesAgo(4000), origin: "UI" },
  { id: "sess-new", title: "This morning", status: "ACTIVE", message_count: 9, started_at: minutesAgo(9000), ended_at: null, last_activity_at: minutesAgo(3), origin: "UI" },
]

function mountDesktop(search = "") {
  Object.defineProperty(window, "innerWidth", { value: 1280, writable: true, configurable: true })
  vi.stubGlobal("matchMedia", vi.fn(() => ({
    matches: false,
    addEventListener: () => {},
    removeEventListener: () => {},
  })))
  Object.defineProperty(window, "location", {
    configurable: true,
    writable: true,
    value: { ...window.location, pathname: "/chat/filip", search },
  })
  global.fetch = vi.fn((url: string) => {
    const u = String(url)
    if (u.includes("/agent-1/chats")) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(sessions) }) as unknown as Promise<Response>
    }
    if (u.includes("/chats")) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve([]) }) as unknown as Promise<Response>
    }
    if (u.includes("/api/v1/agents")) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(agents) }) as unknown as Promise<Response>
    }
    return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}) }) as unknown as Promise<Response>
  }) as unknown as typeof fetch
}

describe("<ChatPageClient> — the tree focuses on the agent in the URL", () => {
  beforeEach(() => {
    searchParams = new URLSearchParams()
    mockPush.mockReset()
    mountDesktop()
  })
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it("lists only this agent, and the way back returns to every agent", async () => {
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    expect(await screen.findByTestId("chat-tree-agent-filip")).toBeInTheDocument()
    expect(screen.queryByTestId("chat-tree-agent-bob")).toBeNull()
    // Its conversations are what the focus is for.
    expect(await screen.findByTestId("chat-tree-thread-sess-new")).toBeInTheDocument()

    fireEvent.click(screen.getByTestId("chat-tree-all-agents"))
    // /chat is the route that means "everyone" — no second piece of state.
    expect(mockPush).toHaveBeenCalledWith("/chat")
  })

  it("clicking the open agent returns to its most recently ACTIVE conversation", async () => {
    searchParams = new URLSearchParams("session=sess-old")
    mountDesktop("?session=sess-old")

    render(<ChatPageClient />)
    await waitFor(() =>
      expect(screen.getByTestId("chat-panel")).toHaveAttribute("data-session-id", "sess-old"),
    )

    fireEvent.click(await screen.findByTestId("chat-tree-agent-filip"))

    // Not "the first row the server sent" — sess-new is older by start date
    // and newer by activity, which is the ordering every other list here uses.
    await waitFor(() =>
      expect(screen.getByTestId("chat-panel")).toHaveAttribute("data-session-id", "sess-new"),
    )
    // Same agent, same page: a route change would remount the panel and its
    // socket for a conversation that is already on screen.
    expect(mockPush).not.toHaveBeenCalled()
  })

  it("still opens the session a deep link names, focused view and all", async () => {
    searchParams = new URLSearchParams("session=sess-old")
    mountDesktop("?session=sess-old")

    render(<ChatPageClient />)

    // internal/chatnotify emits exactly this shape.
    await waitFor(() =>
      expect(screen.getByTestId("chat-panel")).toHaveAttribute("data-session-id", "sess-old"),
    )
    expect(await screen.findByTestId("chat-tree-agent-filip")).toBeInTheDocument()
    expect(screen.queryByTestId("chat-tree-agent-bob")).toBeNull()
  })

  it("reaches another agent through the search, and that is a route change", async () => {
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    fireEvent.change(screen.getByRole("textbox", { name: /search/i }), { target: { value: "bob" } })
    fireEvent.click(await screen.findByTestId("chat-tree-agent-bob"))

    // The slug is the path segment, so another agent is a navigation — and it
    // carries no session, because that page decides which conversation opens.
    expect(mockPush).toHaveBeenCalledWith("/chat/bob")
  })
})
