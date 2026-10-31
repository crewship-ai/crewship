import { beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"

const { identity } = vi.hoisted(() => ({ identity: { id: "human-a" as string | null } }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test", loading: false }) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: identity.id ? { user: { id: identity.id } } : null }) }))
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
  ensureSession: async () => true, sendMessage: send, artifactCreationEnabled: true }
function submit(text: string) {
  const textarea = screen.getByRole("textbox")
  fireEvent.change(textarea, { target: { value: text } })
  fireEvent.submit(textarea.closest("form")!)
}
function selectCreation() { fireEvent.click(screen.getByRole("button", { name: "Create artifact project" })) }

beforeEach(() => { send.mockReset(); identity.id = "human-a"; useComposerStore.setState({ attachments: {}, drafts: {} }) })
describe("explicit artifact project creation consent", () => {
  it.each(["desktop", "mobile"] as const)("sends English or Czech prose unchanged with explicit metadata on %s", async variant => {
    render(<ChatComposer {...props} variant={variant} />)
    selectCreation()
    submit(variant === "desktop" ? "Write a garden plan. Include 3 sections!" : "Napiš mi plán zahrady.\nPřidej tři části!")
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1))
    const [text, metadata] = send.mock.calls[0]
    expect(text).toContain(variant === "desktop" ? "Write a garden plan." : "Napiš mi plán zahrady.")
    expect(metadata).toEqual({ artifact_project_intent: { submission_id: expect.stringMatching(/^[A-Za-z0-9_-]{1,128}$/) } })
    await waitFor(() => expect(screen.getByRole("button", { name: "Create artifact project" })).toHaveAttribute("aria-pressed", "false"))
    submit("Ordinary follow-up")
    await waitFor(() => expect(send).toHaveBeenCalledTimes(2))
    expect(send.mock.calls[1][1]).toBeUndefined()
  })
  it("does not infer consent from ordinary English prose", async () => {
    render(<ChatComposer {...props} />); submit("Please write a document.")
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1)); expect(send.mock.calls[0][1]).toBeUndefined()
  })
  it("retains the same consent ID after a refused send", async () => {
    send.mockReturnValueOnce(false)
    render(<ChatComposer {...props} />); selectCreation(); submit("Please create a plan.")
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1))
    expect(screen.getByRole("textbox")).toHaveValue("Please create a plan.")
    fireEvent.submit(screen.getByRole("textbox").closest("form")!)
    await waitFor(() => expect(send).toHaveBeenCalledTimes(2))
    expect(send.mock.calls[1][1]).toEqual(send.mock.calls[0][1])
  })
  it("uses a fresh consent ID if a refused message is edited", async () => {
    send.mockReturnValue(false)
    render(<ChatComposer {...props} />); selectCreation(); submit("First request.")
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1))
    submit("Changed request!")
    await waitFor(() => expect(send).toHaveBeenCalledTimes(2))
    expect(send.mock.calls[1][1].artifact_project_intent.submission_id).not.toBe(send.mock.calls[0][1].artifact_project_intent.submission_id)
  })
  it("does not carry consent across identity, conversation, or availability changes", () => {
    const view = render(<ChatComposer {...props} />); selectCreation()
    identity.id = "human-b"; view.rerender(<ChatComposer {...props} />)
    expect(screen.getByRole("button", { name: "Create artifact project" })).toHaveAttribute("aria-pressed", "false")
    selectCreation(); view.rerender(<ChatComposer {...props} sessionId="chat-b" />)
    expect(screen.getByRole("button", { name: "Create artifact project" })).toHaveAttribute("aria-pressed", "false")
    selectCreation(); view.rerender(<ChatComposer {...props} sessionId="chat-b" artifactCreationEnabled={false} />)
    expect(screen.queryByRole("button", { name: "Create artifact project" })).toBeNull()
    view.rerender(<ChatComposer {...props} sessionId="chat-b" />)
    expect(screen.getByRole("button", { name: "Create artifact project" })).toHaveAttribute("aria-pressed", "false")
  })
  it("offers no control when support is disabled or the user is anonymous", () => {
    const view = render(<ChatComposer {...props} artifactCreationEnabled={false} />)
    expect(screen.queryByRole("button", { name: "Create artifact project" })).toBeNull()
    identity.id = null; view.rerender(<ChatComposer {...props} />)
    expect(screen.queryByRole("button", { name: "Create artifact project" })).toBeNull()
  })
})

it.each(["desktop", "mobile"] as const)("shows the selected private artifact audience on %s", variant => {
 render(<ChatComposer {...props} variant={variant} />)
 expect(screen.queryByText(/Private project · Sharing/)).toBeNull()
 selectCreation()
 expect(screen.getByText("Private project · Sharing requires your approval")).toBeTruthy()
 selectCreation()
 expect(screen.queryByText(/Private project · Sharing/)).toBeNull()
})

describe("server refusal because the agent's crew is outside the artifact policy", () => {
  it("restores the typed message and names the reason instead of a generic failure", async () => {
    const view = render(<ChatComposer {...props} />)
    selectCreation(); submit("Build me a landing page")
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1))
    send.mockClear()
    // Optimistically cleared on send ...
    await waitFor(() => expect(screen.getByRole("textbox")).toHaveValue(""))
    // ... then the server answers with the typed refusal.
    view.rerender(<ChatComposer {...props} sendRefusal={{ id: 1, code: "artifact_crew_not_allowed" }} artifactCreationEnabled={false} />)
    await waitFor(() => expect(screen.getByRole("textbox")).toHaveValue("Build me a landing page"))
    expect(screen.getByRole("alert")).toHaveTextContent("This agent's crew is not allowed to create artifact projects in this workspace")
    expect(screen.queryByText(/failed to save message/i)).toBeNull()
    expect(screen.queryByRole("button", { name: "Create artifact project" })).toBeNull()
  })
  it("does not overwrite something the person already started typing", async () => {
    const view = render(<ChatComposer {...props} />)
    selectCreation(); submit("First attempt")
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(screen.getByRole("textbox")).toHaveValue(""))
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "Something new" } })
    view.rerender(<ChatComposer {...props} sendRefusal={{ id: 1, code: "artifact_crew_not_allowed" }} />)
    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument())
    expect(screen.getByRole("textbox")).toHaveValue("Something new")
  })
})

