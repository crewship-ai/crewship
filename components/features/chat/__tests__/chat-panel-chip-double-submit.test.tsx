import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// =============================================================================
// #2121 — two chip clicks during one session-create window both send.
//
// `handleSuggestionClick` (chat-panel.tsx) reads `isStreaming` — a prop from
// `useChat` that cannot change until a send produces a render — BEFORE
// `await ensureSessionForSend()`. On a draft session that await is a real
// POST, so two clicks land inside the same open window and both pass the
// guard, both await, and both send. These chips do not go through
// `useMessageSubmit`, so the `submittingRef` latch from #2075 (pinned for the
// composer by chat-panel-session-create.test.tsx) does not cover them.
//
// The fix (issue's decision, option 3): a `creatingSession` state flips true
// synchronously — before the await — and disables the chip rail until the
// create settles, extending the existing `isStreaming` disable backwards.
// Mirrors the harness in chat-panel-session-create.test.tsx (same mocks, same
// `holdCreate` device to keep the create POST open for as long as a test
// needs).
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

// Same stand-in set as chat-panel-session-create.test.tsx — none of these
// surfaces is what this file is about.
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

let creates: { url: string; body: Record<string, unknown> }[] = []
/** Set to keep the create POST in flight for as long as a test needs. */
let holdCreate: Promise<void> | null = null

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
      if (holdCreate) await holdCreate
      return { ok: true, status: 201, json: async () => ({ id: body.session_id }) } as unknown as Response
    }
    return { ok: true, status: 200, json: async () => [] } as unknown as Response
  }) as unknown as typeof fetch
}

async function firstChip() {
  return screen.findByTestId("ask-chip-question-0")
}

describe("ChatPanel — chip double-click during session create (#2121)", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    creates = []
    holdCreate = null
    installFetch()
  })

  it("sends exactly once when a chip is clicked twice while the create is still in flight", async () => {
    let release!: () => void
    holdCreate = new Promise<void>((resolve) => { release = resolve })

    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())

    const chip = await firstChip()
    fireEvent.click(chip)
    await waitFor(() => expect(creates).toHaveLength(1))

    // Second press, inside the window the create POST is holding open. A
    // disabled native button does not dispatch a click at all — this is the
    // same assertion as the composer's "pressed twice" test, expressed
    // through the rail instead of the submit button.
    fireEvent.click(chip)
    release()

    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
    expect(creates).toHaveLength(1)
    expect(toastError).not.toHaveBeenCalled()
  })

  it("disables the chip rail while the session create is in flight", async () => {
    let release!: () => void
    holdCreate = new Promise<void>((resolve) => { release = resolve })

    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())

    const chip = await firstChip()
    expect(chip).not.toBeDisabled()

    fireEvent.click(chip)
    await waitFor(() => expect(creates).toHaveLength(1))
    // Still mounted (turns stays empty in the stub) and now visibly inert.
    await waitFor(() => expect(screen.getByTestId("ask-chip-question-0")).toBeDisabled())

    release()
    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
    // The window closes once the create settles — a real user's next chip
    // click (a different question) must still go through.
    await waitFor(() => expect(screen.getByTestId("ask-chip-question-0")).not.toBeDisabled())
  })

  it("does not gate a second click once the session already exists", async () => {
    // No hold this time — the create resolves on its own microtask queue,
    // so the window a real double-click could land in is at its narrowest.
    // The guard must not leave the rail stuck disabled afterwards.
    render(<ChatPanel {...panelProps} />)
    await waitFor(() => expect(chatStub.loadHistory).toHaveBeenCalled())

    fireEvent.click(await firstChip())
    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(screen.getByTestId("ask-chip-question-0")).not.toBeDisabled())

    fireEvent.click(screen.getByTestId("ask-chip-question-0"))
    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(2))
    // Second send reused the existing row — no second create POST.
    expect(creates).toHaveLength(1)
  })
})
