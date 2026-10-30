import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"

vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test", loading: false }) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "human-a" } } }) }))
vi.mock("../attachment-zone", () => ({
  AttachmentZone: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  AttachmentButton: () => null, CameraButton: () => null,
  EnsureChatSessionProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}))
vi.mock("../mention-autocomplete", () => ({ MentionAutocomplete: () => null }))
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }))

import { ChatComposer } from "../chat-composer"
import { useComposerStore } from "@/stores/composer-store"

const send = vi.fn()
const props = { agentId: "agent-a", sessionId: "chat-a", variant: "desktop" as const,
  isStreaming: false, connectionStatus: "connected", stopGeneration: vi.fn(),
  ensureSession: async () => true, sendMessage: send, documentCreationEnabled: true }

beforeEach(() => { send.mockReset(); useComposerStore.setState({ attachments: {}, drafts: {} }) })
afterEach(() => { vi.unstubAllEnvs() })

describe("composer + › New artifact", () => {
  it("is absent while the flag is off", () => {
    render(<ChatComposer {...props} />)
    expect(screen.queryByRole("button", { name: "New artifact" })).toBeNull()
  })

  it("is absent when no kind has a backend", () => {
    vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true")
    render(<ChatComposer {...props} documentCreationEnabled={false} />)
    expect(screen.queryByRole("button", { name: "New artifact" })).toBeNull()
  })

  it("offers only Word today and sends its starter with explicit document consent", async () => {
    vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true")
    render(<ChatComposer {...props} />)
    fireEvent.click(screen.getByRole("button", { name: "New artifact" }))
    expect(screen.queryByText("Project")).toBeNull()
    fireEvent.click(await screen.findByRole("button", { name: /A partner brief from this chat/ }))
    const textarea = screen.getByRole("textbox")
    expect(textarea).toHaveValue("A partner brief from this chat")
    expect(screen.getByRole("button", { name: "Remove New Word" })).toBeInTheDocument()
    fireEvent.submit(textarea.closest("form")!)
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1))
    expect(send.mock.calls[0][1]).toEqual({ document_creation_intent: { submission_id: expect.any(String), drafting: true } })
  })

  it("keeps what the person already typed and puts the starter in front", async () => {
    vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true")
    render(<ChatComposer {...props} />)
    const textarea = screen.getByRole("textbox")
    fireEvent.change(textarea, { target: { value: "Use the Q3 numbers" } })
    fireEvent.click(screen.getByRole("button", { name: "New artifact" }))
    fireEvent.click(await screen.findByRole("button", { name: /A partner brief from this chat/ }))
    expect(textarea).toHaveValue("A partner brief from this chat\n\nUse the Q3 numbers")
  })

  it.each(["desktop", "mobile"] as const)("starts a project with explicit consent and shows it as one chip on %s", async variant => {
    vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true")
    render(<ChatComposer {...props} variant={variant} artifactCreationEnabled />)
    // The menu replaces the toolbar's text toggles.
    expect(screen.queryByRole("button", { name: "Create artifact project" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Create document" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "New artifact" }))
    fireEvent.click(await screen.findByRole("button", { name: /A three-screen pricing site/ }))
    expect(screen.getByRole("button", { name: "Remove New Project" })).toBeInTheDocument()
    expect(screen.getByText("Private project · Sharing requires your approval")).toBeInTheDocument()
    fireEvent.submit(screen.getByRole("textbox").closest("form")!)
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1))
    expect(send.mock.calls[0][0]).toContain("A three-screen pricing site")
    expect(send.mock.calls[0][1]).toEqual({ artifact_project_intent: { submission_id: expect.any(String) } })
    // Consent covers one message: the chip is gone after sending.
    await waitFor(() => expect(screen.queryByRole("button", { name: "Remove New Project" })).toBeNull())
  })

  it("removing the chip withdraws consent", async () => {
    vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true")
    render(<ChatComposer {...props} artifactCreationEnabled />)
    fireEvent.click(screen.getByRole("button", { name: "New artifact" }))
    fireEvent.click(await screen.findByRole("button", { name: /A three-screen pricing site/ }))
    fireEvent.click(screen.getByRole("button", { name: "Remove New Project" }))
    fireEvent.submit(screen.getByRole("textbox").closest("form")!)
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1))
    expect(send.mock.calls[0][1]).toBeUndefined()
  })
})
