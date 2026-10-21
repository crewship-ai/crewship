import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// While identity is unknown (waiting for sign-in) nothing may send from the
// panel: not a suggestion chip, not the auto-sent handoff goal, not a proposal
// comment. All of them go through sendMessageWithPage.

const viewer = vi.hoisted(() => ({ id: "user-1" }))
const resubscribeSession = vi.fn()
const sendMessage = vi.fn()
const chatStub = {
  turns: [] as unknown[],
  sendMessage,
  stopGeneration: vi.fn(),
  regenerateLastTurn: vi.fn(),
  editAndResend: vi.fn(),
  loadHistory: vi.fn(),
  markHistoryUnavailable: vi.fn(), markHistoryDenied: vi.fn(),
  resubscribeSession,
  isStreaming: false,
  connectionStatus: "connected",
}

vi.mock("@/hooks/use-chat", () => ({ useChat: () => chatStub }))
vi.mock("@/hooks/use-auth", () => ({
  useSessionSafe: () => ({ data: { user: { id: viewer.id } } }), useSession: () => ({ data: { user: { id: viewer.id } } }),
}))
const hold = vi.hoisted(() => ({ waiting: false }))
vi.mock("@/hooks/use-attachment-scope", async (orig) => ({
  ...(await orig<typeof import("@/hooks/use-attachment-scope")>()),
  useAttachmentScope: (chatId: string) => ({
    prefix: "", key: chatId, ready: true, waitingForSignIn: hold.waiting, waitingReason: "person",
  }),
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

/** Rows the fake server has. Unknown chats use the status selected by each
 * test to cover both history-response contracts. */
let serverMessages: Record<string, { id: string; role: string; content: string; ts: string }[]> = {}
let createStatus = 201
let missingHistoryStatus = 200
let profileStatus: number | undefined
let profileMode = "trusted"
const createdRows = new Set<string>()
let creates: { url: string; body: Record<string, unknown> }[] = []
/** Set to keep the create POST in flight for as long as a test needs. */
let holdCreate: Promise<void> | null = null

function installFetch() {
  creates = []
  global.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    const u = String(url)
    const method = (init?.method ?? "GET").toUpperCase()

    if (u.includes("/execution-profile")) {
      const id = u.split("/chats/")[1].split("/")[0]
      const status = profileStatus ?? (serverMessages[id] || createdRows.has(id) ? 200 : 404)
      return { ok: status === 200, status, json: async () => status === 200 ? { mode: profileMode, audience: "group" } : { error: "Chat not found" } } as unknown as Response
    }
    if (u.includes("/messages")) {
      const id = u.split("/chats/")[1].split("/")[0]
      if (!serverMessages[id] && missingHistoryStatus === 404) {
        return { ok: false, status: 404, json: async () => ({ error: "Chat not found" }) } as unknown as Response
      }
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
      createdRows.add(body.session_id)
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


describe("ChatPanel — nothing sends while waiting for sign-in", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    serverMessages = {}
    profileStatus = undefined
    profileMode = "trusted"
    viewer.id = "user-1"
    createdRows.clear()
    createStatus = 201
    missingHistoryStatus = 200
    holdCreate = null
    hold.waiting = false
    installFetch()
  })

  it("a suggestion chip sends normally when identity is known", async () => {
    render(<ChatPanel {...panelProps} />)
    fireEvent.click(await firstChip())
    await waitFor(() => expect(sendMessage).toHaveBeenCalled())
  })

  it("a suggestion chip does not send, and creates no row, while waiting", async () => {
    hold.waiting = true
    render(<ChatPanel {...panelProps} />)
    fireEvent.click(await firstChip())
    await new Promise((r) => setTimeout(r, 60))
    expect(sendMessage).not.toHaveBeenCalled()
    expect(creates).toHaveLength(0)
  })

  it("the auto-sent handoff goal waits instead of sending", async () => {
    hold.waiting = true
    render(<ChatPanel {...panelProps} initialInput="Plan my week" autoSendInitial />)
    await new Promise((r) => setTimeout(r, 80))
    expect(sendMessage).not.toHaveBeenCalled()
    expect(creates).toHaveLength(0)
  })
})
