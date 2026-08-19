import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor, fireEvent, cleanup } from "@testing-library/react"

// =============================================================================
// Narrowing the tree is a FILTER, and the filter is local.
//
// This replaces chat-tree-focus.test.tsx, which pinned the version the owner
// rejected: clicking an agent navigated to /chat/<slug>, and being on that
// route was what listed that agent alone. It cost no state and it cost a page
// transition per pick — the dashboard chrome tore down and rebuilt every time
// somebody looked at a different name. Nobody can feel a useState; everybody
// feels a transition.
//
// So, on /chat/<slug>:
//
//   the URL names an agent  → every agent is still listed, that one selected
//   click an agent          → the others go, animated
//   click it again          → the others come back, animated
//   either direction        → THE ROUTER IS NOT CALLED. That is the point.
//
// The defect the route-driven version was built to fix stays fixed: an agent
// nobody has talked to must still be reachable as a conversation. It gets a row
// of its own now, because the plain click belongs to the filter — and using it
// on ANOTHER agent swaps this page's agent in place, with history.pushState,
// for the same reason selectSession already does (a router-driven param change
// remounts the whole dashboard subtree on static-export builds).
// =============================================================================

let searchParams = new URLSearchParams()
const mockPush = vi.fn()
const mockReplace = vi.fn()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: mockReplace, push: mockPush, back: vi.fn(), forward: vi.fn(), prefetch: vi.fn(), refresh: vi.fn() }),
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
// assertions can say WHOSE conversation is on screen, and which one.
vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: ({ sessionId, agentId }: { sessionId: string; agentId: string }) => (
    <div data-testid="chat-panel" data-session-id={sessionId} data-agent-id={agentId} />
  ),
}))

import { ChatPageClient } from "../chat-page-client"

const agents = [
  {
    id: "agent-1", name: "Filip", slug: "filip", status: "IDLE", role_title: "Data Analyst",
    avatar_seed: "filip", avatar_style: null, crew_id: "crew-1",
    crew: { name: "Research", slug: "research", avatar_style: null },
  },
  // Nobody has ever talked to Bob. That is the case the whole affordance is for.
  {
    id: "agent-2", name: "Bob", slug: "bob", status: "IDLE", role_title: "Engineer",
    avatar_seed: "bob", avatar_style: null, crew_id: "crew-1",
  },
]

function minutesAgo(m: number): string {
  return new Date(Date.now() - m * 60_000).toISOString()
}

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

/** Every router entry point this page could reach for, in one assertion. */
function expectNoNavigation() {
  expect(mockPush).not.toHaveBeenCalled()
  expect(mockReplace).not.toHaveBeenCalled()
}

describe("<ChatPageClient> — the tree narrows by filter, not by route", () => {
  beforeEach(() => {
    searchParams = new URLSearchParams()
    mockPush.mockReset()
    mockReplace.mockReset()
    mountDesktop()
  })
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it("lists every agent — the URL names one, it does not hide the rest", async () => {
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    // The version this replaces rendered ONLY filip here.
    expect(await screen.findByTestId("chat-tree-agent-filip")).toBeInTheDocument()
    expect(screen.getByTestId("chat-tree-agent-bob")).toBeInTheDocument()
    // …and the agent in the URL is open, so where you are is visible.
    expect(await screen.findByTestId("chat-tree-thread-sess-new")).toBeInTheDocument()
    // Nothing narrows it, so there is nothing to undo — the row that existed
    // only to undo a navigation is gone with the navigation.
    expect(screen.queryByTestId("chat-tree-all-agents")).toBeNull()
  })

  it("clicking an agent hides the others, clicking it again brings them back — with no navigation either way", async () => {
    const pushSpy = vi.spyOn(window.history, "pushState")
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())
    await screen.findByTestId("chat-tree-agent-bob")

    // The page's own arrival wrote ?session= once; the filter must add nothing.
    pushSpy.mockClear()
    mockPush.mockClear()

    fireEvent.click(screen.getByTestId("chat-tree-agent-filip"))
    // Animated out, so it leaves on its own schedule rather than instantly.
    await waitFor(() => expect(screen.queryByTestId("chat-tree-agent-bob")).toBeNull())
    expect(screen.getByTestId("chat-tree-agent-filip")).toBeInTheDocument()

    fireEvent.click(screen.getByTestId("chat-tree-agent-filip"))
    await waitFor(() => expect(screen.getByTestId("chat-tree-agent-bob")).toBeInTheDocument())

    // The whole point: no route change, and not even a URL write. The reader
    // cannot feel the state; they would have felt both of these.
    expectNoNavigation()
    expect(pushSpy).not.toHaveBeenCalled()
  })

  it("keeps the filter while threads are switched inside it", async () => {
    searchParams = new URLSearchParams("session=sess-new")
    mountDesktop("?session=sess-new")

    render(<ChatPageClient />)
    await waitFor(() =>
      expect(screen.getByTestId("chat-panel")).toHaveAttribute("data-session-id", "sess-new"),
    )

    // Nothing has narrowed it yet — being on /chat/filip is not a filter.
    expect(await screen.findByTestId("chat-tree-agent-bob")).toBeInTheDocument()

    fireEvent.click(screen.getByTestId("chat-tree-agent-filip"))
    await waitFor(() => expect(screen.queryByTestId("chat-tree-agent-bob")).toBeNull())

    fireEvent.click(screen.getByTestId("chat-tree-thread-sess-old"))
    await waitFor(() =>
      expect(screen.getByTestId("chat-panel")).toHaveAttribute("data-session-id", "sess-old"),
    )

    // Selecting a conversation is not a request to widen the column back out.
    expect(screen.queryByTestId("chat-tree-agent-bob")).toBeNull()
    expect(screen.getByTestId("chat-tree-agent-filip")).toBeInTheDocument()
  })

  it("offers to start one with an agent that has none, from the keyboard", async () => {
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    // Nothing under Bob, so no chevron and — since the plain click is the
    // filter — nothing the row itself can open. This is the original defect:
    // an inert agent on the surface whose job is to make talking to one easy.
    fireEvent.click(await screen.findByTestId("chat-tree-agent-bob"))

    const start = await screen.findByTestId("chat-tree-start-bob")
    expect(start).toHaveAccessibleName(/start a conversation with bob/i)
    // A SidebarRow, so it is in the tab order and answers Enter/Space.
    expect(start).toHaveAttribute("tabindex", "0")
  })

  it("swaps to another agent in place: the URL moves, the router does not, the page does not remount", async () => {
    const pushSpy = vi.spyOn(window.history, "pushState")
    render(<ChatPageClient />)
    await waitFor(() =>
      expect(screen.getByTestId("chat-panel")).toHaveAttribute("data-agent-id", "agent-1"),
    )
    await screen.findByTestId("chat-tree-agent-bob")

    // The nodes that must survive. React reuses a DOM node only when it does
    // NOT unmount the subtree, so identity here is the remount assertion —
    // this is the dashboard chrome tearing down, in miniature.
    const gridBefore = screen.getByTestId("chat-layout-grid")
    const treeBefore = screen.getByTestId("chat-tree-sidebar")

    pushSpy.mockClear()
    mockPush.mockClear()

    fireEvent.click(screen.getByTestId("chat-tree-agent-bob"))
    fireEvent.click(await screen.findByTestId("chat-tree-start-bob"))

    // The panel follows: a different agent, and a session that agent's page
    // decided on (openInitialSession), not one carried in the URL.
    await waitFor(() =>
      expect(screen.getByTestId("chat-panel")).toHaveAttribute("data-agent-id", "agent-2"),
    )
    // The page's own slug followed too — the agent was re-resolved from the
    // roster already in memory, with no request to find out who Bob is.
    expect(screen.getByTitle("Back to agent canvas")).toHaveAttribute("href", "/crews?agent=bob")

    // The address bar moved…
    expect(
      pushSpy.mock.calls.some((c) => typeof c[2] === "string" && c[2] === "/chat/bob"),
    ).toBe(true)
    // …without Next.js being asked to re-resolve the route…
    expectNoNavigation()
    // …and nothing around the conversation was rebuilt to do it.
    expect(screen.getByTestId("chat-layout-grid")).toBe(gridBefore)
    expect(screen.getByTestId("chat-tree-sidebar")).toBe(treeBefore)
  })

  it("still opens the session a deep link names", async () => {
    searchParams = new URLSearchParams("session=sess-old")
    mountDesktop("?session=sess-old")

    render(<ChatPageClient />)

    // internal/chatnotify emits exactly this shape.
    await waitFor(() =>
      expect(screen.getByTestId("chat-panel")).toHaveAttribute("data-session-id", "sess-old"),
    )
    expect(await screen.findByTestId("chat-tree-agent-filip")).toBeInTheDocument()
  })
})
