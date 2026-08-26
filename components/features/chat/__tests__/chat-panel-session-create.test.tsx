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
import { useComposerStore } from "@/stores/composer-store"

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
/** Set to keep the create POST in flight for as long as a test needs. */
let holdCreate: Promise<void> | null = null

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
      // The create is a round trip, and the panel is fully interactive for the
      // whole of it. Tests that care about what happens DURING it hold it here.
      if (holdCreate) await holdCreate
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
    holdCreate = null
    installFetch()
  })

  // Pressing Send on a draft session starts a POST, and until it answers the
  // composer looks exactly as it did — the draft is still in the box (it is
  // only cleared once the send is away) and the button is still live. So the
  // natural response to a slow create is to press Send again, and both
  // presses used to get through: every guard in useMessageSubmit is
  // synchronous and already behind them by the time either one is waiting.
  //
  // The row itself was never duplicated — `createInFlightRef` collapses the
  // POSTs and the endpoint is an upsert — but the MESSAGE was, twice into the
  // same conversation. The latch is in the composer's submit path, so this
  // covers the main chat's shape of the window (a real round trip) and the
  // onboarding suite covers the other (a wait for the transcript base).
  it("sends once when Send is pressed twice while the create is still in flight", async () => {
    let release!: () => void
    holdCreate = new Promise<void>((resolve) => { release = resolve })

    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())

    const input = screen.getByRole("textbox")
    fireEvent.change(input, { target: { value: "what changed yesterday?" } })
    const submit = screen.getByRole("button", { name: /submit/i })
    fireEvent.click(submit)
    await waitFor(() => expect(creates).toHaveLength(1))

    // Second press, inside the window the POST is holding open.
    fireEvent.click(submit)
    release()

    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
    expect(sendMessage).toHaveBeenCalledWith("what changed yesterday?")
    expect(creates).toHaveLength(1)
    expect(toastError).not.toHaveBeenCalled()
    // And the composer is not wedged: the next message still goes.
    await waitFor(() => expect((input as HTMLTextAreaElement).value).toBe(""))
    fireEvent.change(input, { target: { value: "and today?" } })
    fireEvent.click(submit)
    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(2))
    expect(sendMessage).toHaveBeenLastCalledWith("and today?")
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

  // The create is reached by TWO callers, and only one of them is a send.
  //
  // Attaching a file creates the row the same way the first message does —
  // composer/attachment-zone.tsx runs ensureSession before it uploads a byte,
  // because the attachments endpoint 404s without the row. So a failed create
  // during an upload produced the accurate per-file toast ("receipt.pdf was not
  // attached … press Retry") AND this one, which said "your message wasn't
  // sent" about a message the user never wrote.
  //
  // The composer takes ONE ensureSession prop and hands the same function to
  // both paths (composer/chat-composer.tsx: useMessageSubmit and
  // EnsureChatSessionProvider), so the panel cannot word it per caller. The
  // wording therefore has to be true for both: the conversation could not be
  // started. What did not happen next — no message, no attachment — is said by
  // the caller that knows, which for the upload is the per-file toast and the
  // error chip.
  it("does not claim a message was sent when the create fails during an upload", async () => {
    createStatus = 500
    useComposerStore.setState({ attachments: {}, drafts: {} })
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())

    const input = document.querySelector<HTMLInputElement>(
      'input[type="file"]:not([aria-label])',
    )!
    const file = new File(["payload"], "receipt.pdf", { type: "application/pdf" })
    Object.defineProperty(input, "files", { value: [file], configurable: true })
    fireEvent.change(input)

    // The create was attempted and refused — this is the upload path reaching
    // ensureSession, not a send.
    await waitFor(() => expect(creates).toHaveLength(1))
    await waitFor(() => expect(toastError).toHaveBeenCalled())

    const said = toastError.mock.calls
      .map(([title, opts]) => `${title} ${JSON.stringify(opts ?? {})}`)
      .join("\n")

    // Nothing may describe a message. The user attached a file.
    expect(said).not.toMatch(/message/i)
    expect(said).not.toMatch(/wasn't sent|was not sent/i)
    // And the thing that DID fail is still named.
    expect(said).toMatch(/conversation/i)
    expect(said).toContain("receipt.pdf")
    expect(sendMessage).not.toHaveBeenCalled()
  })

  // The send path keeps its own toast, and it must still be one the user can
  // act on — the create is the only thing that failed, so that is what it says.
  it("says the conversation could not be started when a send is refused", async () => {
    createStatus = 500
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())

    fireEvent.click(await firstChip())

    await waitFor(() => expect(toastError).toHaveBeenCalledTimes(1))
    const [title, opts] = toastError.mock.calls[0]
    const said = `${title} ${JSON.stringify(opts ?? {})}`
    expect(said).toMatch(/conversation/i)
    expect(said).toMatch(/try again/i)
    expect(said).not.toMatch(/message/i)
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
