import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, act } from "@testing-library/react"

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() },
}))

// The composer's neighbours. None of them is what this file is about; the
// attachment zone in particular reaches for upload endpoints and a store.
vi.mock("../attachment-zone", () => ({
  AttachmentZone: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
  AttachmentButton: () => null,
  CameraButton: () => null,
  EnsureChatSessionProvider: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}))
vi.mock("../mention-autocomplete", () => ({ MentionAutocomplete: () => null }))

// The sheet's own behaviour (validation, rendering, uploads) is its package's
// business and is tested there. What this file pins is the CONTRACT between
// them: the sheet passes an envelope as its third argument, and the composer
// has to forward it. Standing in for the sheet is the only way to assert that
// without re-testing the form.
const sheetSubmit = { current: null as null | AskSheetSubmit }
type AskSheetSubmit = (
  form: { id: string; label: string; fields: unknown[] },
  text: string,
  envelope: AskSubmissionEnvelope,
) => Promise<boolean>

vi.mock("../../asks/ask-form-sheet", () => ({
  AskFormSheet: (props: { onSubmit: AskSheetSubmit }) => {
    sheetSubmit.current = props.onSubmit
    return <div data-testid="ask-sheet" />
  },
}))

import { ChatComposer } from "../chat-composer"
import {
  ASK_SUBMISSION_METADATA_KEY,
  type AskSubmissionEnvelope,
} from "../../asks/ask-envelope"
import { resetAskProvenance } from "../../asks/ask-provenance"

const form = {
  id: "receipt",
  label: "Add a receipt",
  template: "Receipt from {{vendor}}",
  fields: [{ name: "vendor", label: "Vendor", type: "text" }],
}

function envelope(overrides: Partial<AskSubmissionEnvelope> = {}): AskSubmissionEnvelope {
  return {
    submission_id: "sub_1",
    form_id: "receipt",
    form_label: "Add a receipt",
    form_version: 1,
    values: { vendor: "Acme" },
    rendered_text: "Receipt from Acme",
    ...overrides,
  }
}

function renderComposer() {
  const sendMessage = vi.fn()
  const ensureSession = vi.fn(async () => true)
  render(
    <ChatComposer
      agentId="agent-1"
      sessionId="chat-1"
      variant="desktop"
      isStreaming={false}
      connectionStatus="connected"
      stopGeneration={vi.fn()}
      ensureSession={ensureSession}
      sendMessage={sendMessage}
      askForm={form}
      onCloseAskForm={vi.fn()}
      renderAskTemplate={() => "Receipt from Acme"}
    />,
  )
  return { sendMessage }
}

describe("the composer forwards the sheet's envelope into the send path", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    sheetSubmit.current = null
    resetAskProvenance()
  })

  it("passes the envelope on as message metadata, leaving the text alone", async () => {
    const { sendMessage } = renderComposer()
    expect(screen.getByTestId("ask-sheet")).toBeTruthy()

    const env = envelope()
    await act(async () => {
      await sheetSubmit.current!(form, "Receipt from Acme", env)
    })

    expect(sendMessage).toHaveBeenCalledTimes(1)
    const [text, metadata] = sendMessage.mock.calls[0]
    // The text an agent receives is the rendered template and nothing else.
    expect(text).toBe("Receipt from Acme")
    expect(metadata).toEqual({ [ASK_SUBMISSION_METADATA_KEY]: env })
  })

  it("gives two identical submissions distinct submission ids on the wire", async () => {
    const { sendMessage } = renderComposer()

    await act(async () => {
      await sheetSubmit.current!(form, "Receipt from Acme", envelope({ submission_id: "sub_a" }))
    })
    await act(async () => {
      await sheetSubmit.current!(form, "Receipt from Acme", envelope({ submission_id: "sub_b" }))
    })

    const ids = sendMessage.mock.calls.map(
      (c) => (c[1] as Record<string, AskSubmissionEnvelope>)[ASK_SUBMISSION_METADATA_KEY].submission_id,
    )
    expect(ids).toEqual(["sub_a", "sub_b"])
  })
})
