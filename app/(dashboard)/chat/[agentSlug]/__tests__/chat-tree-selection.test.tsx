import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor, fireEvent, cleanup } from "@testing-library/react"

// =============================================================================
// The chat page's left column is the shared agent tree, and what you pick in it
// decides what the centre pane is.
//
// Two things this pins down that the flat session list could not:
//
//  · A FOLDER is not a conversation. Picking Files/Asks/Memory replaces the
//    centre pane with that folder's surface and takes the composer with it —
//    a message box dangling under a file listing is a promise the page cannot
//    keep. Unmounting ChatPanel is also what closes its WebSocket
//    (chat-panel.tsx opens one per mounted panel), so "no composer" and "no
//    second socket" are the same fact here.
//
//  · The folder never becomes a path segment. The static export rewrites
//    exactly one level (internal/api/static.go) and the slug is parsed out of
//    window.location.pathname, so /chat/<agent>/<folder> would 404 in
//    production while passing every dev-server test. It is a query parameter.
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

function mountDesktop() {
  Object.defineProperty(window, "innerWidth", { value: 1280, writable: true, configurable: true })
  vi.stubGlobal("matchMedia", vi.fn(() => ({
    matches: false,
    addEventListener: () => {},
    removeEventListener: () => {},
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
      return Promise.resolve({ ok: true, json: () => Promise.resolve(agents) }) as unknown as Promise<Response>
    }
    return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}) }) as unknown as Promise<Response>
  }) as unknown as typeof fetch
}

/**
 * The agent the URL names is expanded already — arriving at /chat/filip and
 * finding filip collapsed would be the tree hiding where you are — so a folder
 * row is reached by waiting for it, not by opening its parent.
 */
function folderRow(folder: string): Promise<HTMLElement> {
  return screen.findByTestId(`chat-tree-folder-filip-${folder}`)
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

    await folderRow("sessions")
    fireEvent.click(await screen.findByTestId("chat-tree-thread-sess-2"))

    await waitFor(() =>
      expect(screen.getByTestId("chat-panel")).toHaveAttribute("data-session-id", "sess-2"),
    )
    expect(
      pushSpy.mock.calls.some((c) => typeof c[2] === "string" && c[2] === "/chat/filip?session=sess-2"),
    ).toBe(true)
  })

  it("selecting a folder replaces the centre pane and takes the composer with it", async () => {
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    fireEvent.click(await folderRow("files"))

    await waitFor(() => expect(screen.getByTestId("chat-folder-pane")).toBeInTheDocument())
    expect(screen.queryByTestId("chat-panel")).toBeNull()
    expect(screen.queryByTestId("chat-composer")).toBeNull()
  })

  it("encodes the folder as a query parameter, never as a deeper route", async () => {
    const pushSpy = vi.spyOn(window.history, "pushState")
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    fireEvent.click(await folderRow("memory"))

    await waitFor(() =>
      expect(
        pushSpy.mock.calls.some((c) => typeof c[2] === "string" && /[?&]folder=memory\b/.test(c[2])),
      ).toBe(true),
    )
    // One path level. /chat/filip/memory would 404 under the static export.
    for (const call of pushSpy.mock.calls) {
      if (typeof call[2] === "string") expect(call[2].split("?")[0]).toBe("/chat/filip")
    }
  })

  it("comes back to exactly one conversation panel when Sessions is picked again", async () => {
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    fireEvent.click(await folderRow("asks"))
    await waitFor(() => expect(screen.queryByTestId("chat-panel")).toBeNull())

    fireEvent.click(await folderRow("sessions"))

    // Each mounted panel opens its own WebSocket, so two on screen is two
    // sockets for one agent.
    await waitFor(() => expect(screen.getAllByTestId("chat-panel")).toHaveLength(1))
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

  it("gives a narrow viewport a way out of a folder it was deep-linked into", async () => {
    searchParams = new URLSearchParams("folder=files")
    Object.defineProperty(window, "innerWidth", { value: 860, writable: true, configurable: true })
    Object.defineProperty(window, "location", {
      configurable: true,
      writable: true,
      value: { ...window.location, pathname: "/chat/filip", search: "?folder=files" },
    })

    render(<ChatPageClient />)
    expect(await screen.findByTestId("chat-folder-pane")).toBeInTheDocument()

    // There is no tree at this width, so the tab strip is the only door back.
    fireEvent.click(screen.getByRole("tab", { name: /chat/i }))

    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())
    expect(screen.queryByTestId("chat-folder-pane")).toBeNull()
  })

  it("opens straight into a folder when the URL already names one", async () => {
    searchParams = new URLSearchParams("folder=files")
    Object.defineProperty(window, "location", {
      configurable: true,
      writable: true,
      value: { ...window.location, pathname: "/chat/filip", search: "?folder=files" },
    })

    render(<ChatPageClient />)

    expect(await screen.findByTestId("chat-folder-pane")).toBeInTheDocument()
    expect(screen.queryByTestId("chat-panel")).toBeNull()
  })
})
