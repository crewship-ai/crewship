import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

// =============================================================================
// Where the ask forms come from.
//
// `useAskForms` fetched `GET /agents/{id}` on every chat mount for the sake of
// ONE column, `ask_forms` — a column the page above already has, because it
// resolves this agent out of the roster it fetched for the tree and hands down
// `suggested_prompts` and `role_title` from the same record. That was one
// avoidable request per mount, on the hottest surface in the product.
//
// The forms now ride the same path the suggestions do. The hook's own fetch
// stays, and stays tested, because it is what a caller with no agent record
// still needs — it is just no longer the way the chat page gets there.
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

// The panel's neighbours. Each opens its own surface (files, artifacts, the
// slash palette, the export dialog) and none of them is what this file is
// about.
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

const receiptForm = JSON.stringify([
  {
    id: "receipt",
    label: "Add a receipt",
    template: "Zaúčtuj fakturu od {{supplier}}",
    attachment: "required",
    fields: [{ name: "supplier", label: "Supplier", type: "text", required: true }],
  },
])

/** Every `GET /api/v1/agents/{id}` the mount made — the request this feature
 *  used to add. The agent's own chats/files endpoints live under
 *  `/agents/{id}/…` and are somebody else's business. */
function agentDetailCalls(): string[] {
  const f = global.fetch as unknown as { mock: { calls: unknown[][] } }
  return f.mock.calls
    .map((c) => String(c[0]))
    .filter((u) => /\/api\/v1\/agents\/[^/?]+(\?|$)/.test(u))
}

const panelProps = {
  agentId: "agent-1",
  sessionId: "sess-1",
  agentName: "Riley",
  agentSlug: "riley",
}

describe("ChatPanel: the ask forms come from the agent record the page already had", () => {
  beforeEach(() => {
    global.fetch = vi.fn((url: string) => {
      const u = String(url)
      if (u.includes("/messages")) {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ messages: [] }) }) as unknown as Promise<Response>
      }
      if (u.includes("/participants")) {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ participants: [] }) }) as unknown as Promise<Response>
      }
      if (/\/api\/v1\/agents\/[^/?]+(\?|$)/.test(u)) {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ id: "agent-1", ask_forms: receiptForm }) }) as unknown as Promise<Response>
      }
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve([]) }) as unknown as Promise<Response>
    }) as unknown as typeof fetch
  })

  it("renders the rail from the record, without asking the server for the agent again", async () => {
    render(<ChatPanel {...panelProps} askForms={receiptForm} />)

    await waitFor(() => expect(screen.getByTestId("ask-chip-form-receipt")).toBeInTheDocument())
    expect(agentDetailCalls()).toEqual([])
  })

  it("asks for nothing when the record says the agent has no forms", async () => {
    render(<ChatPanel {...panelProps} askForms={null} />)

    await waitFor(() => expect(screen.getByTestId("ask-rail")).toBeInTheDocument())
    expect(screen.queryByTestId("ask-chip-form-receipt")).toBeNull()
    expect(agentDetailCalls()).toEqual([])
  })

  it("still fetches for a caller that has no agent record — the hook's fallback stays", async () => {
    render(<ChatPanel {...panelProps} />)

    await waitFor(() => expect(screen.getByTestId("ask-chip-form-receipt")).toBeInTheDocument())
    expect(agentDetailCalls()).toHaveLength(1)
  })
})
