import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

// =============================================================================
// A user attached a document, sent the message, and the agent never mentioned
// it. The upload worked — the file was in the container — and the WS frame
// carried the user's text and nothing else. Nothing ever told the agent the
// file existed.
//
// These are the composer-level halves of that fix: the message that goes out
// names the attachment by its agent-visible path, an attachment with no
// caption is still a message, and a send the size guard refuses leaves BOTH
// the draft and the attachments where the user can retry with them.
// =============================================================================

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

vi.mock("../mention-autocomplete", () => ({
  MentionAutocomplete: () => null,
}))

const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: { error: (m: string) => toastError(m), success: vi.fn(), info: vi.fn() },
}))

import { ChatComposer } from "../chat-composer"
import { useComposerStore } from "@/stores/composer-store"
import { WS_MAX_OUTBOUND_FRAME_BYTES } from "@/hooks/use-websocket"

const sendMessage = vi.fn()
const ensureSession = vi.fn(async () => {})

const baseProps = {
  agentId: "agent-1",
  sessionId: "sess-1",
  agentName: "Filip",
  isStreaming: false,
  connectionStatus: "connected",
  stopGeneration: vi.fn(),
  ensureSession,
  sendMessage,
}

/** Put a finished upload in the store, exactly as the upload path leaves it. */
function attachReady(name = "invoice.pdf") {
  useComposerStore.setState({
    attachments: {
      "sess-1": [
        {
          id: "att-1",
          name,
          size: 1234,
          type: "application/pdf",
          status: "ready",
          url: `/output/filip/attachments/sess-1/${name}`,
          path: `attachments/sess-1/${name}`,
        },
      ],
    },
  })
}

function submitForm() {
  const form = document.querySelector("form")!
  fireEvent.submit(form)
}

function typeInComposer(text: string) {
  const textarea = document.querySelector("textarea")!
  fireEvent.change(textarea, { target: { value: text } })
}

describe("composer: attachments ride along with the message", () => {
  beforeEach(() => {
    useComposerStore.setState({ attachments: {}, drafts: {} })
    sendMessage.mockClear()
    ensureSession.mockClear()
    toastError.mockClear()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("sends the agent-visible path with the message, then clears the attachments", async () => {
    render(<ChatComposer {...baseProps} variant="desktop" />)
    attachReady()
    typeInComposer("what do you make of this?")

    submitForm()

    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
    const [content] = sendMessage.mock.calls[0]
    expect(content).toContain("what do you make of this?")
    expect(content).toContain("attachments/sess-1/invoice.pdf")
    // The relative path, not the absolute one: the agent's CWD is its
    // output directory, and the crew slug has no business in the user's
    // own transcript.
    expect(content).not.toContain("/output/filip")

    await waitFor(() =>
      expect(useComposerStore.getState().attachments["sess-1"]).toBeUndefined(),
    )
  })

  it("sends a message with no attachments byte-identically", async () => {
    render(<ChatComposer {...baseProps} variant="desktop" />)
    typeInComposer("just a question")

    submitForm()

    await waitFor(() => expect(sendMessage).toHaveBeenCalledWith("just a question"))
  })

  it("sends an attachment with no caption at all", async () => {
    render(<ChatComposer {...baseProps} variant="mobile" />)
    attachReady("IMG_0007.jpg")

    // Send must be reachable: an empty draft with a file attached used to
    // leave the button disabled, so a photo could not be sent at all.
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Submit" })).not.toBeDisabled(),
    )

    submitForm()

    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
    expect(sendMessage.mock.calls[0][0]).toContain("attachments/sess-1/IMG_0007.jpg")
  })

  it("keeps Send disabled when there is neither text nor an attachment", () => {
    render(<ChatComposer {...baseProps} variant="desktop" />)
    expect(screen.getByRole("button", { name: "Submit" })).toBeDisabled()
  })

  it("refuses a send the attachment block pushes over the frame cap, and keeps both the draft and the attachments", async () => {
    render(<ChatComposer {...baseProps} variant="desktop" />)
    attachReady()

    // Just under the cap on its own; over it once the block is appended.
    const envelopeBytes = (content: string) =>
      new TextEncoder().encode(
        JSON.stringify({
          type: "send_message",
          payload: JSON.stringify({ session_id: "sess-1", content }),
        }),
      ).length
    const text = "a".repeat(WS_MAX_OUTBOUND_FRAME_BYTES - envelopeBytes("") - 10)
    typeInComposer(text)

    submitForm()

    await waitFor(() => expect(toastError).toHaveBeenCalledTimes(1))
    expect(toastError.mock.calls[0][0]).toMatch(/too large/i)
    expect(sendMessage).not.toHaveBeenCalled()
    expect(ensureSession).not.toHaveBeenCalled()

    // The whole point of the guard is that the user can trim and retry.
    expect(useComposerStore.getState().attachments["sess-1"]).toHaveLength(1)
    expect(document.querySelector("textarea")!.value).toBe(text)
  })

  it("will not send while an upload is still in flight", async () => {
    render(<ChatComposer {...baseProps} variant="desktop" />)
    useComposerStore.setState({
      attachments: {
        "sess-1": [
          { id: "att-2", name: "big.zip", size: 99, type: "application/zip", status: "uploading" },
        ],
      },
    })
    typeInComposer("here it is")

    submitForm()

    await waitFor(() => expect(toastError).toHaveBeenCalledTimes(1))
    expect(toastError.mock.calls[0][0]).toMatch(/uploading/i)
    expect(sendMessage).not.toHaveBeenCalled()
    expect(useComposerStore.getState().attachments["sess-1"]).toHaveLength(1)
  })
})
