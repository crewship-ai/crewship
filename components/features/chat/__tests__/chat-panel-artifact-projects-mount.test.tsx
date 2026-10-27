import { useState, type ReactNode } from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { cleanup, render, screen, waitFor } from "@testing-library/react"

// With the projects flag on, verifying the chat's read scope must not remount
// the chat: the composer keeps its local state (draft, attachments).
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
vi.mock("../turn-renderer", () => ({ TurnRenderer: () => null }))
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }))
const mounts = vi.hoisted(() => ({ count: 0 }))
vi.mock("../composer/chat-composer", () => ({ ChatComposer: () => {
  const [id] = useState(() => ++mounts.count)
  return <span data-testid="composer">{id}</span>
} }))

import { ChatPanel } from "../chat-panel"

const json = (value: unknown) => new Response(JSON.stringify(value), { status: 200, headers: { "content-type": "application/json" } })
const calls: string[] = []

beforeEach(() => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true")
  mounts.count = 0
  calls.length = 0
  global.fetch = vi.fn((input: RequestInfo | URL) => {
    const path = String(input)
    calls.push(path)
    const value = path.includes("/pages/artifacts/availability") ? { enabled: true }
      : path.includes("/execution-profile") ? { mode: "trusted" }
      : path.includes("/messages") ? { messages: [] }
      : path.includes("/participants") ? { participants: [] } : {}
    return Promise.resolve(json(value))
  }) as typeof fetch
})
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

it("keeps the chat mounted while the read scope is verified", async () => {
  render(<ChatPanel agentId="agent-1" agentSlug="riley" agentName="Riley" sessionId="sess-1" askForms={null} />)
  // Availability is asked only once the scope is verified, so it marks the flip.
  await waitFor(() => expect(calls.some(p => p.includes("/pages/artifacts/availability"))).toBe(true))
  expect(screen.getByTestId("composer").textContent).toBe("1")
  expect(mounts.count).toBe(1)
})
