import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// =============================================================================
// The first send must create the `chats` row — and the panel may not infer
// that the row already exists from an empty history.
//
// GET /api/v1/chats/{id}/messages answers **200 with an empty message list**
// for a chat that does not exist at all:
//
//     // internal/api/proxy.go, ChatMessages
//     if errors.Is(err, sql.ErrNoRows) {
//         // Chat doesn't exist yet (new session before first message)
//         writeJSON(w, http.StatusOK, map[string]interface{}{"messages": []interface{}{}})
//
// That is deliberate and shared with the CLI (`crewship history --prompts`,
// `export`, `recap` all read the same endpoint), so it is not moving. What
// moved is the panel: it used to read "not a 404" as "the row exists", set its
// sessionReady flag, and then skip the create POST entirely on the first send.
// The result on dev2 was silent data loss — no `chats` row, no persisted
// messages (the WS channel authorizer refuses a send for a session with no
// row: internal/ws/channel_auth.go isSessionOwner), an auto-title PATCH into
// the void, and a sidebar stuck on "Untitled session".
//
// So: existence is something this panel has CONFIRMED, by creating the row or
// by loading real messages for it — never something inferred from an empty
// list. On the first send of a session it POSTs, once, and the redundant POST
// for a row that already exists is free (INSERT OR IGNORE,
// internal/api/agent_chats.go CreateChat).
//
// The mock below therefore answers exactly as proxy.go does. A mock that
// disagrees with the server is a test that certifies a bug — this suite exists
// because the previous one did.
// =============================================================================

const resubscribeSession = vi.fn()
const sendMessage = vi.fn()
const chatStub = {
  turns: [] as unknown[],
  sendMessage,
  stopGeneration: vi.fn(),
  regenerateLastTurn: vi.fn(),
  editAndResend: vi.fn(),
  loadHistory: vi.fn(),
  markHistoryUnavailable: vi.fn(),
  resubscribeSession,
  isStreaming: false,
  connectionStatus: "connected",
}

vi.mock("@/hooks/use-chat", () => ({ useChat: () => chatStub }))
vi.mock("@/hooks/use-auth", () => ({
  useSession: () => ({ data: { user: { id: "user-1" } } }),
}))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

// The panel's neighbours — each opens its own surface and none of them is what
// this file is about. Same stand-in set as chat-panel-ask-forms.test.tsx.
vi.mock("../right-panel", () => ({ RightPanel: () => null }))
vi.mock("../right-rail", () => ({ RightRail: () => null }))
vi.mock("../right-drawer", () => ({ RightDrawer: () => null }))
vi.mock("../artifact/artifact-pane", () => ({ ArtifactPane: () => null }))
vi.mock("../composer/slash-palette", () => ({ SlashPalette: () => null }))
vi.mock("../search/conversation-search", () => ({ ConversationSearch: () => null }))
vi.mock("../export/export-dialog", () => ({ ExportDialog: () => null }))
vi.mock("../composer/mention-autocomplete", () => ({ MentionAutocomplete: () => null }))

const { toastError } = vi.hoisted(() => ({ toastError: vi.fn() }))
vi.mock("sonner", () => ({
  toast: { error: toastError, success: vi.fn(), info: vi.fn(), warning: vi.fn(), message: vi.fn() },
}))

import { ChatPanel } from "../chat-panel"

const panelProps = {
  agentId: "agent-1",
  sessionId: "draft-1",
  agentName: "Riley",
  agentSlug: "riley",
  agentRole: "Data Analyst",
  askForms: null,
}

/** Rows the fake server has. Anything else is an unknown chat, and an unknown
 *  chat answers 200 + `{"messages": []}` — exactly like proxy.go. */
let serverMessages: Record<string, { id: string; role: string; content: string; ts: string }[]> = {}
let createStatus = 201
let creates: { url: string; body: Record<string, unknown> }[] = []

function installFetch() {
  creates = []
  global.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    const u = String(url)
    const method = (init?.method ?? "GET").toUpperCase()

    if (u.includes("/messages")) {
      const id = u.split("/chats/")[1].split("/")[0]
      return {
        ok: true, status: 200,
        json: async () => ({ messages: serverMessages[id] ?? [] }),
      } as unknown as Response
    }
    if (u.includes("/participants")) {
      return { ok: true, status: 200, json: async () => ({ participants: [] }) } as unknown as Response
    }
    if (u.includes("/chats") && method === "POST") {
      const body = JSON.parse(String(init?.body ?? "{}"))
      creates.push({ url: u, body })
      if (createStatus >= 400) {
        return { ok: false, status: createStatus, json: async () => ({ error: "nope" }) } as unknown as Response
      }
      return { ok: true, status: createStatus, json: async () => ({ id: body.session_id }) } as unknown as Response
    }
    return { ok: true, status: 200, json: async () => [] } as unknown as Response
  }) as unknown as typeof fetch
}

/** The cold-start rail's first question chip: clicking it sends. */
async function firstChip() {
  const chip = await screen.findByTestId("ask-chip-question-0")
  return chip
}

describe("ChatPanel — the first send creates the row, whatever the history said", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    serverMessages = {}
    createStatus = 201
    installFetch()
  })

  it("POSTs the chat on the first send even though the history came back 200/empty", async () => {
    render(<ChatPanel {...panelProps} />)
    // Let the history GET settle — this is the response that used to convince
    // the panel the row already existed.
    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())
    expect(creates).toEqual([])

    fireEvent.click(await firstChip())

    await waitFor(() => expect(creates).toHaveLength(1))
    expect(creates[0].url).toContain("/api/v1/agents/agent-1/chats")
    expect(creates[0].url).toContain("workspace_id=ws-test")
    expect(creates[0].body).toMatchObject({ session_id: "draft-1", origin: "UI" })
    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
  })

  it("creates the row once per session, not once per message", async () => {
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())

    fireEvent.click(await firstChip())
    await waitFor(() => expect(creates).toHaveLength(1))
    // The rail is still on screen (the stubbed useChat reports no turns), so a
    // second chip is a second send into the same session.
    fireEvent.click(await firstChip())
    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(2))

    expect(creates).toHaveLength(1)
  })

  it("takes the session channel exactly once, after the row exists", async () => {
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())
    expect(resubscribeSession).not.toHaveBeenCalled()

    fireEvent.click(await firstChip())
    await waitFor(() => expect(resubscribeSession).toHaveBeenCalledTimes(1))

    fireEvent.click(await firstChip())
    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(2))
    expect(resubscribeSession).toHaveBeenCalledTimes(1)
  })

  it("skips the POST for a session whose real messages it has loaded", async () => {
    serverMessages["chat-old"] = [
      { id: "m1", role: "user", content: "yesterday", ts: "2026-08-12T09:00:00.000Z" },
    ]
    render(<ChatPanel {...panelProps} sessionId="chat-old" />)
    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())

    // No cold-start rail with turns on screen in the real panel, so drive the
    // same handler the composer does — the suggestion rail is rendered because
    // the stubbed useChat reports no turns.
    fireEvent.click(await firstChip())

    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
    // A history with real messages IS proof the row exists; nothing to create.
    expect(creates).toEqual([])
  })

  it("does not send — and says so — when the row could not be created", async () => {
    createStatus = 500
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())

    fireEvent.click(await firstChip())

    await waitFor(() => expect(creates).toHaveLength(1))
    await waitFor(() => expect(toastError).toHaveBeenCalledTimes(1))
    // The message must NOT go out: the WS authorizer would refuse it for a
    // session with no row, and the user would be left believing it was saved.
    expect(sendMessage).not.toHaveBeenCalled()
    expect(resubscribeSession).not.toHaveBeenCalled()
  })

  it("retries the create on the next send after a failure", async () => {
    createStatus = 500
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())

    fireEvent.click(await firstChip())
    await waitFor(() => expect(creates).toHaveLength(1))

    // A failed create must not latch: the row still does not exist, so the
    // next send has to try again rather than assume it is there.
    createStatus = 201
    fireEvent.click(await firstChip())
    await waitFor(() => expect(creates).toHaveLength(2))
    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
  })
})
