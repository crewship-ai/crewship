import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// =============================================================================
// `chat_session_created` fires where the row is actually created.
//
// The panel creates the `chats` row lazily: a draft session has no row until
// the first send (or the first attachment) needs one, and `ensureSession`
// POSTs it then. That POST is the moment a conversation begins on this
// surface, so it is the moment the event describes — `source: "composer"`,
// because typing is what started it.
//
// Three properties, each one a way the number could lie:
//
//   · once per session, not once per message — the panel POSTs once and the
//     event has to follow the POST, not the send;
//   · never for a create that failed — there is no conversation, and a
//     funnel that counts refused creates as starts is not a funnel;
//   · nothing about the message reaches the event. The panel has the draft
//     text in hand at exactly this moment.
//
// The create behaviour itself is pinned in chat-panel-session-create.test.tsx;
// this file is only about the event that now rides along with it.
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

vi.mock("../right-panel", () => ({ RightPanel: () => null }))
vi.mock("../right-rail", () => ({ RightRail: () => null }))
vi.mock("../right-drawer", () => ({ RightDrawer: () => null }))
vi.mock("../artifact/artifact-pane", () => ({ ArtifactPane: () => null }))
// Partial: the panel also imports constants from this module (the palette
// shortcut label), and a total mock would drop them.
vi.mock("../composer/slash-palette", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../composer/slash-palette")>()),
  SlashPalette: () => null,
}))
vi.mock("../search/conversation-search", () => ({ ConversationSearch: () => null }))
vi.mock("../export/export-dialog", () => ({ ExportDialog: () => null }))
vi.mock("../composer/mention-autocomplete", () => ({ MentionAutocomplete: () => null }))

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn(), info: vi.fn(), warning: vi.fn(), message: vi.fn() },
}))

import { ChatPanel } from "../chat-panel"
import { resetChatTelemetry, setChatTelemetrySink, type ChatEvent } from "@/lib/telemetry"

const panelProps = {
  agentId: "agent-1",
  sessionId: "draft-1",
  agentName: "Riley",
  agentSlug: "riley",
  agentRole: "Data Analyst",
  askForms: null,
}

let createStatus = 201
let creates: { url: string; body: Record<string, unknown> }[] = []
let events: ChatEvent[] = []

const created = () => events.filter((e) => e.name === "chat_session_created")

function installFetch() {
  creates = []
  global.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    const u = String(url)
    const method = (init?.method ?? "GET").toUpperCase()
    if (u.includes("/messages")) {
      return { ok: true, status: 200, json: async () => ({ messages: [] }) } as unknown as Response
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
const firstChip = () => screen.findByTestId("ask-chip-question-0")

describe("ChatPanel — the create that starts a conversation is measured", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    createStatus = 201
    installFetch()
    resetChatTelemetry()
    events = []
    setChatTelemetrySink((e) => events.push(e))
  })

  it("emits chat_session_created once, when the row is created", async () => {
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())
    expect(created()).toEqual([])

    fireEvent.click(await firstChip())

    await waitFor(() => expect(creates).toHaveLength(1))
    await waitFor(() => expect(created()).toHaveLength(1))
    expect(created()[0].payload).toEqual({
      session_id: "draft-1",
      agent_id: "agent-1",
      source: "composer",
    })
  })

  it("does not emit again on the second message of the same session", async () => {
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())

    fireEvent.click(await firstChip())
    await waitFor(() => expect(created()).toHaveLength(1))

    fireEvent.click(await firstChip())
    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(2))
    expect(creates).toHaveLength(1)
    expect(created()).toHaveLength(1)
  })

  it("emits nothing when the row could not be created", async () => {
    createStatus = 500
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())

    fireEvent.click(await firstChip())

    await waitFor(() => expect(creates).toHaveLength(1))
    await new Promise((r) => setTimeout(r, 50))
    // No row, no conversation, no start.
    expect(created()).toEqual([])
  })

  it("carries no text into telemetry — the chip's own words included", async () => {
    render(<ChatPanel {...panelProps} suggestedPrompts={"What is still unpaid at Vodafone?"} />)
    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())

    fireEvent.click(await firstChip())
    await waitFor(() => expect(created()).toHaveLength(1))

    const serialized = JSON.stringify(created())
    expect(serialized).not.toContain("Vodafone")
    expect(serialized).not.toContain("unpaid")
  })
})
