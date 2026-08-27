import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

// =============================================================================
// Three props the surface rewrite dropped on the floor.
//
// `chat-page-client.tsx` resolved its agent out of the roster it had already
// fetched for the tree and handed ChatPanel three values off that record.
// `chat-client.tsx` replaced it and passed none of them, and because the tests
// that covered them lived beside the deleted page they went out with it — so
// nothing failed.
//
// None of the three is cosmetic:
//
//  · `suggestedPrompts` — ChatPanel calls `getSuggestions(agentRole,
//    suggestedPrompts)`. Omitted, every agent somebody had configured with its
//    own chips silently reverted to the generic role pack.
//  · `askForms` — a documented THREE-state prop. `null` means "this agent has
//    no forms"; OMITTED means "I have no record, go and ask", which is a detail
//    fetch per conversation for a column the page is already holding.
//  · `sessionOrigin` — the connection bar's origin chip. It is the only place
//    the surface says a thread was opened by a cron routine rather than a
//    person, which is exactly what an operator looks for.
//
// The assertions below fail against the rewrite as merged.
// =============================================================================

vi.mock("next/navigation", () => ({
  useSearchParams: () => new URLSearchParams(window.location.search.replace(/^\?/, "")),
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1", loading: false }),
}))

vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEventSafe: () => {},
}))

vi.mock("@/lib/telemetry", () => ({
  emitChatEvent: vi.fn(),
}))

// The column is not what is under test, and the real one pulls in avatars and
// a filter kit that have their own suites.
//
// It does echo `threadsLoaded`, though, and that is load-bearing: the roster
// resolves one fetch before the per-agent thread fan-out does, so ChatPanel
// mounts with `threadsByAgent` still empty and only then re-renders with the
// thread's origin. Asserting on the panel's mere presence reads that first
// frame — which passes in isolation and fails under a loaded suite. Every
// assertion below waits for this flag first.
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

// Mocked down to the props under test — the real panel opens a WebSocket.
// `(undefined)` and `(null)` are rendered as distinct strings on purpose: the
// whole point of `askForms` is that those two are different answers.
vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: ({
    suggestedPrompts,
    askForms,
    sessionOrigin,
  }: {
    suggestedPrompts?: string | null
    askForms?: string | null
    sessionOrigin?: string | null
  }) => (
    <div
      data-testid="chat-panel"
      data-suggested={suggestedPrompts === undefined ? "(undefined)" : suggestedPrompts ?? "(null)"}
      data-ask-forms={askForms === undefined ? "(undefined)" : askForms ?? "(null)"}
      data-origin={sessionOrigin === undefined ? "(undefined)" : sessionOrigin ?? "(null)"}
    />
  ),
}))

import { ChatClient } from "../chat-client"

const receiptForm = JSON.stringify([
  {
    id: "receipt",
    attachment: "required",
    fields: [{ name: "supplier", label: "Supplier", type: "text", required: true }],
  },
])

const agent = {
  id: "agent-1",
  name: "Filip",
  slug: "filip",
  status: "IDLE",
  role_title: "Data Analyst",
  avatar_seed: "filip",
  avatar_style: null,
  crew_id: "crew-1",
  suggested_prompts: "Summarise this quarter\nWhat changed since Friday?" as string | null,
  ask_forms: receiptForm as string | null,
}

const thread = {
  id: "sess-1",
  title: "Yesterday",
  status: "ACTIVE",
  message_count: 3,
  started_at: "2026-08-20T10:00:00Z",
  last_activity_at: "2026-08-20T10:05:00Z",
  ended_at: null,
  origin: "CRON" as string | null,
}

const apiFetchMock = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetchMock(...args),
}))

function ok(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) } as Response)
}

describe("<ChatClient> — the panel gets what the roster record already holds", () => {
  beforeEach(() => {
    agent.suggested_prompts = "Summarise this quarter\nWhat changed since Friday?"
    agent.ask_forms = receiptForm
    thread.origin = "CRON"

    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} })),
    )
    Object.defineProperty(window, "innerWidth", { value: 1280, writable: true, configurable: true })
    Object.defineProperty(window, "location", {
      configurable: true,
      writable: true,
      value: { ...window.location, pathname: "/chat/filip", search: "?session=sess-1" },
    })
    window.history.replaceState = vi.fn()

    apiFetchMock.mockReset()
    apiFetchMock.mockImplementation((url: string) => {
      const u = String(url)
      if (/\/chats\?/.test(u)) return ok([thread])
      if (/\/api\/v1\/agents\?/.test(u)) return ok([agent])
      return ok({})
    })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  /** Render, and wait until BOTH fetches have settled — see the sidebar mock. */
  const panel = async () => {
    render(<ChatClient />)
    await waitFor(() =>
      expect(screen.getByTestId("sidebar").getAttribute("data-loaded")).toBe("true"),
    )
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())
    return screen.getByTestId("chat-panel")
  }

  it("passes the record's suggested_prompts, so a configured agent keeps its own chips", async () => {
    expect((await panel()).getAttribute("data-suggested")).toBe(
      "Summarise this quarter\nWhat changed since Friday?",
    )
  })

  it("passes null — not nothing — for an agent with no chips", async () => {
    agent.suggested_prompts = null
    expect((await panel()).getAttribute("data-suggested")).toBe("(null)")
  })

  it("passes the record's ask_forms column straight through", async () => {
    expect((await panel()).getAttribute("data-ask-forms")).toBe(receiptForm)
  })

  it("passes null — not nothing — for an agent with no forms, so the panel does not go looking", async () => {
    agent.ask_forms = null
    // "(undefined)" would mean the page said nothing, which is the panel's
    // signal to fetch the agent detail endpoint for itself.
    expect((await panel()).getAttribute("data-ask-forms")).toBe("(null)")
  })

  it("makes no request for the agent detail endpoint at all", async () => {
    await panel()
    const detail = apiFetchMock.mock.calls
      .map((c) => String(c[0]))
      .filter((u) => /\/api\/v1\/agents\/[^/?]+(\?|$)/.test(u))
    expect(detail).toEqual([])
  })

  it("reports the open conversation's origin, so the chip can say CRON", async () => {
    expect((await panel()).getAttribute("data-origin")).toBe("CRON")
  })

  it("reports null for a thread whose origin predates the column", async () => {
    thread.origin = null
    expect((await panel()).getAttribute("data-origin")).toBe("(null)")
  })
})
