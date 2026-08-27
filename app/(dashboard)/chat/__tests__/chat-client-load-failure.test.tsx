import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

// =============================================================================
// A failed fan-out must not mint a draft on top of a real conversation.
//
// This is the `ensureSlug` bug reached by another road. The cap fix guarantees
// the named agent a SLOT in the fan-out; it cannot guarantee the request in
// that slot succeeds. When it 500s, `threadsByAgent[named.id]` is absent for a
// reason that has nothing to do with the agent having no history — and the
// auto-open effect read absent as empty and opened a new conversation.
//
// `crewship open <agent>` and every "Open chat" link in the product build
// exactly this URL, so the wrong write is one bad response away on the most
// common entry point the surface has.
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

// Partial: the page also imports `applyReadOverrides` from this module.
vi.mock("@/components/features/chat/conversations-sidebar", async () => {
  const actual = await vi.importActual<
    typeof import("@/components/features/chat/conversations-sidebar")
  >("@/components/features/chat/conversations-sidebar")
  return {
    ...actual,
    ConversationsSidebar: ({
      threadsLoaded,
      loadError,
      threadErrors,
    }: {
      threadsLoaded: boolean
      loadError?: string | null
      threadErrors?: Record<string, string>
    }) => (
      <div
        data-testid="sidebar"
        data-loaded={String(!!threadsLoaded)}
        data-load-error={loadError ?? "(none)"}
        data-thread-errors={Object.keys(threadErrors ?? {}).sort().join(",") || "(none)"}
      />
    ),
  }
})

vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: ({ agentSlug, sessionId }: { agentSlug: string; sessionId: string }) => (
    <div data-testid="chat-panel" data-agent={agentSlug} data-session={sessionId} />
  ),
}))

import { ChatClient } from "../chat-client"

const riley = { id: "a-riley", name: "Riley", slug: "riley", status: "IDLE", crew_id: "c1" }

const apiFetchMock = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetchMock(...args),
}))

function ok(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) } as Response)
}
function fail(status: number) {
  return Promise.resolve({ ok: false, status, json: () => Promise.resolve(null) } as Response)
}

function setUrl(pathname: string, search: string) {
  Object.defineProperty(window, "location", {
    configurable: true,
    writable: true,
    value: { ...window.location, pathname, search },
  })
  searchParams = new URLSearchParams(search.replace(/^\?/, ""))
}

async function sidebarSettled() {
  await waitFor(() =>
    expect(screen.getByTestId("sidebar").getAttribute("data-loaded")).toBe("true"),
  )
  return screen.getByTestId("sidebar")
}

describe("<ChatClient> — a load failure is reported, not rendered as emptiness", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} })),
    )
    Object.defineProperty(window, "innerWidth", { value: 1280, writable: true, configurable: true })
    window.history.replaceState = vi.fn()
    apiFetchMock.mockReset()
  })

  afterEach(() => vi.restoreAllMocks())

  it("does not open a new conversation for an agent whose thread list 500ed", async () => {
    apiFetchMock.mockImplementation((url: string) => {
      const u = String(url)
      if (/\/api\/v1\/agents\/[^/]+\/chats/.test(u)) return fail(500)
      if (/\/api\/v1\/agents\?/.test(u)) return ok([riley])
      return ok({})
    })

    setUrl("/chat/riley", "")
    render(<ChatClient />)
    const sidebar = await sidebarSettled()

    expect(sidebar.getAttribute("data-thread-errors")).toBe("a-riley")
    // The panel is what a minted draft would render. Its absence is the
    // assertion: no conversation was invented against an unknown history.
    expect(screen.queryByTestId("chat-panel")).not.toBeInTheDocument()
  })

  it("still opens a new conversation when the list genuinely came back empty", async () => {
    apiFetchMock.mockImplementation((url: string) => {
      const u = String(url)
      if (/\/api\/v1\/agents\/[^/]+\/chats/.test(u)) return ok([])
      if (/\/api\/v1\/agents\?/.test(u)) return ok([riley])
      return ok({})
    })

    setUrl("/chat/riley", "")
    render(<ChatClient />)
    await sidebarSettled()

    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())
    expect(screen.getByTestId("chat-panel").getAttribute("data-agent")).toBe("riley")
  })

  it("hands the column the roster failure rather than an empty roster", async () => {
    apiFetchMock.mockImplementation((url: string) => {
      const u = String(url)
      if (/\/api\/v1\/agents\?/.test(u)) return fail(503)
      return ok([])
    })

    setUrl("/chat", "")
    render(<ChatClient />)
    const sidebar = await sidebarSettled()

    expect(sidebar.getAttribute("data-load-error")).toBe("HTTP 503")
  })
})
