import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor, fireEvent, cleanup, within } from "@testing-library/react"

// =============================================================================
// The chat page's left column is the shared agent tree, and the only thing you
// can pick in it is a conversation.
//
// It briefly offered four folders per agent — Sessions / Files / Asks / Memory
// — and three of them replaced the centre pane with a surface that already
// existed somewhere else: Files in this page's own right rail, Asks in the
// agent's configuration tab, Memory on the agent canvas. `Files` was on screen
// twice at once, left and right, in the same view. The rule that settled it:
//
//   left column = navigation between objects  ("where am I going")
//   right panel = context of the open object  ("what is here")
//   config page = the object's own settings
//
// So the tree navigates conversations and nothing else, and `?folder=` is dead
// — including in a URL somebody bookmarked while it worked, which must land on
// the conversation AND stop carrying a parameter nothing reads.
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

// The flat list is the narrow-viewport drawer now; this stands in for it so
// the fallback below 900px can be asserted by identity, not by guesswork.
vi.mock("@/components/features/chat/sessions-sidebar", () => ({
  SessionsSidebar: () => <div data-testid="sessions-sidebar" />,
}))

// The composer lives inside ChatPanel — so "is there a ChatPanel" and "is
// there a composer" are the same question, and the stand-in makes the
// assertion say which one it means.
vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: ({ sessionId }: { sessionId: string }) => (
    <div data-testid="chat-panel" data-session-id={sessionId}>
      <div data-testid="chat-composer" />
    </div>
  ),
}))

import { ChatPageClient } from "../chat-page-client"

const agents = [
  {
    id: "agent-1",
    name: "Filip",
    slug: "filip",
    status: "IDLE",
    role_title: "Data Analyst",
    avatar_seed: "filip",
    avatar_style: null,
    crew_id: "crew-1",
    crew: { name: "Research", slug: "research", avatar_style: null },
  },
]

const iso = new Date().toISOString()
const sessions = [
  { id: "sess-1", title: "Yesterday", status: "ACTIVE", message_count: 3, started_at: iso, ended_at: null, last_activity_at: iso, origin: "UI" },
  { id: "sess-2", title: "The export", status: "ACTIVE", message_count: 9, started_at: iso, ended_at: null, last_activity_at: iso, origin: "UI" },
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
    if (u.includes("/chats")) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(sessions) }) as unknown as Promise<Response>
    }
    if (u.includes("/api/v1/agents")) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(agents) }) as unknown as Promise<Response>
    }
    return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}) }) as unknown as Promise<Response>
  }) as unknown as typeof fetch
}

describe("<ChatPageClient> — the tree is the left column", () => {
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

  it("replaces the flat 240px session list with the shared 280px tree", async () => {
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    expect(screen.getByTestId("chat-tree-sidebar")).toBeInTheDocument()
    // 240px was this page's own number. The kit's is 280.
    expect(screen.getByTestId("chat-layout-grid").style.gridTemplateColumns).toBe("280px 1fr")
  })

  it("selecting a thread in the tree opens that conversation", async () => {
    const pushSpy = vi.spyOn(window.history, "pushState")
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    fireEvent.click(await screen.findByTestId("chat-tree-thread-sess-2"))

    await waitFor(() =>
      expect(screen.getByTestId("chat-panel")).toHaveAttribute("data-session-id", "sess-2"),
    )
    expect(
      pushSpy.mock.calls.some((c) => typeof c[2] === "string" && c[2] === "/chat/filip?session=sess-2"),
    ).toBe(true)
  })

  it("offers no Files, Asks or Memory row — the tree navigates conversations only", async () => {
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    // The active agent is expanded already (arriving at /chat/filip and
    // finding filip collapsed would be the tree hiding where you are).
    await screen.findByTestId("chat-tree-thread-sess-1")

    expect(document.querySelectorAll('[data-testid^="chat-tree-folder-"]')).toHaveLength(0)
    for (const gone of [/^files$/i, /^asks$/i, /^memory$/i, /^sessions$/i]) {
      expect(screen.queryByText(gone)).toBeNull()
    }
    expect(screen.queryByTestId("chat-folder-pane")).toBeNull()
  })

  it("never unmounts the conversation for a folder — the composer stays put", async () => {
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    fireEvent.click(await screen.findByTestId("chat-tree-agent-filip"))
    fireEvent.click(screen.getByTestId("chat-tree-agent-filip"))

    // Each mounted panel opens its own WebSocket, so two on screen is two
    // sockets for one agent — and zero is a page with no conversation on it.
    expect(screen.getAllByTestId("chat-panel")).toHaveLength(1)
    expect(screen.getByTestId("chat-composer")).toBeInTheDocument()
  })

  it("lands a bookmarked ?folder= URL on the conversation and scrubs the dead parameter", async () => {
    const replaceSpy = vi.spyOn(window.history, "replaceState")
    searchParams = new URLSearchParams("folder=files&session=sess-2")
    mountDesktop("?folder=files&session=sess-2")

    render(<ChatPageClient />)

    // The folder pane does not exist any more; the session in the same URL
    // is still honoured.
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())
    expect(screen.getByTestId("chat-panel")).toHaveAttribute("data-session-id", "sess-2")
    expect(screen.queryByTestId("chat-folder-pane")).toBeNull()

    // …and the address bar stops carrying state nothing reads.
    await waitFor(() =>
      expect(
        replaceSpy.mock.calls.some((c) => typeof c[2] === "string" && c[2] === "/chat/filip?session=sess-2"),
      ).toBe(true),
    )
  })

  it("says the open agent's session list failed rather than showing it as empty", async () => {
    // The page fetches THIS agent's chats itself (the tree's fan-out skips
    // it), and that fetch had the same `r.ok ? r.json() : []` defect: a 500
    // rendered as an agent with no history at all.
    let failing = true
    global.fetch = vi.fn((url: string) => {
      const u = String(url)
      if (u.includes("/chats")) {
        return (failing
          ? Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({}) })
          : Promise.resolve({ ok: true, json: () => Promise.resolve(sessions) })) as unknown as Promise<Response>
      }
      if (u.includes("/api/v1/agents")) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(agents) }) as unknown as Promise<Response>
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}) }) as unknown as Promise<Response>
    }) as unknown as typeof fetch

    render(<ChatPageClient />)

    const failed = await screen.findByTestId("chat-tree-threads-error-filip")
    expect(failed).toHaveTextContent(/500/)

    failing = false
    fireEvent.click(within(failed).getByRole("button", { name: /retry/i }))

    await waitFor(() => expect(screen.queryByTestId("chat-tree-threads-error-filip")).toBeNull())
    expect(await screen.findByTestId("chat-tree-thread-sess-1")).toBeInTheDocument()
  })

  it("below 900px the tree steps aside for the drawer and the tab strip", async () => {
    Object.defineProperty(window, "innerWidth", { value: 860, writable: true, configurable: true })

    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    // 280px of tree plus a conversation does not fit 860px, so the surface
    // falls back to the shape built for exactly this — not to a squeezed tree.
    expect(screen.queryByTestId("chat-tree-sidebar")).toBeNull()
    expect(screen.getByTestId("chat-layout-grid").style.gridTemplateColumns).toBe("1fr")
    expect(screen.getByRole("tablist", { name: /panel/i })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /sessions/i }))
    expect(screen.getByTestId("sessions-sidebar")).toBeInTheDocument()
  })
})
