import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

// =============================================================================
// Submitting a form is not a new transport. It renders the template and sends
// an ORDINARY message down the composer's own path — same size guard, same
// draft-survival rules, same attachment block — so the sheet is mounted inside
// the composer and reuses useMessageSubmit rather than reimplementing it.
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
import { lookupAskProvenance } from "../../asks/ask-provenance"
import type { AskForm, AskValues } from "../../asks/types"

/** Stand-in for lib/ask-template.ts — see the note in
 *  asks/__tests__/ask-form-sheet.test.tsx. */
const renderAskTemplate = (form: AskForm, values: AskValues) =>
  form.template.replace(/\{\{(\w+)\}\}/g, (_m, k) => {
    const v = values[k]
    return Array.isArray(v) ? v.join(", ") : v == null ? "" : `${v}`
  })

const receipt: AskForm = {
  id: "receipt",
  label: "Add a receipt",
  template: "Zaúčtuj fakturu od {{supplier}}",
  attachment: "required",
  fields: [{ name: "supplier", label: "Supplier", type: "text", required: true }],
}

const sendMessage = vi.fn()
const ensureSession = vi.fn(async () => {})
const onCloseAskForm = vi.fn()
const onSent = vi.fn()

const baseProps = {
  agentId: "agent-1",
  sessionId: "sess-1",
  agentName: "Riley",
  isStreaming: false,
  connectionStatus: "connected",
  stopGeneration: vi.fn(),
  ensureSession,
  sendMessage,
  onSent,
}

function attachReady(name = "IMG_4821.heic") {
  useComposerStore.setState({
    attachments: {
      "sess-1": [
        {
          id: "att-1",
          name,
          size: 2100,
          type: "image/heic",
          status: "ready",
          url: `/output/riley/attachments/sess-1/${name}`,
          path: `attachments/sess-1/${name}`,
        },
      ],
    },
  })
}

describe("form submit rides the composer's own send path", () => {
  beforeEach(() => {
    useComposerStore.setState({ attachments: {}, drafts: {} })
    sendMessage.mockClear()
    ensureSession.mockClear()
    onCloseAskForm.mockClear()
    onSent.mockClear()
    toastError.mockClear()
  })

  it("sends the rendered template as one ordinary message, attachment path included", async () => {
    render(
      <ChatComposer
        {...baseProps}
        variant="desktop"
        askForm={receipt}
        onCloseAskForm={onCloseAskForm}
        renderAskTemplate={renderAskTemplate}
      />,
    )
    attachReady()
    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: "Vodafone" } })

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
    const [content] = sendMessage.mock.calls[0]
    expect(content).toContain("Zaúčtuj fakturu od Vodafone")
    // The attachment reaches the agent through the ONE convention
    // (lib/attachment-message.ts), not a second one invented for forms.
    expect(content).toContain("attachments/sess-1/IMG_4821.heic")
    expect(content).not.toContain("/output/riley")

    expect(ensureSession).toHaveBeenCalled()
    await waitFor(() => expect(onCloseAskForm).toHaveBeenCalled())
    // Attachments are consumed by the send, exactly as a typed message does it.
    await waitFor(() =>
      expect(useComposerStore.getState().attachments["sess-1"]).toBeUndefined(),
    )
  })

  it("records the provenance of the turn it just sent", async () => {
    render(
      <ChatComposer
        {...baseProps}
        variant="desktop"
        askForm={receipt}
        onCloseAskForm={onCloseAskForm}
        renderAskTemplate={renderAskTemplate}
      />,
    )
    attachReady()
    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: "Vodafone" } })
    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
    const [content] = sendMessage.mock.calls[0]
    expect(lookupAskProvenance("sess-1", content)).toBe("Add a receipt")
  })

  it("does not render the sheet when no form is open — the composer is unchanged", () => {
    render(<ChatComposer {...baseProps} variant="desktop" />)
    expect(screen.queryByTestId("ask-sheet")).toBeNull()
  })
})
