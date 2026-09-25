import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"
import type { ReactNode } from "react"

const chatStub = vi.hoisted(() => ({
  turns: [] as unknown[], sendMessage: vi.fn(), stopGeneration: vi.fn(),
  regenerateLastTurn: vi.fn(), editAndResend: vi.fn(), loadHistory: vi.fn(),
  markHistoryUnavailable: vi.fn(), resubscribeSession: vi.fn(),
  isStreaming: false, connectionStatus: "connected",
}))
vi.mock("@/hooks/use-chat", () => ({ useChat: () => chatStub }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "user-1" } } }), useSession: () => ({ data: { user: { id: "user-1" } } }) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
vi.mock("../right-rail", () => ({ RightRail: () => null }))
vi.mock("../right-drawer", () => ({ RightDrawer: ({ children }: { children: ReactNode }) => children }))
vi.mock("../artifact/artifact-pane", () => ({ ArtifactPane: () => null }))
vi.mock("../composer/slash-palette", () => ({ SlashPalette: () => null }))
vi.mock("../composer/chat-composer", () => ({ ChatComposer: () => null }))
vi.mock("../right-panel", () => ({ RightPanel: ({ initialTab }: { initialTab: string }) => <div data-testid="context-tab">{initialTab}</div> }))
vi.mock("../turn-renderer", () => ({ TurnRenderer: ({ onFileClick }: { onFileClick: (path: string) => void }) => <>
  <button onClick={() => onFileClick("reports/result.pdf")}>Preview generated PDF</button>
  <button onClick={() => onFileClick("runs/msg_1/AGENTS.md")}>Preview agent configuration</button>
</> }))
const toastError = vi.fn()
vi.mock("sonner", () => ({ toast: { error: (...args: unknown[]) => toastError(...args), success: vi.fn(), info: vi.fn() } }))

import { ChatPanel } from "../chat-panel"
import { useDrawerStore } from "@/stores/drawer-store"
import { useArtifactStore } from "@/stores/artifact-store"
const props = { agentId: "agent-1", agentSlug: "riley", sessionId: "sess-1", askForms: null }

beforeEach(() => {
  chatStub.turns = []
  toastError.mockReset()
  useDrawerStore.setState({ open: false, activeTab: "artifacts" })
  useArtifactStore.getState().closeAll()
  global.fetch = vi.fn((url: string) => Promise.resolve({ ok: true, status: 200, json: async () => String(url).includes("/messages") ? { messages: [] } : String(url).includes("/participants") ? { participants: [] } : {} })) as unknown as typeof fetch
})
afterEach(cleanup)

describe("chat artifact entry points", () => {
  it("opens Artifacts on the mobile context page", () => {
    render(<ChatPanel {...props} mobilePanel="artifacts" />)
    expect(screen.getByTestId("context-tab")).toHaveTextContent("artifacts")
  })
  it("opens Work through the mobile Work page", () => {
    render(<ChatPanel {...props} mobilePanel="work" />)
    expect(screen.getByTestId("context-tab")).toHaveTextContent("work")
  })
  it("opens a generated PDF in the artifact pane", async () => {
    chatStub.turns = [{ id: "turn-1", role: "assistant", parts: [], timestamp: new Date() }]
    render(<ChatPanel {...props} />)
    fireEvent.click(await screen.findByRole("button", { name: "Preview generated PDF" }))
    expect(useArtifactStore.getState().activeId).toBe("agent-1:reports/result.pdf")
    expect(useDrawerStore.getState().activeTab).toBe("artifacts")
  })
  it("does not open agent configuration from a transcript link", async () => {
    chatStub.turns = [{ id: "turn-1", role: "assistant", parts: [], timestamp: new Date() }]
    render(<ChatPanel {...props} />)
    fireEvent.click(await screen.findByRole("button", { name: "Preview agent configuration" }))
    expect(useArtifactStore.getState().open).toBe(false)
    expect(toastError).toHaveBeenCalledWith("This file is not available in Artifacts")
  })
})
