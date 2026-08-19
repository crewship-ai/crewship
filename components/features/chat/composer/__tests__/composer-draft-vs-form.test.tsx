import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

// =============================================================================
// The composer and the ask sheet are two ways to send, and only ONE of them
// owns the textarea.
//
// The sheet grows ABOVE the composer inside the same column (PRD §5.2) rather
// than over it, so while a form is being filled in the textarea is still live,
// still visible, and still holding whatever was typed into it. A form submit
// sends the rendered template and nothing else — the typed note was never part
// of it — so clearing the composer on the form's behalf silently deletes a
// message the user had not sent yet.
// =============================================================================

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

vi.mock("../mention-autocomplete", () => ({
  MentionAutocomplete: () => null,
}))

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() },
}))

import { ChatComposer } from "../chat-composer"
import { useComposerStore } from "@/stores/composer-store"
import type { AskForm, AskValues } from "../../asks/types"

/** Stand-in for lib/ask-template.ts — see asks/__tests__/ask-form-sheet.test.tsx. */
const renderAskTemplate = (form: AskForm, values: AskValues) =>
  form.template.replace(/\{\{(\w+)\}\}/g, (_m, k) => {
    const v = values[k]
    return Array.isArray(v) ? v.join(", ") : v == null ? "" : `${v}`
  })

const receipt: AskForm = {
  id: "receipt",
  label: "Add a receipt",
  template: "Zaúčtuj fakturu od {{supplier}}",
  attachment: "optional",
  fields: [{ name: "supplier", label: "Supplier", type: "text", required: true }],
}

const sendMessage = vi.fn()
const ensureSession = vi.fn(async () => true)
const onCloseAskForm = vi.fn()

const baseProps = {
  agentId: "agent-1",
  sessionId: "sess-1",
  agentName: "Riley",
  variant: "desktop" as const,
  isStreaming: false,
  connectionStatus: "connected",
  stopGeneration: vi.fn(),
  ensureSession,
  sendMessage,
}

const TYPED = "also, this is urgent"

describe("a form submit leaves the composer's own draft alone", () => {
  beforeEach(() => {
    useComposerStore.setState({ attachments: {}, drafts: {} })
    sendMessage.mockClear()
    ensureSession.mockClear()
    onCloseAskForm.mockClear()
  })

  it("keeps what was typed in the textarea when the sheet sends", async () => {
    useComposerStore.getState().setDraft("sess-1", TYPED)
    render(
      <ChatComposer
        {...baseProps}
        askForm={receipt}
        onCloseAskForm={onCloseAskForm}
        renderAskTemplate={renderAskTemplate}
      />,
    )

    const textarea = screen.getByPlaceholderText("Message Riley...")
    fireEvent.change(textarea, { target: { value: TYPED } })
    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: "Vodafone" } })

    fireEvent.click(screen.getByTestId("ask-submit"))

    // The form's message goes out — and carries only the form.
    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
    expect(sendMessage.mock.calls[0][0]).toBe("Zaúčtuj fakturu od Vodafone")
    await waitFor(() => expect(onCloseAskForm).toHaveBeenCalled())

    // …and the note the user was still writing is untouched, in the box and in
    // the store that survives a reload.
    expect((textarea as HTMLTextAreaElement).value).toBe(TYPED)
    expect(useComposerStore.getState().drafts["sess-1"]).toBe(TYPED)
  })

  it("still clears the composer when the typed message is the one sent", async () => {
    useComposerStore.getState().setDraft("sess-1", TYPED)
    render(<ChatComposer {...baseProps} />)

    const textarea = screen.getByPlaceholderText("Message Riley...")
    fireEvent.change(textarea, { target: { value: TYPED } })
    fireEvent.submit(document.querySelector("form")!)

    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
    expect(sendMessage.mock.calls[0][0]).toBe(TYPED)
    await waitFor(() => expect((textarea as HTMLTextAreaElement).value).toBe(""))
    expect(useComposerStore.getState().drafts["sess-1"]).toBeUndefined()
  })
})
