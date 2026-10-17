vi.mock("../artifact-result-cards", () => ({ ArtifactResultCards: () => null }))
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, act } from "@testing-library/react"
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
vi.mock("../artifact/artifact-pane", () => ({ ArtifactPane: ({ onAskSelection }: { onAskSelection?: (text: string) => void }) => {
  const open = useArtifactStore((state) => state.open)
  return open ? <div data-testid="artifact-pane"><button onClick={() => onAskSelection?.("Selected source A2:B2")}>Ask about test selection</button></div> : null
} }))
vi.mock("../composer/slash-palette", () => ({ SlashPalette: () => null }))
vi.mock("../composer/chat-composer", () => ({ ChatComposer: ({ contextRequest }: { contextRequest?: { text: string } }) => <output data-testid="composer-context">{contextRequest?.text}</output> }))
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
  global.fetch = vi.fn((url: string) => Promise.resolve({ ok: true, status: 200, json: async () => String(url).includes("/execution-profile") ? { mode: "trusted" } : String(url).includes("/restricted-files") ? { files: [] } : String(url).includes("/messages") ? { messages: [] } : String(url).includes("/participants") ? { participants: [] } : {} })) as unknown as typeof fetch
})
afterEach(cleanup)

describe("chat artifact entry points", () => {
  it("hides cached agent-wide artifacts while the conversation authority is pending", () => {
    render(<ChatPanel {...props} mobilePanel="artifacts" />)
    expect(screen.queryByTestId("context-tab")).not.toBeInTheDocument()
    expect(screen.getByText("Checking conversation access…")).toBeInTheDocument()
  })
  it("uses classified conversation files for restricted mobile artifact access", async () => {
    global.fetch = vi.fn((url: string) => Promise.resolve({ ok: true, status: 200, json: async () => String(url).includes("/execution-profile") ? { mode: "restricted" } : String(url).includes("/restricted-files") ? { files: [] } : String(url).includes("/messages") ? { messages: [] } : {} })) as unknown as typeof fetch
    render(<ChatPanel {...props} mobilePanel="artifacts" />)
    await screen.findByText("Output files")
    expect(screen.queryByTestId("context-tab")).not.toBeInTheDocument()
    expect(screen.getByText("No output files in this conversation.")).toBeInTheDocument()
  })
  it("opens Files on the mobile context page", async () => {
    render(<ChatPanel {...props} mobilePanel="artifacts" />)
    expect(await screen.findByTestId("context-tab")).toHaveTextContent("artifacts")
  })
  it("opens Work through the mobile Work page", async () => {
    render(<ChatPanel {...props} mobilePanel="work" />)
    expect(await screen.findByTestId("context-tab")).toHaveTextContent("work")
  })
  it("returns a mobile selection to the unobscured composer without losing its context", async () => {
    const changePanel = vi.fn()
    const view = render(<ChatPanel {...props} mobilePanel="artifacts" onMobilePanelChange={changePanel} />)
    await screen.findByTestId("context-tab")
    act(() => useArtifactStore.getState().openFile({ id: "source", agentId: "agent-1", path: "report.csv", title: "report.csv" }))
    fireEvent.click(screen.getByRole("button", { name: "Ask about test selection" }))
    expect(changePanel).toHaveBeenCalledWith("chat")
    expect(useArtifactStore.getState().open).toBe(false)
    expect(useArtifactStore.getState().tabs).toHaveLength(1)
    view.rerender(<ChatPanel {...props} mobilePanel="chat" onMobilePanelChange={changePanel} />)
    expect(screen.getByTestId("composer-context")).toHaveTextContent("Selected source A2:B2")
    expect(screen.queryByTestId("artifact-pane")).not.toBeInTheDocument()
  })
  it.each(["chat", "work"] as const)("keeps an open artifact from covering the mobile %s panel", async (panel) => {
    const view = render(<ChatPanel {...props} mobilePanel="artifacts" />)
    await screen.findByTestId("context-tab")
    act(() => useArtifactStore.getState().openFile({ id: "source", agentId: "agent-1", path: "report.csv", title: "report.csv" }))
    expect(screen.getByTestId("artifact-pane")).toBeInTheDocument()
    view.rerender(<ChatPanel {...props} mobilePanel={panel} />)
    expect(screen.queryByTestId("artifact-pane")).not.toBeInTheDocument()
    expect(useArtifactStore.getState().tabs).toHaveLength(1)
    view.rerender(<ChatPanel {...props} mobilePanel="artifacts" />)
    expect(screen.getByTestId("artifact-pane")).toBeInTheDocument()
  })
  it("keeps a desktop preview open when adding selected context", async () => {
    const changePanel = vi.fn()
    render(<ChatPanel {...props} onMobilePanelChange={changePanel} />)
    await screen.findByTestId("context-tab")
    act(() => useArtifactStore.getState().openFile({ id: "source", agentId: "agent-1", path: "report.csv", title: "report.csv" }))
    fireEvent.click(screen.getByRole("button", { name: "Ask about test selection" }))
    expect(useArtifactStore.getState().open).toBe(true)
    expect(screen.getByTestId("artifact-pane")).toBeInTheDocument()
    expect(screen.getByTestId("composer-context")).toHaveTextContent("Selected source A2:B2")
    expect(changePanel).not.toHaveBeenCalled()
  })
  it("opens a generated PDF in the artifact pane", async () => {
    chatStub.turns = [{ id: "turn-1", role: "assistant", parts: [], timestamp: new Date() }]
    render(<ChatPanel {...props} />)
    await screen.findByTestId("context-tab")
    fireEvent.click(await screen.findByRole("button", { name: "Preview generated PDF" }))
    expect(useArtifactStore.getState().activeId).toBe("agent-1:reports/result.pdf")
    expect(useDrawerStore.getState().activeTab).toBe("artifacts")
  })
  it("does not open agent configuration from a transcript link", async () => {
    chatStub.turns = [{ id: "turn-1", role: "assistant", parts: [], timestamp: new Date() }]
    render(<ChatPanel {...props} />)
    await screen.findByTestId("context-tab")
    fireEvent.click(await screen.findByRole("button", { name: "Preview agent configuration" }))
    expect(useArtifactStore.getState().open).toBe(false)
    expect(toastError).toHaveBeenCalledWith("This file is not available in Files")
  })
})
