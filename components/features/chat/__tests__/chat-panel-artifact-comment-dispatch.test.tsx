import type { ReactNode } from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, cleanup, render, waitFor } from "@testing-library/react"

// A comment saved for the agent in the side viewer starts the run in this
// chat: the panel sends the message bound to the comment through its own send.
vi.mock("../virtual-conversation", () => ({ virtualChatEnabled: () => false, VirtualConversation: ({ footer }: { footer?: ReactNode }) => <>{footer}</> }))
const chat = vi.hoisted(() => ({
  turns: [], sendMessage: vi.fn(), stopGeneration: vi.fn(), regenerateLastTurn: vi.fn(), editAndResend: vi.fn(),
  loadHistory: vi.fn(), markHistoryUnavailable: vi.fn(), resubscribeSession: vi.fn(),
  isStreaming: false, connectionStatus: "connected",
}))
vi.mock("@/hooks/use-chat", () => ({ useChat: () => chat }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "user-1" } } }), useSession: () => ({ data: { user: { id: "user-1" } } }) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
vi.mock("../right-rail", () => ({ RightRail: () => null }))
vi.mock("../right-drawer", () => ({ RightDrawer: ({ children }: { children: ReactNode }) => children }))
vi.mock("../right-panel", () => ({ RightPanel: () => null }))
vi.mock("../artifact/artifact-pane", () => ({ ArtifactPane: () => null }))
vi.mock("../composer/slash-palette", () => ({ SlashPalette: () => null }))
vi.mock("../composer/chat-composer", () => ({ ChatComposer: () => null }))
vi.mock("../turn-renderer", () => ({ TurnRenderer: () => null }))
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }))
const host = vi.hoisted(() => ({ sendToAgent: undefined as undefined | ((r: unknown) => Promise<boolean>) }))
vi.mock("../artifact-context/projects/artifact-viewer-context", () => ({
  ArtifactViewerProvider: ({ children, sendToAgent }: { children: ReactNode; sendToAgent?: (r: unknown) => Promise<boolean> }) => { host.sendToAgent = sendToAgent; return <>{children}</> },
}))

import { ChatPanel } from "../chat-panel"

const json = (value: unknown) => new Response(JSON.stringify(value), { status: 200, headers: { "content-type": "application/json" } })

beforeEach(() => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true")
  host.sendToAgent = undefined
  chat.sendMessage.mockReset()
  global.fetch = vi.fn((input: RequestInfo | URL) => {
    const path = String(input)
    return Promise.resolve(json(path.includes("/execution-profile") ? { mode: "trusted" } : path.includes("/messages") ? { messages: [] } : path.includes("/participants") ? { participants: [] } : {}))
  }) as typeof fetch
})
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

const comment = { id: "cm1", version: 2, path: "index.html", selector: "#plan-team", quote: "Team", text: "Make it stand out" }

it("sends the comment's message with its artifact project intent through this chat", async () => {
  chat.sendMessage.mockReturnValue(true)
  render(<ChatPanel agentId="agent-1" agentSlug="riley" agentName="Riley" sessionId="sess-1" askForms={null} />)
  await waitFor(() => expect(host.sendToAgent).toBeTypeOf("function"))
  let started: boolean | undefined
  await act(async () => { started = await host.sendToAgent!({ pageId: "page-1", comment }) })
  expect(started).toBe(true)
  expect(chat.sendMessage).toHaveBeenCalledTimes(1)
  const [text, metadata] = chat.sendMessage.mock.calls[0]
  expect(text).toContain("Requested change: Make it stand out")
  expect(metadata).toMatchObject({ artifact_project_intent: { submission_id: "artifact-comment-cm1", page_id: "page-1", comment_id: "cm1", base_version: 2 } })
})

it("reports that the agent did not start when the chat could not send", async () => {
  chat.sendMessage.mockReturnValue(false)
  render(<ChatPanel agentId="agent-1" agentSlug="riley" agentName="Riley" sessionId="sess-1" askForms={null} />)
  await waitFor(() => expect(host.sendToAgent).toBeTypeOf("function"))
  let started: boolean | undefined
  await act(async () => { started = await host.sendToAgent!({ pageId: "page-1", comment }) })
  expect(started).toBe(false)
})
