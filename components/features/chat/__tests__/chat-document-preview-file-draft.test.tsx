import { useState, type ReactNode } from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"

// Reuses chat-panel-artifacts-entry fixtures. Stateful mock proves the parent
// lifetime boundary only; this is not an actual CodeMirror or browser test.
vi.mock("../virtual-conversation", () => ({ virtualChatEnabled: () => false, VirtualConversation: ({ footer }: { footer?: ReactNode }) => <>{footer}</> }))
const chat = vi.hoisted(() => ({
  turns: [{ id: "saved-turn", role: "assistant", parts: [], timestamp: new Date() }],
  sendMessage: vi.fn(), stopGeneration: vi.fn(), regenerateLastTurn: vi.fn(), editAndResend: vi.fn(),
  loadHistory: vi.fn(), markHistoryUnavailable: vi.fn(), resubscribeSession: vi.fn(),
  isStreaming: false, connectionStatus: "connected",
}))
vi.mock("@/hooks/use-chat", () => ({ useChat: () => chat }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "user-1" } } }), useSession: () => ({ data: { user: { id: "user-1" } } }) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
vi.mock("../right-rail", () => ({ RightRail: () => null }))
vi.mock("../right-drawer", () => ({ RightDrawer: ({ children }: { children: ReactNode }) => children }))
vi.mock("../right-panel", () => ({ RightPanel: function ContextDraft() {
  const [draft, setDraft] = useState("")
  return <textarea aria-label="Context draft" value={draft} onChange={event => setDraft(event.target.value)} />
} }))
vi.mock("../artifact/artifact-pane", () => ({ ArtifactPane: function FileDraft() {
  const [draft, setDraft] = useState("")
  const open = useArtifactStore(state => state.open)
  return open ? <textarea aria-label="File draft" value={draft} onChange={event => setDraft(event.target.value)} /> : null
} }))
vi.mock("../composer/slash-palette", () => ({ SlashPalette: () => null }))
vi.mock("../composer/chat-composer", () => ({ ChatComposer: () => <textarea aria-label="Composer" /> }))
vi.mock("../turn-renderer", () => ({ TurnRenderer: () => null }))
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }))

import { ChatPanel } from "../chat-panel"
import { useArtifactStore } from "@/stores/artifact-store"
import { useDrawerStore } from "@/stores/drawer-store"

beforeEach(() => {
  useArtifactStore.getState().closeAll()
  useDrawerStore.setState({ open: true, activeTab: "artifacts" })
  global.fetch = vi.fn((input: RequestInfo | URL) => {
    const path = String(input)
    const value = path.includes("/documents/status") ? { enabled: true }
      : path.includes("/document-results") ? { results: [{ page_id: "saved-document", type: "document", page_slug: "old-address", title: "Creation title", revision: 0, created_at: "2026-10-17T00:00:00Z", agent_id: "agent-1", agent_name: "Riley", run_id: "ended-run", on_behalf_of_user_id: "user-1" }] }
      : path.startsWith("/api/v1/pages/%40saved-document/document?") ? { page_id: "saved-document", schema: 2, rev: 7, content: { type: "doc", content: [{ type: "paragraph", attrs: { id: "saved-body" }, content: [{ type: "text", text: "Saved current body" }] }] } }
      : path.startsWith("/api/v1/pages/%40saved-document?") ? { id: "saved-document", name: "Current saved title", has_document: true }
      : path.includes("/execution-profile") ? { mode: "trusted" }
      : path.includes("/restricted-files") ? { files: [] }
      : path.includes("/messages") ? { messages: [] }
      : path.includes("/participants") ? { participants: [] } : {}
    return Promise.resolve(new Response(JSON.stringify(value)))
  }) as typeof fetch
})
afterEach(cleanup)

it.each(["File draft", "Context draft"])("retains the same unsaved %s input across saved Document open/close", async label => {
  render(<ChatPanel agentId="agent-1" agentSlug="riley" sessionId="sess-1" askForms={null} />)
  const card = await screen.findByRole("link", { name: "Open document Creation title" })
  if (label === "File draft") act(() => useArtifactStore.getState().openFile({ id: "draft-file", agentId: "agent-1", path: "draft.txt", title: "draft.txt" }))
  const input = await screen.findByRole("textbox", { name: label })
  fireEvent.change(input, { target: { value: "Unsaved work stays in this mounted child" } })
  expect(input).toHaveValue("Unsaved work stays in this mounted child")
  fireEvent.click(card)
  expect(await screen.findByRole("heading", { name: "Current saved title" })).toBeInTheDocument()
  expect(screen.getByText("Saved current body")).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Close document preview" }))
  expect(screen.queryByRole("region", { name: "Document preview" })).not.toBeInTheDocument()
  expect(screen.getByRole("textbox", { name: label })).toBe(input)
  expect(input).toHaveValue("Unsaved work stays in this mounted child")
})
