import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"
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
vi.mock("../right-panel", () => ({ RightPanel: (props: {
  files: { name: string }[], filesError?: string, previewFile?: { path: string }, onPreviewHandled?: (request: { path: string }) => void, onRetryFiles: () => void, initialTab: string,
}) => <div>
  <span>{props.initialTab}</span>
  {props.files.map((file) => <span key={file.name}>{file.name}</span>)}
  {props.filesError && <button onClick={props.onRetryFiles}>Retry files</button>}
  {props.previewFile && <><span data-testid="preview">{props.previewFile.path}</span><button onClick={() => props.onPreviewHandled?.(props.previewFile!)}>Finish preview</button></>}
</div> }))
vi.mock("../turn-renderer", () => ({ TurnRenderer: ({ onFileClick }: { onFileClick: (path: string) => void }) =>
  <button onClick={() => onFileClick("reports/result.md")}>Preview generated file</button> }))
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }))

import { ChatPanel } from "../chat-panel"
import { useDrawerStore } from "@/stores/drawer-store"
const props = { agentId: "agent-1", agentSlug: "riley", sessionId: "sess-1", askForms: null }
let failFiles = false
let fileRequests = 0
beforeEach(() => {
  failFiles = false
  fileRequests = 0
  chatStub.turns = []
  useDrawerStore.setState({ open: false, activeTab: "team" })
  global.fetch = vi.fn((url: string) => {
    const path = String(url)
    if (path.includes("/files?")) {
      fileRequests++
      return Promise.resolve({ ok: !failFiles, status: failFiles ? 500 : 200, json: async () => [{ name: "report.md" }] })
    }
    return Promise.resolve({ ok: true, status: 200, json: async () => path.includes("/messages") ? { messages: [] } : path.includes("/participants") ? { participants: [] } : {} })
  }) as unknown as typeof fetch
})
afterEach(cleanup)

describe("chat file entry points", () => {
  it("loads mobile Files even when the desktop drawer is closed on Team", async () => {
    render(<ChatPanel {...props} mobilePanel="files-only" />)
    expect(await screen.findByText("report.md")).toBeInTheDocument()
    expect(fileRequests).toBe(1)
  })
  it("retries a failed files request", async () => {
    failFiles = true
    render(<ChatPanel {...props} mobilePanel="files-only" />)
    const retry = await screen.findByRole("button", { name: "Retry files" })
    failFiles = false
    fireEvent.click(retry)
    expect(await screen.findByText("report.md")).toBeInTheDocument()
    expect(fileRequests).toBe(2)
  })
  it("opens a real file request from a transcript Preview", async () => {
    chatStub.turns = [{ id: "turn-1", role: "assistant", parts: [], timestamp: new Date() }]
    render(<ChatPanel {...props} />)
    fireEvent.click(await screen.findByRole("button", { name: "Preview generated file" }))
    expect(await screen.findByTestId("preview")).toHaveTextContent("reports/result.md")
    expect(useDrawerStore.getState().open).toBe(true)
    expect(useDrawerStore.getState().activeTab).toBe("files")
  })
  it("switches mobile Preview to the Files view", async () => {
    chatStub.turns = [{ id: "turn-1", role: "assistant", parts: [], timestamp: new Date() }]
    const change = vi.fn()
    render(<ChatPanel {...props} mobilePanel="chat" onMobilePanelChange={change} />)
    fireEvent.click(await screen.findByRole("button", { name: "Preview generated file" }))
    await waitFor(() => expect(change).toHaveBeenCalledWith("files"))
  })
  it("opens More on an available Team tab", () => {
    render(<ChatPanel {...props} mobilePanel="more" />)
    expect(screen.getByText("team")).toBeInTheDocument()
  })
})


it("consumes a handled preview before the drawer is reopened on Team", async () => {
  chatStub.turns = [{ id: "turn-1", role: "assistant", parts: [], timestamp: new Date() }]
  render(<ChatPanel {...props} />)
  fireEvent.click(await screen.findByRole("button", { name: "Preview generated file" }))
  fireEvent.click(await screen.findByRole("button", { name: "Finish preview" }))
  expect(screen.queryByTestId("preview")).not.toBeInTheDocument()
  const { act } = await import("@testing-library/react")
  act(() => { useDrawerStore.getState().setOpen(false) })
  act(() => { useDrawerStore.getState().setActiveTab("team") })
  expect(screen.getByText("team")).toBeInTheDocument()
  expect(screen.queryByTestId("preview")).not.toBeInTheDocument()
})
