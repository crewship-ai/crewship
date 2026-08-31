import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

// =============================================================================
// Two ways the page could show you a conversation you did not ask for.
//
// 1. A URL that names a different agent and no session left the previous
//    `sessionId` in state. The auto-open effect returns early when a session is
//    already set, so the page rendered the OLD conversation under the NEW
//    agent's name. Reachable with the Back button — which is the whole reason
//    this page listens for `popstate`.
//
// 2. `?prompt=` is auto-sent on mount, and ChatPanel is keyed on the session,
//    so selecting another thread REMOUNTS it. The handoff prompt was still in
//    state, so it was sent again — a message nobody typed, in a conversation
//    the reader opened for something else.
// =============================================================================

let searchParams = new URLSearchParams()

vi.mock("next/navigation", () => ({
  useSearchParams: () => searchParams,
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1", loading: false }),
}))

vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: () => {} }))
vi.mock("@/lib/telemetry", () => ({ emitChatEvent: vi.fn() }))

vi.mock("@/components/features/chat/conversations-sidebar", async () => {
  const actual = await vi.importActual<
    typeof import("@/components/features/chat/conversations-sidebar")
  >("@/components/features/chat/conversations-sidebar")
  return {
    ...actual,
    ConversationsSidebar: ({ threadsLoaded }: { threadsLoaded: boolean }) => (
      <div data-testid="sidebar" data-loaded={String(!!threadsLoaded)} />
    ),
  }
})

vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: ({
    agentSlug,
    sessionId,
    initialInput,
    autoSendInitial,
  }: {
    agentSlug: string
    sessionId: string
    initialInput?: string
    autoSendInitial?: boolean
  }) => (
    <div
      data-testid="chat-panel"
      data-agent={agentSlug}
      data-session={sessionId}
      data-initial={initialInput ?? "(none)"}
      data-autosend={String(!!autoSendInitial)}
    />
  ),
}))

import { ChatClient } from "../chat-client"

const riley = { id: "a-riley", name: "Riley", slug: "riley", status: "IDLE", crew_id: "c1" }
const morgan = { id: "a-morgan", name: "Morgan", slug: "morgan", status: "IDLE", crew_id: "c1" }

const threads: Record<string, unknown[]> = {
  "a-riley": [
    {
      id: "riley-1",
      title: "Riley's thread",
      status: "ACTIVE",
      message_count: 1,
      started_at: "2026-08-20T10:00:00Z",
      last_activity_at: "2026-08-20T10:00:00Z",
    },
  ],
  "a-morgan": [
    {
      id: "morgan-1",
      title: "Morgan's thread",
      status: "ACTIVE",
      message_count: 1,
      started_at: "2026-08-21T10:00:00Z",
      last_activity_at: "2026-08-21T10:00:00Z",
    },
  ],
}

const apiFetchMock = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetchMock(...args),
}))

function ok(body: unknown, headers: Record<string, string> = {}) {
  // `headers` is real, not omitted: the fan-out reads the per-kind totals off
  // `X-Chat-Kind-Counts`, and a double thinner than a Response is how a stub
  // stops testing the code and starts testing itself.
  return Promise.resolve({
    ok: true,
    status: 200,
    headers: new Headers(headers),
    json: () => Promise.resolve(body),
  } as Response)
}

function setUrl(pathname: string, search: string) {
  Object.defineProperty(window, "location", {
    configurable: true,
    writable: true,
    value: { ...window.location, pathname, search },
  })
  searchParams = new URLSearchParams(search.replace(/^\?/, ""))
}

async function panel() {
  await waitFor(() =>
    expect(screen.getByTestId("sidebar").getAttribute("data-loaded")).toBe("true"),
  )
  await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())
  return screen.getByTestId("chat-panel")
}

describe("<ChatClient> — the URL must not leave a stale selection behind", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} })),
    )
    Object.defineProperty(window, "innerWidth", { value: 1280, writable: true, configurable: true })
    window.history.replaceState = vi.fn()

    apiFetchMock.mockReset()
    apiFetchMock.mockImplementation((url: string) => {
      const u = String(url)
      const m = /\/api\/v1\/agents\/([^/]+)\/chats/.exec(u)
      if (m) return ok(threads[m[1]] ?? [])
      if (/\/api\/v1\/agents\?/.test(u)) return ok([riley, morgan])
      return ok({})
    })
  })

  afterEach(() => vi.restoreAllMocks())

  it("opens the named agent's own freshest thread on a bare /chat/<slug>", async () => {
    setUrl("/chat/riley", "")
    render(<ChatClient />)
    const p = await panel()
    expect(p.getAttribute("data-agent")).toBe("riley")
    expect(p.getAttribute("data-session")).toBe("riley-1")
  })

  it("does not carry a session across a Back that names another agent", async () => {
    // Land on Morgan's conversation…
    setUrl("/chat/morgan", "?session=morgan-1")
    const view = render(<ChatClient />)
    expect((await panel()).getAttribute("data-session")).toBe("morgan-1")

    // …then Back to a bare /chat/riley. `popstate` is what the page listens
    // for, and the URL names a different agent with no session.
    setUrl("/chat/riley", "")
    view.rerender(<ChatClient />)
    window.dispatchEvent(new Event("popstate"))

    await waitFor(() => {
      const p = screen.getByTestId("chat-panel")
      expect(p.getAttribute("data-agent")).toBe("riley")
      // Morgan's conversation under Riley's name is the bug.
      expect(p.getAttribute("data-session")).toBe("riley-1")
    })
  })
})

describe("<ChatClient> — the ?prompt= handoff belongs to one conversation", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} })),
    )
    Object.defineProperty(window, "innerWidth", { value: 1280, writable: true, configurable: true })
    window.history.replaceState = vi.fn()

    apiFetchMock.mockReset()
    apiFetchMock.mockImplementation((url: string) => {
      const u = String(url)
      const m = /\/api\/v1\/agents\/([^/]+)\/chats/.exec(u)
      if (m) return ok(threads[m[1]] ?? [])
      if (/\/api\/v1\/agents\?/.test(u)) return ok([riley, morgan])
      return ok({})
    })
  })

  afterEach(() => vi.restoreAllMocks())

  it("auto-sends the prompt in the conversation it arrived for", async () => {
    setUrl("/chat/riley", "?session=riley-1&prompt=Draft%20the%20routine")
    render(<ChatClient />)
    const p = await panel()
    expect(p.getAttribute("data-initial")).toBe("Draft the routine")
    expect(p.getAttribute("data-autosend")).toBe("true")
  })

  it("does not re-send it into a conversation the reader switches to", async () => {
    setUrl("/chat/riley", "?session=riley-1&prompt=Draft%20the%20routine")
    const view = render(<ChatClient />)
    expect((await panel()).getAttribute("data-autosend")).toBe("true")

    // Switching threads remounts ChatPanel, which is what re-fires
    // autoSendInitial. The handoff must not come with it.
    setUrl("/chat/morgan", "?session=morgan-1")
    view.rerender(<ChatClient />)
    window.dispatchEvent(new Event("popstate"))

    await waitFor(() => {
      const p = screen.getByTestId("chat-panel")
      expect(p.getAttribute("data-session")).toBe("morgan-1")
    })
    const p = screen.getByTestId("chat-panel")
    expect(p.getAttribute("data-autosend")).toBe("false")
    expect(p.getAttribute("data-initial")).toBe("(none)")
  })
})
