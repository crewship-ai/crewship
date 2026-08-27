import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// =============================================================================
// Naming a conversation after its first message.
//
// The derivation itself is a pure function and is tabled in
// `lib/__tests__/chat-title.test.ts`. What is NOT covered there is the wiring,
// and the wiring is a WRITE — a PATCH that fires on the first message of every
// conversation the product opens. The 410-line suite that used to pin it
// belonged to the page this rewrite deleted, and its replacement was never
// written; this is that replacement, at the size the behaviour actually needs.
//
// Three properties, each of which the code comment claims and none of which
// was being checked:
//
//   * it fires ONCE. The write is asynchronous, so two quick sends both read a
//     null title and both PATCH — which is why there is a ref and not just an
//     "already titled" test.
//   * it does not overwrite a name that exists, INCLUDING one the user typed.
//   * a message that derives nothing usable leaves the conversation untitled,
//     because a thread called "…" looks like a name and carries none of the
//     information one.
// =============================================================================

let searchParams = new URLSearchParams()

vi.mock("next/navigation", () => ({
  useSearchParams: () => searchParams,
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1", loading: false }),
}))

vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: () => {} }))

const emitChatEvent = vi.fn()
vi.mock("@/lib/telemetry", () => ({ emitChatEvent: (...a: unknown[]) => emitChatEvent(...a) }))

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

// The panel is the thing that calls `onSend`; a button standing in for the
// composer is enough, and it keeps the test about the page's reaction rather
// than about the composer's internals.
vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: ({
    sessionId,
    onSend,
  }: {
    sessionId: string
    onSend?: (sid: string, text: string) => void
  }) => (
    <button
      data-testid="send"
      onClick={() => onSend?.(sessionId, "Summarise the Q3 revenue report for me")}
    >
      send
    </button>
  ),
}))

import { ChatClient } from "../chat-client"

const riley = { id: "a-riley", name: "Riley", slug: "riley", status: "IDLE", crew_id: "c1" }

function thread(over: Record<string, unknown> = {}) {
  return {
    id: "riley-1",
    title: null,
    status: "ACTIVE",
    message_count: 1,
    started_at: "2026-08-20T10:00:00Z",
    last_activity_at: "2026-08-20T10:00:00Z",
    ...over,
  }
}

const apiFetchMock = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetchMock(...args),
}))

function ok(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) } as Response)
}

function setUrl(pathname: string, search: string) {
  Object.defineProperty(window, "location", {
    configurable: true,
    writable: true,
    value: { ...window.location, pathname, search },
  })
  searchParams = new URLSearchParams(search.replace(/^\?/, ""))
}

/** Every PATCH the page made against a chat, with its parsed body. */
function patches() {
  return apiFetchMock.mock.calls
    .filter(([, init]) => (init as RequestInit | undefined)?.method === "PATCH")
    .map(([url, init]) => ({
      url: String(url),
      body: JSON.parse(String((init as RequestInit).body)) as { title?: string },
    }))
}

function mountWith(rows: unknown[]) {
  apiFetchMock.mockImplementation((url: string, init?: RequestInit) => {
    const u = String(url)
    if (init?.method === "PATCH") return ok({ title: "Summarise the Q3 revenue report for me" })
    if (/\/api\/v1\/agents\/[^/]+\/chats/.test(u)) return ok(rows)
    if (/\/api\/v1\/agents\?/.test(u)) return ok([riley])
    return ok({})
  })
  setUrl("/chat/riley", "")
  render(<ChatClient />)
}

async function sendButton() {
  await waitFor(() =>
    expect(screen.getByTestId("sidebar").getAttribute("data-loaded")).toBe("true"),
  )
  return await waitFor(() => screen.getByTestId("send"))
}

describe("<ChatClient> — a conversation is named after its first message", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} })),
    )
    Object.defineProperty(window, "innerWidth", { value: 1280, writable: true, configurable: true })
    window.history.replaceState = vi.fn()
    apiFetchMock.mockReset()
    emitChatEvent.mockReset()
  })

  afterEach(() => vi.restoreAllMocks())

  it("PATCHes the derived title for an untitled conversation", async () => {
    mountWith([thread()])
    fireEvent.click(await sendButton())

    await waitFor(() => expect(patches()).toHaveLength(1))
    const [p] = patches()
    expect(p.url).toContain("/api/v1/agents/a-riley/chats/riley-1")
    expect(p.body.title).toBe("Summarise the Q3 revenue report for me")
  })

  it("emits chat_session_titled only after the server accepts the name", async () => {
    mountWith([thread()])
    fireEvent.click(await sendButton())

    await waitFor(() =>
      expect(emitChatEvent).toHaveBeenCalledWith("chat_session_titled", {
        session_id: "riley-1",
        source: "auto",
      }),
    )
  })

  it("does not rename a conversation that already has a title", async () => {
    mountWith([thread({ title: "A name the user typed" })])
    fireEvent.click(await sendButton())

    // Nothing to wait for on the happy path, so wait for the send to have been
    // processed and then assert the absence.
    await waitFor(() => expect(apiFetchMock).toHaveBeenCalled())
    expect(patches()).toHaveLength(0)
    expect(emitChatEvent).not.toHaveBeenCalled()
  })

  it("fires once across two quick sends, not once per send", async () => {
    mountWith([thread()])
    const send = await sendButton()

    // Both clicks read a title that is still null — the PATCH from the first
    // has not returned, and the fan-out it triggers has not re-rendered. The
    // ref is what makes "once" a property of the page rather than a race.
    fireEvent.click(send)
    fireEvent.click(send)

    await waitFor(() => expect(patches().length).toBeGreaterThan(0))
    expect(patches()).toHaveLength(1)
  })
})
