import { beforeEach, describe, expect, it, vi } from "vitest"
import { act, render } from "@testing-library/react"
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

const sound = vi.hoisted(() => ({ play: vi.fn(async () => true), complete: (_reply: { sessionId: string; repliedAt: string }) => {} }))
vi.mock("@/lib/notification-sound-coordinator", () => ({ playSoundOnce: sound.play }))
vi.mock("@/hooks/use-chat", () => ({ useChat: (options: { onReplyCompleted: typeof sound.complete }) => { sound.complete = options.onReplyCompleted; return chatStub } }))
vi.mock("@/hooks/use-auth", () => ({
  useSessionSafe: () => ({ data: { user: { id: "user-1" } } }), useSession: () => ({ data: { user: { id: "user-1" } } }),
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


beforeEach(() => {
  vi.clearAllMocks()
  global.fetch = vi.fn(async () => new Response(JSON.stringify({ messages: [], participants: [] }), { status: 200 }))
})
describe("direct agent completion sound wiring", () => {
  it("delivers a canonical chat sound even in the focused chat and aborts on session change", () => {
    const view = render(<ChatPanel {...panelProps} />)
    expect(sound.play).not.toHaveBeenCalled()
    const repliedAt = "2026-09-08T09:00:00.123Z"
    act(() => sound.complete({ sessionId: "draft-1", repliedAt }))
    expect(sound.play).toHaveBeenCalledWith(JSON.stringify(["user-1", "ws-test"]), {
      key: `agent-reply:draft-1:${Date.parse(repliedAt)}`, category: "chat",
    }, expect.any(Function))
    const current = sound.play.mock.calls[0][2] as () => boolean
    expect(current()).toBe(true)
    view.rerender(<ChatPanel {...panelProps} sessionId="other" />)
    expect(current()).toBe(false)
    act(() => sound.complete({ sessionId: "draft-1", repliedAt }))
    expect(sound.play).toHaveBeenCalledTimes(1)
  })
  it("does not notify after unmount or for routine/issue sessions", () => {
    const view = render(<ChatPanel {...panelProps} />)
    act(() => sound.complete({ sessionId: "draft-1", repliedAt: "2026-09-08T09:00:00.123Z" }))
    const current = sound.play.mock.calls[0][2] as () => boolean
    view.unmount()
    expect(current()).toBe(false)
    sound.play.mockClear()
    render(<ChatPanel {...panelProps} sessionKind="routine" />)
    act(() => sound.complete({ sessionId: "draft-1", repliedAt: "2026-09-08T09:00:00.123Z" }))
    expect(sound.play).not.toHaveBeenCalled()
  })
})
