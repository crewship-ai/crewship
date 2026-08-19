import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

// =============================================================================
// Somebody else's navigation.
//
// This page owns its own URL writes (history.pushState + popstate — see the
// docstring on selectSession: a router-driven param change remounts the
// dashboard chrome on a static-export build). What it does not own is a
// navigation that arrives from OUTSIDE it: ⌘K's Conversations group opens a
// hit with router.push("/chat/<slug>?session=<id>"), and a router.push fires
// no popstate and does not remount a segment whose only change is the query
// string. Reading the URL back is not routing, so the bypass is untouched —
// the page still never calls the router itself.
// =============================================================================

const mockReplace = vi.fn()
const mockPush = vi.fn()

// The live URL, shared by the navigation mocks and by window.location — the
// two things the page reads. A router.push moves both at once, which is what
// `navigateTo` below stands in for.
let currentPathname = "/chat/filip"
let currentSearch = "?session=session-1"

vi.mock("next/navigation", () => ({
  useRouter: () => ({
    replace: mockReplace,
    push: mockPush,
    back: vi.fn(),
    forward: vi.fn(),
    prefetch: vi.fn(),
    refresh: vi.fn(),
  }),
  // Deliberately a fresh object per call: Next memoises this per navigation,
  // and nothing this page does may depend on that identity holding.
  useSearchParams: () => new URLSearchParams(currentSearch),
  useParams: () => ({}),
  usePathname: () => currentPathname,
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: ({ agentId, sessionId }: { agentId: string; sessionId: string }) => (
    <div data-testid="chat-panel" data-agent-id={agentId} data-session-id={sessionId}>
      ChatPanel mock
    </div>
  ),
}))

import { ChatPageClient } from "../chat-page-client"

const mockAgents = [
  {
    id: "agent-1",
    name: "Filip",
    slug: "filip",
    status: "IDLE",
    role_title: "Data Analyst",
    avatar_seed: "filip",
    avatar_style: null,
    crew: { name: "Research", slug: "research", avatar_style: null },
  },
  {
    id: "agent-2",
    name: "Nina",
    slug: "nina",
    status: "IDLE",
    role_title: "Bookkeeper",
    avatar_seed: "nina",
    avatar_style: null,
    crew: { name: "Research", slug: "research", avatar_style: null },
  },
]

const sessionRow = (id: string) => ({
  id,
  title: null,
  status: "ACTIVE",
  message_count: 3,
  started_at: new Date().toISOString(),
  ended_at: null,
  origin: "UI",
})

function setUrl(pathname: string, search: string) {
  currentPathname = pathname
  currentSearch = search
  Object.defineProperty(window, "location", {
    configurable: true,
    writable: true,
    value: { ...window.location, pathname, search },
  })
}

describe("a ⌘K hit opens, even in the chat that is already on screen", () => {
  beforeEach(() => {
    mockReplace.mockClear()
    mockPush.mockClear()
    setUrl("/chat/filip", "?session=session-1")

    global.fetch = vi.fn((url: string) => {
      const u = String(url)
      if (u.includes("/api/v1/agents") && !u.includes("/chats")) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(mockAgents) })
      }
      if (u.includes("/chats")) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([sessionRow("session-1"), sessionRow("session-2")]),
        })
      }
      return Promise.resolve({ ok: true, json: () => Promise.resolve({}) })
    }) as unknown as typeof fetch
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  const panel = () => screen.getByTestId("chat-panel")

  it("switches conversation when the hit is in the agent already open", async () => {
    const { rerender } = render(<ChatPageClient />)
    await waitFor(() => expect(panel()).toHaveAttribute("data-session-id", "session-1"))

    // What the palette does: router.push to the same route with a different
    // ?session=. No popstate, no remount — only the URL and a re-render.
    setUrl("/chat/filip", "?session=session-2")
    rerender(<ChatPageClient />)

    await waitFor(() => expect(panel()).toHaveAttribute("data-session-id", "session-2"))
    // The conversation moved without this page navigating: the bypass holds.
    expect(mockReplace).not.toHaveBeenCalled()
    expect(mockPush).not.toHaveBeenCalled()
  })

  it("follows the hit into another agent rather than opening its thread here", async () => {
    const { rerender } = render(<ChatPageClient />)
    await waitFor(() => expect(panel()).toHaveAttribute("data-agent-id", "agent-1"))

    setUrl("/chat/nina", "?session=session-9")
    rerender(<ChatPageClient />)

    await waitFor(() => expect(panel()).toHaveAttribute("data-session-id", "session-9"))
    // A thread belongs to the agent it was filed under. Opening Nina's
    // conversation against Filip's id would send the next message to Filip.
    expect(panel()).toHaveAttribute("data-agent-id", "agent-2")
  })

  it("leaves the open conversation alone when the URL has not moved", async () => {
    const { rerender } = render(<ChatPageClient />)
    await waitFor(() => expect(panel()).toHaveAttribute("data-session-id", "session-1"))

    // A re-render for any other reason must not re-apply the URL over a
    // selection the page has already made through its own pushState path.
    rerender(<ChatPageClient />)
    rerender(<ChatPageClient />)
    expect(panel()).toHaveAttribute("data-session-id", "session-1")
  })
})
