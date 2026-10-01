import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

const state = vi.hoisted(() => ({ user: "h1", workspace: "w1", send: vi.fn(), fetch: vi.fn() }))
vi.mock("@/hooks/use-chat", () => ({ useChat: () => ({ turns: [], sendMessage: state.send, stopGeneration: vi.fn(), regenerateLastTurn: vi.fn(), editAndResend: vi.fn(), loadHistory: vi.fn(), markHistoryUnavailable: vi.fn(), resubscribeSession: vi.fn(), isStreaming: false, connectionStatus: "connected" }) }))
vi.mock("@/hooks/use-auth", () => ({ useSession: () => ({ data: { user: { id: state.user } } }), useSessionSafe: () => ({ data: { user: { id: state.user } } }) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: state.workspace, loading: false }) }))
vi.mock("@/lib/api-fetch", async importOriginal => ({ ...await importOriginal<typeof import("@/lib/api-fetch")>(), apiFetch: state.fetch }))
vi.mock("../right-panel", () => ({ RightPanel: () => null }))
vi.mock("../right-rail", () => ({ RightRail: () => null }))
vi.mock("../right-drawer", () => ({ RightDrawer: () => null }))
vi.mock("../artifact/artifact-pane", () => ({ ArtifactPane: () => null }))
vi.mock("../composer/slash-palette", () => ({ SlashPalette: () => null }))
vi.mock("../search/conversation-search", () => ({ ConversationSearch: () => null }))
vi.mock("../export/export-dialog", () => ({ ExportDialog: () => null }))
vi.mock("../files/restricted-files", () => ({ RestrictedFiles: () => null }))
vi.mock("../restricted-memory", () => ({ RestrictedMemory: () => null }))
vi.mock("../composer/chat-composer", () => ({ ChatComposer: ({ sendMessage, onSent }: { sendMessage: (text: string) => boolean; onSent: () => void }) => <button type="button" onClick={() => { if (sendMessage("Read the selected brief") !== false) onSent() }}>Send fixture message</button> }))

import { ChatPanel } from "../chat-panel"

beforeEach(() => {
  vi.clearAllMocks()
  state.user = "h1"; state.workspace = "w1"
  state.send.mockReturnValue(true)
  state.fetch.mockImplementation(async (url: string) => ({ ok: true, status: 200, json: async () => {
    if (url.includes("execution-profile")) return { mode: "restricted", audience: "private" }
    if (url.includes("project-input-options")) return { files: [{ version_id: `version-${state.user}`, name: `${state.user} brief.txt`, project_name: "Own project", size_bytes: 5 }], has_more: false }
    if (url.includes("participants")) return { participants: [] }
    return { messages: [] }
  } }))
})

describe("project source selection in the chat panel", () => {
  it.each([undefined, "chat"] as const)("forwards the explicit selection and clears it after sending (%s)", async mobilePanel => {
    render(<ChatPanel agentId="agent" sessionId="chat1" agentName="Native" agentSlug="native" mobilePanel={mobilePanel} />)
    const checkbox = await screen.findByRole("checkbox")
    expect(checkbox).not.toBeChecked()
    fireEvent.click(checkbox)
    fireEvent.click(screen.getByRole("button", { name: "Send fixture message" }))
    expect(state.send).toHaveBeenCalledWith("Read the selected brief", { project_file_versions: ["version-h1"] })
    await waitFor(() => expect(checkbox).not.toBeChecked())
  })

  it("prepares a fresh conversation without a model call before selecting the first message inputs", async () => {
    let created = false
    state.fetch.mockImplementation(async (url: string, options?: RequestInit) => {
      if (options?.method === "POST") { created = true; return { ok: true, status: 201, json: async () => ({}) } }
      if (url.includes("execution-profile")) return { ok: created, status: created ? 200 : 404, json: async () => ({ mode: "restricted", audience: "private" }) }
      if (url.includes("project-input-options")) return { ok: true, status: 200, json: async () => ({ files: [{ version_id: "first-version", name: "First brief.txt", project_name: "Own project", size_bytes: 5 }], has_more: false }) }
      return { ok: false, status: 404, json: async () => ({}) }
    })
    render(<ChatPanel agentId="agent" sessionId="fresh" agentName="Native" agentSlug="native" />)
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument()
    expect(state.fetch.mock.calls.some(([url]) => String(url).includes("project-input-options"))).toBe(false)
    fireEvent.click(await screen.findByRole("button", { name: "Prepare project inputs" }))
    fireEvent.click(await screen.findByRole("checkbox"))
    expect(state.send).not.toHaveBeenCalled()
    expect(state.fetch.mock.calls.filter(([, options]) => options?.method === "POST")).toHaveLength(1)
    fireEvent.click(screen.getByRole("button", { name: "Send fixture message" }))
    expect(state.send).toHaveBeenCalledWith("Read the selected brief", { project_file_versions: ["first-version"] })
  })

  it("does not carry a selection between authenticated users or conversations", async () => {
    const { rerender } = render(<ChatPanel agentId="agent" sessionId="chat1" agentName="Native" agentSlug="native" />)
    fireEvent.click(await screen.findByRole("checkbox"))
    state.user = "h2"
    rerender(<ChatPanel agentId="agent" sessionId="chat2" agentName="Native" agentSlug="native" />)
    const checkbox = await screen.findByRole("checkbox")
    await screen.findByText("h2 brief.txt")
    expect(screen.queryByText("h1 brief.txt")).not.toBeInTheDocument()
    expect(checkbox).not.toBeChecked()
    fireEvent.click(screen.getByRole("button", { name: "Send fixture message" }))
    expect(state.send).toHaveBeenLastCalledWith("Read the selected brief")
  })
})
