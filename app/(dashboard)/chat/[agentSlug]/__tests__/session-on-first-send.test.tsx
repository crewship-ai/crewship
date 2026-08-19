import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// =============================================================================
// Arriving at a chat must not create a chat — and sending into one must.
//
// ensureSession() used to POST /agents/{id}/chats from a mount effect: open
// the page, and a session existed whether or not a word was ever typed. With
// chat becoming a top-level surface (PRD step 4) every stray click on the nav
// entry would mint another "Untitled session", and the sidebar filled with
// empty threads — the same noise the sessionsLoaded gate was already added to
// suppress, one layer down.
//
// The fix leans on machinery that was already there: POST /agents/{id}/chats
// accepts a client-supplied `session_id` and does INSERT OR IGNORE
// (internal/api/agent_chats.go, CreateChat), and ChatPanel's ensureSession()
// fires that POST on the first send. So the page mints a draft id locally and
// hands it straight to the panel; the row appears in the database when — and
// only when — a message goes out.
//
// **This suite renders the REAL ChatPanel.** It used to substitute a stand-in
// that POSTed the chat itself, which meant the half of the flow that broke was
// the half nobody was testing: the real panel probed
// GET /chats/{id}/messages and skipped the create whenever the response was
// not a 404 — and that endpoint answers **200 with an empty message list** for
// a chat that does not exist (internal/api/proxy.go, ChatMessages). Draft
// sessions were therefore never created, and the conversation was lost. The
// fake server below answers exactly as proxy.go does; a mock that disagrees
// with the server is a test that certifies a bug.
//
// Two behaviours must survive, and both have tests here:
//   · `?prompt=` (routine-create-dialog) still wants a FRESH session up front,
//     because it auto-sends into it;
//   · with no `?session=`, the freshest EXISTING session is still selected —
//     it was only the creating that moved.
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

// The WebSocket, stubbed at the hook. Everything this suite is about happens
// over HTTP; what the socket contributes is `sendMessage` (did the message go
// out?) and `resubscribeSession` (the channel is re-taken once the row exists
// — pinned in hooks/__tests__/use-chat.draft-resubscribe.test.ts).
const { chatStub } = vi.hoisted(() => ({
  chatStub: {
    turns: [] as unknown[],
    sendMessage: vi.fn(),
    stopGeneration: vi.fn(),
    regenerateLastTurn: vi.fn(),
    editAndResend: vi.fn(),
    loadHistory: vi.fn(),
    markHistoryUnavailable: vi.fn(),
    resubscribeSession: vi.fn(),
    isStreaming: false,
    connectionStatus: "connected",
  },
}))
vi.mock("@/hooks/use-chat", () => ({ useChat: () => chatStub }))
vi.mock("@/hooks/use-auth", () => ({
  useSession: () => ({ data: { user: { id: "user-1" } } }),
}))

// The panel's neighbours. Each opens its own surface (files, artifacts, the
// slash palette, the export dialog) and none of them is what this file is
// about.
vi.mock("@/components/features/chat/right-panel", () => ({ RightPanel: () => null }))
vi.mock("@/components/features/chat/right-rail", () => ({ RightRail: () => null }))
vi.mock("@/components/features/chat/right-drawer", () => ({ RightDrawer: () => null }))
vi.mock("@/components/features/chat/artifact/artifact-pane", () => ({ ArtifactPane: () => null }))
vi.mock("@/components/features/chat/composer/slash-palette", () => ({ SlashPalette: () => null }))
vi.mock("@/components/features/chat/search/conversation-search", () => ({ ConversationSearch: () => null }))
vi.mock("@/components/features/chat/export/export-dialog", () => ({ ExportDialog: () => null }))
vi.mock("@/components/features/chat/composer/mention-autocomplete", () => ({ MentionAutocomplete: () => null }))

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn(), info: vi.fn(), warning: vi.fn(), message: vi.fn() },
}))

// The desktop left column is the shared agent tree; SessionsSidebar is now the
// phone drawer only. What this suite watches for is unchanged — the page's own
// session list and which of them is active — so the stand-in moved to the
// component that receives them.
vi.mock("@/components/features/chat/chat-tree-sidebar", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/components/features/chat/chat-tree-sidebar")>()
  return {
    ...mod,
    ChatTreeSidebar: ({
      threadsByAgent,
      activeThreadId,
    }: {
      threadsByAgent: Record<string, { id: string }[]>
      activeThreadId: string | null
    }) => (
      <div
        data-testid="sidebar"
        data-active={activeThreadId ?? ""}
        data-ids={(threadsByAgent["agent-1"] ?? []).map((s) => s.id).join(",")}
      />
    ),
  }
})

import { ChatPageClient } from "../chat-page-client"

const mockAgents = [
  {
    id: "agent-1", name: "Filip", slug: "filip", status: "IDLE", role_title: "Data Analyst",
    avatar_seed: "filip", avatar_style: null, suggested_prompts: null, ask_forms: null,
    crew: { name: "Research", slug: "research", avatar_style: null },
  },
]

/** Every POST this render made to the chat-create endpoint. */
let chatPosts: { url: string; body: Record<string, unknown> }[] = []
let existingChats: Record<string, unknown>[] = []
/** Chats the fake server has messages for. Anything else is an unknown chat. */
let serverMessages: Record<string, { id: string; role: string; content: string; ts: string }[]> = {}

function installFetch() {
  global.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    const u = String(url)
    const method = (init?.method ?? "GET").toUpperCase()

    // ── GET /api/v1/chats/{id}/messages ────────────────────────────────────
    // proxy.go: a chat that does not exist is NOT a 404 — it is 200 with an
    // empty list. This one line is the whole reason the suite exists.
    if (u.includes("/messages")) {
      const id = u.split("/chats/")[1].split("/")[0]
      return { ok: true, status: 200, json: async () => ({ messages: serverMessages[id] ?? [] }) } as unknown as Response
    }
    if (u.includes("/participants")) {
      return { ok: true, status: 200, json: async () => ({ participants: [] }) } as unknown as Response
    }
    if (u.includes("/api/v1/agents") && !u.includes("/chats") && !u.includes("/files")) {
      return { ok: true, status: 200, json: async () => mockAgents } as unknown as Response
    }
    if (u.includes("/chats") && method === "POST") {
      const body = JSON.parse(String(init?.body ?? "{}"))
      chatPosts.push({ url: u, body })
      return { ok: true, status: 201, json: async () => ({ id: body.session_id ?? "server-minted-1" }) } as unknown as Response
    }
    if (u.includes("/chats") && method === "GET") {
      return { ok: true, status: 200, json: async () => existingChats } as unknown as Response
    }
    return { ok: true, status: 200, json: async () => ({}) } as unknown as Response
  }) as unknown as typeof fetch
}

async function renderSettled() {
  const view = render(<ChatPageClient />)
  await waitFor(() => expect(screen.getByTestId("ask-rail")).toBeInTheDocument(), { timeout: 3000 })
  // The history GET has answered — this is the response that used to convince
  // the panel the row already existed.
  await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())
  return view
}

/** The id the panel is looking at, read off the composer's own session. */
function activeSessionId(): string {
  return screen.getByTestId("sidebar").getAttribute("data-active") ?? ""
}

/** Send, the way a user with an empty chat in front of them does it. */
async function clickFirstSuggestion() {
  fireEvent.click(await screen.findByTestId("ask-chip-question-0"))
}

describe("<ChatPageClient> — a session is created by sending, not by arriving", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    chatPosts = []
    existingChats = []
    serverMessages = {}
    searchParams = new URLSearchParams()
    Object.defineProperty(window, "location", {
      configurable: true, writable: true,
      value: { ...window.location, pathname: "/chat/filip", search: "" },
    })
    installFetch()
  })

  afterEach(() => { vi.restoreAllMocks() })

  it("POSTs nothing when the page is opened with no session and no prompt", async () => {
    await renderSettled()
    // Give any mount effect that wanted to create one a chance to fire.
    await new Promise((r) => setTimeout(r, 50))
    expect(chatPosts).toEqual([])
  })

  it("still hands the composer a session to write into", async () => {
    await renderSettled()
    // The "Allocating session…" placeholder was the state the old mount POST
    // existed to escape. Not creating must not mean not being able to type.
    expect(screen.queryByText(/Allocating session/)).not.toBeInTheDocument()
    expect(activeSessionId()).toBeTruthy()
  })

  it("creates exactly one chat on the first send, keyed on the draft id", async () => {
    await renderSettled()
    const draftId = activeSessionId()

    await clickFirstSuggestion()

    await waitFor(() => expect(chatPosts).toHaveLength(1))
    expect(chatPosts[0].body).toMatchObject({ session_id: draftId, origin: "UI" })
    expect(chatPosts[0].url).toContain("/api/v1/agents/agent-1/chats")

    // The message goes out only after the row exists, and the channel is
    // re-taken now that the authorizer has something to resolve.
    await waitFor(() => expect(chatStub.sendMessage).toHaveBeenCalledTimes(1))
    expect(chatStub.resubscribeSession).toHaveBeenCalledTimes(1)

    // And the thread the user just started is now a real row in the list.
    await waitFor(() =>
      expect(screen.getByTestId("sidebar").getAttribute("data-ids")).toBe(draftId),
    )
  })

  it("does not create it twice when a second message follows", async () => {
    await renderSettled()

    await clickFirstSuggestion()
    await waitFor(() => expect(chatPosts).toHaveLength(1))
    await clickFirstSuggestion()
    await waitFor(() => expect(chatStub.sendMessage).toHaveBeenCalledTimes(2))

    expect(chatPosts).toHaveLength(1)
  })

  it("puts the session in the URL once it is real, so a reload comes back to it", async () => {
    const replaceSpy = vi.spyOn(window.history, "replaceState")
    await renderSettled()
    const draftId = activeSessionId()

    // Before the first send the URL stays clean: a draft id in the address bar
    // would survive a reload and point at a conversation that was never created.
    expect(
      replaceSpy.mock.calls.some((c) => typeof c[2] === "string" && c[2].includes("session=")),
    ).toBe(false)

    await clickFirstSuggestion()

    await waitFor(() =>
      expect(
        replaceSpy.mock.calls.some(
          (c) => typeof c[2] === "string" && c[2] === `/chat/filip?session=${draftId}`,
        ),
      ).toBe(true),
    )
  })

  it("selects the freshest existing session instead of drafting a new one", async () => {
    existingChats = [
      { id: "chat-newest", title: "Yesterday", status: "ACTIVE", message_count: 3, started_at: "2026-08-11T09:00:00Z", ended_at: null },
      { id: "chat-older", title: "Last week", status: "ENDED", message_count: 9, started_at: "2026-08-04T09:00:00Z", ended_at: null },
    ]
    serverMessages["chat-newest"] = [
      { id: "m1", role: "user", content: "yesterday", ts: "2026-08-11T09:00:00.000Z" },
    ]
    await renderSettled()
    await new Promise((r) => setTimeout(r, 50))

    expect(activeSessionId()).toBe("chat-newest")
    expect(chatPosts).toEqual([])
  })

  it("skips the create for a session whose real messages it loaded", async () => {
    // A conversation the user is coming back to: the history proves the row is
    // there, so the first message of this visit costs no POST.
    existingChats = [
      { id: "chat-newest", title: "Yesterday", status: "ACTIVE", message_count: 3, started_at: "2026-08-11T09:00:00Z", ended_at: null },
    ]
    serverMessages["chat-newest"] = [
      { id: "m1", role: "user", content: "yesterday", ts: "2026-08-11T09:00:00.000Z" },
    ]
    await renderSettled()

    await clickFirstSuggestion()

    await waitFor(() => expect(chatStub.sendMessage).toHaveBeenCalledTimes(1))
    expect(chatPosts).toEqual([])
  })

  it("still creates a fresh session up front for the ?prompt= handoff", async () => {
    // routine-create-dialog sends the user here with the goal pre-typed and
    // expects it to auto-send into a conversation of its own — never into
    // whatever thread happened to be open last.
    existingChats = [
      { id: "chat-newest", title: "Yesterday", status: "ACTIVE", message_count: 3, started_at: "2026-08-11T09:00:00Z", ended_at: null },
    ]
    searchParams = new URLSearchParams("prompt=Author%20a%20routine%20for%20me")

    await renderSettled()
    await waitFor(() => expect(chatPosts.length).toBeGreaterThan(0))
    expect(chatPosts[0].body).toMatchObject({ origin: "UI" })
    expect(chatPosts[0].body.session_id).toBeUndefined()

    // The page created the row; the panel's auto-send re-asserts it with the
    // id it was handed. That second POST is an INSERT OR IGNORE that writes
    // nothing (internal/api/agent_chats.go) — the panel confirms the row it is
    // about to send into rather than assuming somebody else made one, which is
    // exactly the assumption that lost conversations.
    await waitFor(() => expect(chatStub.sendMessage).toHaveBeenCalledWith("Author a routine for me"))
    expect(chatPosts).toHaveLength(2)
    expect(chatPosts[1].body).toMatchObject({ session_id: "server-minted-1", origin: "UI" })
    expect(activeSessionId()).toBe("server-minted-1")
  })

  it("does not send, and creates no sidebar row, when the create fails", async () => {
    await renderSettled()
    const draftId = activeSessionId()
    const realFetch = global.fetch as unknown as ReturnType<typeof vi.fn>
    global.fetch = vi.fn(async (url: string, init?: RequestInit) => {
      if (String(url).includes("/chats") && (init?.method ?? "GET").toUpperCase() === "POST") {
        chatPosts.push({ url: String(url), body: JSON.parse(String(init?.body ?? "{}")) })
        return { ok: false, status: 500, json: async () => ({ error: "nope" }) } as unknown as Response
      }
      return realFetch(url, init)
    }) as unknown as typeof fetch

    await clickFirstSuggestion()

    await waitFor(() => expect(chatPosts).toHaveLength(1))
    await new Promise((r) => setTimeout(r, 50))
    // Nothing may pretend this worked: the message is not sent, the sidebar
    // gains no row for a chat that does not exist, and the URL keeps no id
    // that a reload would fail to find.
    expect(chatStub.sendMessage).not.toHaveBeenCalled()
    expect(screen.getByTestId("sidebar").getAttribute("data-ids")).toBe("")
    expect(activeSessionId()).toBe(draftId)
  })
})
