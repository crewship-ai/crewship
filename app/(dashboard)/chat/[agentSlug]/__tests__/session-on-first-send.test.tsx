import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// =============================================================================
// Arriving at a chat must not create a chat.
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
// (internal/api/agent_chats.go:234-268), a 404 from the history GET is
// explicitly "brand-new session, not an error" (chat-panel.tsx:194-206), and
// ChatPanel's own ensureSession() (chat-panel.tsx:288-298) fires that POST on
// the first send. So the page mints a draft id locally and hands it straight
// to the panel; the row appears in the database when — and only when — a
// message goes out.
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

// Stand-in for ChatPanel. The "send" button reproduces exactly what the real
// panel does on submit — ensureSession() POSTs the chat into existence with
// the id it was handed, then onSend() tells the page (chat-panel.tsx:288-298
// and hooks/use-message-submit.ts). Everything else about the panel (the WS,
// the turn list, the composer store) is irrelevant here and expensive.
vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: ({
    agentId, sessionId, initialInput, autoSendInitial, onSend,
  }: {
    agentId: string
    sessionId: string
    initialInput?: string
    autoSendInitial?: boolean
    onSend?: (sid: string, text: string) => void
  }) => (
    <div
      data-testid="chat-panel"
      data-agent-id={agentId}
      data-session-id={sessionId}
      data-initial-input={initialInput ?? ""}
      data-auto-send={autoSendInitial ? "1" : ""}
    >
      <button
        type="button"
        onClick={async () => {
          // Imported here rather than at module scope: this factory is
          // hoisted above the file's imports.
          const { apiFetch } = await import("@/lib/api-fetch")
          await apiFetch(`/api/v1/agents/${agentId}/chats?workspace_id=ws-test`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ session_id: sessionId, origin: "UI" }),
          })
          onSend?.(sessionId, "how is the deploy going")
        }}
      >
        send
      </button>
    </div>
  ),
}))

import { ChatPageClient } from "../chat-page-client"

const mockAgents = [
  {
    id: "agent-1", name: "Filip", slug: "filip", status: "IDLE", role_title: "Data Analyst",
    avatar_seed: "filip", avatar_style: null,
    crew: { name: "Research", slug: "research", avatar_style: null },
  },
]

/** Every POST this render made to the chat-create endpoint. */
let chatPosts: { url: string; body: Record<string, unknown> }[] = []
let existingChats: Record<string, unknown>[] = []

function installFetch() {
  global.fetch = vi.fn((url: string, init?: RequestInit) => {
    const u = String(url)
    if (u.includes("/api/v1/agents") && !u.includes("/chats")) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(mockAgents) }) as unknown as Promise<Response>
    }
    if (u.includes("/chats") && init?.method === "POST") {
      const body = JSON.parse(String(init.body ?? "{}"))
      chatPosts.push({ url: u, body })
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ id: body.session_id ?? "server-minted-1" }),
      }) as unknown as Promise<Response>
    }
    if (u.includes("/chats")) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(existingChats) }) as unknown as Promise<Response>
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve({}) }) as unknown as Promise<Response>
  }) as unknown as typeof fetch
}

async function renderSettled() {
  const view = render(<ChatPageClient />)
  await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument(), { timeout: 3000 })
  return view
}

describe("<ChatPageClient> — a session is created by sending, not by arriving", () => {
  beforeEach(() => {
    chatPosts = []
    existingChats = []
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
    expect(screen.getByTestId("chat-panel").getAttribute("data-session-id")).toBeTruthy()
  })

  it("creates exactly one chat on the first send, keyed on the draft id", async () => {
    await renderSettled()
    const draftId = screen.getByTestId("chat-panel").getAttribute("data-session-id")

    fireEvent.click(screen.getByRole("button", { name: "send" }))

    await waitFor(() => expect(chatPosts).toHaveLength(1))
    expect(chatPosts[0].body).toMatchObject({ session_id: draftId, origin: "UI" })
    expect(chatPosts[0].url).toContain("/api/v1/agents/agent-1/chats")

    // And the thread the user just started is now a real row in the list.
    await waitFor(() =>
      expect(screen.getByTestId("sidebar").getAttribute("data-ids")).toBe(draftId),
    )
  })

  it("puts the session in the URL once it is real, so a reload comes back to it", async () => {
    const replaceSpy = vi.spyOn(window.history, "replaceState")
    await renderSettled()
    const draftId = screen.getByTestId("chat-panel").getAttribute("data-session-id")

    // Before the first send the URL stays clean: a draft id in the address bar
    // would survive a reload and point at a chat that was never created.
    expect(
      replaceSpy.mock.calls.some((c) => typeof c[2] === "string" && c[2].includes("session=")),
    ).toBe(false)

    fireEvent.click(screen.getByRole("button", { name: "send" }))

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
    await renderSettled()
    await new Promise((r) => setTimeout(r, 50))

    expect(screen.getByTestId("chat-panel").getAttribute("data-session-id")).toBe("chat-newest")
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
    await waitFor(() => expect(chatPosts).toHaveLength(1))

    const panel = screen.getByTestId("chat-panel")
    expect(panel.getAttribute("data-session-id")).not.toBe("chat-newest")
    expect(panel.getAttribute("data-initial-input")).toBe("Author a routine for me")
    expect(panel.getAttribute("data-auto-send")).toBe("1")
  })
})
