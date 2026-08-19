import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, waitFor } from "@testing-library/react"

// =============================================================================
// The last hop of the ask-envelope chain.
//
// A questionnaire submission carries a structured envelope — which form, which
// version, which answers, and which upload answered which field — alongside the
// ordinary readable message. Every other hop was built and tested: the sheet
// builds it, the composer forwards it, use-chat puts it on the wire, the ws
// layer carries it, chatbridge persists it onto conversation.Message.Metadata,
// the JSONL store round-trips it, and the history endpoint returns it.
//
// And then the panel threw it away. Its local HistoryMessage type never named
// `metadata`, so the field was dropped in the map into loadHistory — while the
// renderer was already calling askProvenanceForTurn, reading a field no code
// path could hand it. Every reload lost the provenance the server had kept.
//
// This test exists at the panel boundary and not in use-chat because that is
// where the drop was: the shape flowing INTO loadHistory is the thing under
// test, so it asserts on what the panel passes rather than on what renders.
// =============================================================================

const chatStub = {
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
vi.mock("../composer/slash-palette", () => ({ SlashPalette: () => null }))
vi.mock("../search/conversation-search", () => ({ ConversationSearch: () => null }))
vi.mock("../export/export-dialog", () => ({ ExportDialog: () => null }))
vi.mock("../composer/mention-autocomplete", () => ({ MentionAutocomplete: () => null }))

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() },
}))

import { ChatPanel } from "../chat-panel"

/** The envelope shape the ask sheet produces, as chatbridge stores it. */
const askSubmission = {
  submission_id: "sub_7f3a",
  form_id: "receipt",
  form_label: "Add a receipt",
  form_version: 1,
  values: { supplier: "Acme", amount: "12.50" },
  field_attachment_ids: { document: ["attachments/chat-1/att_1/invoice.pdf"] },
  rendered_text: "Please file this receipt.",
}

/** The history endpoint's answer: the server has always returned metadata. */
function historyResponse() {
  return {
    messages: [
      {
        id: "m1",
        role: "user",
        content: "Please file this receipt.",
        ts: "2026-08-18T10:00:00Z",
        metadata: { ask_submission: askSubmission },
      },
      {
        id: "m2",
        role: "assistant",
        content: "Filed.",
        ts: "2026-08-18T10:00:04Z",
      },
    ],
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes("/messages")) {
        return new Response(JSON.stringify(historyResponse()), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      }
      // Participants, agent detail and anything else the panel probes on
      // mount: empty is fine, none of it is what this file is about.
      return new Response(JSON.stringify({}), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })
    }),
  )
})

function renderPanel() {
  return render(
    <ChatPanel
      agentId="agent-1"
      sessionId="chat-1"
      agentName="Riley"
      agentSlug="riley"
    />,
  )
}

describe("chat panel history → loadHistory", () => {
  it("carries a user message's metadata through, so a reload keeps the ask envelope", async () => {
    renderPanel()

    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())

    const [messages] = chatStub.loadHistory.mock.calls.at(-1) as [
      Array<{ id: string; metadata?: Record<string, unknown> }>,
    ]
    const user = messages.find((m) => m.id === "m1")

    expect(user?.metadata).toEqual({ ask_submission: askSubmission })
  })

  it("leaves a message without metadata undefined rather than inventing an empty object", async () => {
    // An empty object is not the same answer as "this message carried nothing",
    // and a reader that treats {} as present would show provenance for a plain
    // message.
    renderPanel()

    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())

    const [messages] = chatStub.loadHistory.mock.calls.at(-1) as [
      Array<{ id: string; metadata?: Record<string, unknown> }>,
    ]

    expect(messages.find((m) => m.id === "m2")?.metadata).toBeUndefined()
  })

  it("still passes the fields it always did", async () => {
    // A regression guard, green before this change and after: the fix is one
    // added key, and it must not have disturbed the mapping around it.
    renderPanel()

    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())

    const [messages] = chatStub.loadHistory.mock.calls.at(-1) as [
      Array<{ id: string; role: string; content: string; timestamp: Date }>,
    ]
    const user = messages.find((m) => m.id === "m1")!

    expect(user.role).toBe("user")
    expect(user.content).toBe("Please file this receipt.")
    expect(user.timestamp).toBeInstanceOf(Date)
  })
})
