import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, within, act } from "@testing-library/react"

// =============================================================================
// One upload field, one answer.
//
// The sheet used to read ONE attachment list keyed only by the session, so
// every `file` / `photo` field in a form was handed the same list of paths:
// a form asking for a contract AND an identity photo could not tell which file
// answered which question, one upload satisfied both required fields, and the
// rendered message named every file under every field.
//
// What these pin is field ownership: an upload belongs to the field it was
// dropped into, it satisfies that field and no other, its chip is drawn next to
// the question that asked for it, and the message names it exactly once — under
// that field. An upload that belongs to no field is the composer's own and is
// named by the attachment block, exactly as it was before any of this existed.
// =============================================================================

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: { error: (m: string) => toastError(m), success: vi.fn(), info: vi.fn() },
}))

import { renderAskTemplate } from "@/lib/ask-template"

import { AskFormSheet } from "../ask-form-sheet"
import type { AskForm, AskValues } from "../types"
import { useComposerStore, type ComposerAttachment } from "@/stores/composer-store"

const SESSION = "sess-1"
const FORM_ID = "supplier-intake"

/** Two REQUIRED upload fields — the shape the audit's finding is about. */
const twoUploads: AskForm = {
  id: FORM_ID,
  label: "Onboard a supplier",
  attachment: "optional",
  template: "Contract: {{contract}}\nIdentity: {{identity}}",
  fields: [
    { name: "contract", label: "Contract", type: "file", required: true },
    { name: "identity", label: "Identity photo", type: "photo", required: true },
  ],
}

const onSubmit = vi.fn(async () => true)
const onClose = vi.fn()

/** Stand-in renderer — see the note in ask-form-sheet.test.tsx. The real one is
 *  used in the message test below, where the exact bytes are the point. */
const stubRender = (form: AskForm, values: AskValues) =>
  form.template.replace(/\{\{(\w+)\}\}/g, (_m, k) => {
    const v = values[k]
    if (Array.isArray(v)) return v.join(", ")
    if (v === true) return "yes"
    if (v === false || v == null) return ""
    return `${v}`
  })

function renderSheet(form: AskForm, render_ = stubRender) {
  return render(
    <AskFormSheet
      form={form}
      agentId="agent-1"
      sessionId={SESSION}
      onSubmit={onSubmit}
      renderTemplate={render_}
      onClose={onClose}
    />,
  )
}

/** A finished upload that answers `field` of this form. */
function owned(id: string, name: string, field: string): ComposerAttachment {
  return {
    id,
    name,
    size: 2100,
    type: "application/pdf",
    status: "ready",
    url: `/output/riley/attachments/${SESSION}/${name}`,
    path: `attachments/${SESSION}/${name}`,
    owner: { formId: FORM_ID, field },
  }
}

/** A finished upload that answers no question — the composer's paperclip. */
function plain(id: string, name: string): ComposerAttachment {
  return {
    id,
    name,
    size: 2100,
    type: "application/pdf",
    status: "ready",
    url: `/output/riley/attachments/${SESSION}/${name}`,
    path: `attachments/${SESSION}/${name}`,
  }
}

function seed(...items: ComposerAttachment[]) {
  act(() => {
    useComposerStore.setState({ attachments: { [SESSION]: items } })
  })
}

/** The wrapper `form-field.tsx` puts around a field's upload control, so a
 *  query can be scoped to ONE field's slot. */
function slot(field: string) {
  return screen.getByTestId(`ask-field-${field}-attachment`)
}

function lastValues(spy: ReturnType<typeof vi.fn>): AskValues {
  return spy.mock.calls[spy.mock.calls.length - 1][1] as AskValues
}

describe("an upload answers the field it was dropped into", () => {
  beforeEach(() => {
    useComposerStore.setState({ attachments: {}, drafts: {} })
    onSubmit.mockClear()
    onClose.mockClear()
    toastError.mockClear()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  // ---------------------------------------------------------------------
  // The finding itself: one file, two required fields.
  // ---------------------------------------------------------------------
  it("does not let one field's file satisfy another field's requirement", async () => {
    const spy = vi.fn(stubRender)
    renderSheet(twoUploads, spy)
    seed(owned("att-1", "contract.pdf", "contract"))

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toMatch(/identity photo/i)
    expect(toastError.mock.calls[0][0]).toMatch(/required/i)
    expect(onSubmit).not.toHaveBeenCalled()

    // …and the field that was never answered receives no path at all. The
    // renderer is handed one value per field; giving it the whole session's
    // paths is how the second question got answered with the first one's file.
    const values = lastValues(spy)
    expect(values.contract).toEqual([`attachments/${SESSION}/contract.pdf`])
    expect(values.identity).toEqual([])
  })

  it("submits once each field has its own file", async () => {
    renderSheet(twoUploads)
    seed(owned("att-1", "contract.pdf", "contract"), owned("att-2", "id.jpg", "identity"))

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
    expect(onSubmit.mock.calls[0][1]).toBe(
      `Contract: attachments/${SESSION}/contract.pdf\nIdentity: attachments/${SESSION}/id.jpg`,
    )
  })

  // ---------------------------------------------------------------------
  // Chips.
  // ---------------------------------------------------------------------
  it("draws each field's chips next to that field, and only those", () => {
    renderSheet(twoUploads)
    seed(owned("att-1", "contract.pdf", "contract"), owned("att-2", "id.jpg", "identity"))

    expect(within(slot("contract")).getByText("contract.pdf")).toBeInTheDocument()
    expect(within(slot("contract")).queryByText("id.jpg")).toBeNull()
    expect(within(slot("identity")).getByText("id.jpg")).toBeInTheDocument()
    expect(within(slot("identity")).queryByText("contract.pdf")).toBeNull()
  })

  it("removing a chip removes it from that field only", () => {
    renderSheet(twoUploads)
    seed(owned("att-1", "contract.pdf", "contract"), owned("att-2", "id.jpg", "identity"))

    fireEvent.click(within(slot("contract")).getByLabelText("Remove contract.pdf"))

    expect(within(slot("contract")).queryByText("contract.pdf")).toBeNull()
    expect(within(slot("identity")).getByText("id.jpg")).toBeInTheDocument()
    const left = useComposerStore.getState().attachments[SESSION] ?? []
    expect(left.map((a) => a.id)).toEqual(["att-2"])
  })

  // ---------------------------------------------------------------------
  // The message.
  // ---------------------------------------------------------------------
  it("names each file under the field that owns it, exactly once", async () => {
    const realForm: AskForm = {
      ...twoUploads,
      template: "Please onboard this supplier.\n\nContract: {{contract}}\nIdentity: {{identity}}",
    }
    renderSheet(realForm, renderAskTemplate)
    seed(owned("att-1", "contract.pdf", "contract"), owned("att-2", "id.jpg", "identity"))

    fireEvent.click(screen.getByTestId("ask-preview-toggle"))
    // The preview is the WHOLE outgoing message: the rendered template plus
    // whatever block the composer would append. A file already named by the
    // template must not be named a second time by the block.
    const preview = screen.getByTestId("ask-preview").textContent ?? ""
    expect(preview.match(/contract\.pdf/g) ?? []).toHaveLength(1)
    expect(preview.match(/id\.jpg/g) ?? []).toHaveLength(1)
    expect(preview).not.toContain("I've attached")
    expect(preview).toContain(`Contract: attachments/${SESSION}/contract.pdf`)
    expect(preview).toContain(`Identity: attachments/${SESSION}/id.jpg`)
  })

  // ---------------------------------------------------------------------
  // The plain composer path is untouched.
  // ---------------------------------------------------------------------
  it("still names a plain (non-field) attachment in the appended block", () => {
    renderSheet(twoUploads, renderAskTemplate)
    seed(plain("att-9", "notes.pdf"))

    fireEvent.click(screen.getByTestId("ask-preview-toggle"))
    const preview = screen.getByTestId("ask-preview").textContent ?? ""
    // Byte-for-byte the block lib/attachment-message.ts has always produced.
    expect(preview).toContain(
      "I've attached a file to this message. The path is relative to your working directory:\n\n" +
        `- attachments/${SESSION}/notes.pdf`,
    )
    // It is visible, too: the composer hides its own chip list while a sheet
    // with an upload control is open, so this is the only place it can be seen.
    expect(within(screen.getByTestId("ask-message-attachments")).getByText("notes.pdf")).toBeInTheDocument()
  })

  it("does not let a plain attachment answer an upload field", () => {
    const spy = vi.fn(stubRender)
    renderSheet(twoUploads, spy)
    seed(plain("att-9", "notes.pdf"))

    const values = lastValues(spy)
    expect(values.contract).toEqual([])
    expect(values.identity).toEqual([])
    expect(within(slot("contract")).queryByText("notes.pdf")).toBeNull()
  })

  // A file the user attached to a FORM FIELD is that field's answer. If the
  // form is never sent, it must not turn up in the next ordinary message the
  // user types — the template that named it is gone, so the block would be
  // announcing a file nobody in that conversation asked for.
  it("keeps a field's file out of an unrelated plain message", async () => {
    const { composeMessageWithAttachments } = await import("@/lib/attachment-message")
    const list = [owned("att-1", "contract.pdf", "contract"), plain("att-9", "notes.pdf")]

    expect(composeMessageWithAttachments("what about this", list)).toBe(
      "what about this\n\n" +
        "I've attached a file to this message. The path is relative to your working directory:\n\n" +
        `- attachments/${SESSION}/notes.pdf`,
    )
  })

  // ---------------------------------------------------------------------
  // A failed upload satisfies nothing — per field.
  // ---------------------------------------------------------------------
  it("does not accept another field's successful upload for a field whose own upload failed", async () => {
    renderSheet(twoUploads)
    seed(
      {
        id: "att-1",
        name: "contract.pdf",
        size: 2100,
        type: "application/pdf",
        status: "error",
        error: "create parent dir: permission denied",
        owner: { formId: FORM_ID, field: "contract" },
      },
      owned("att-2", "id.jpg", "identity"),
    )

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toMatch(/contract/i)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("refuses to send while one of its own uploads is still in flight", async () => {
    renderSheet(twoUploads)
    seed(
      {
        id: "att-1",
        name: "contract.pdf",
        size: 2100,
        type: "application/pdf",
        status: "uploading",
        owner: { formId: FORM_ID, field: "contract" },
      },
      owned("att-2", "id.jpg", "identity"),
    )

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toMatch(/uploading/i)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  // ---------------------------------------------------------------------
  // The wiring: a file picked in a field's own control is stamped with that
  // field on the way in, through the composer's ONE upload path.
  // ---------------------------------------------------------------------
  it("stamps the field on a file picked in that field's control", async () => {
    global.fetch = vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        json: () =>
          Promise.resolve({
            path: `attachments/${SESSION}/contract.pdf`,
            agent_path: `/output/riley/attachments/${SESSION}/contract.pdf`,
          }),
      }),
    ) as unknown as typeof fetch

    const spy = vi.fn(stubRender)
    renderSheet(twoUploads, spy)

    const input = screen.getByTestId("ask-upload-contract") as HTMLInputElement
    const file = new File(["%PDF"], "contract.pdf", { type: "application/pdf" })
    Object.defineProperty(input, "files", { value: [file], configurable: true })
    fireEvent.change(input)

    // The chip is owned from the moment it exists, not once the upload lands:
    // an in-flight upload has to be attributable to the field that started it.
    await waitFor(() => {
      const list = useComposerStore.getState().attachments[SESSION] ?? []
      expect(list).toHaveLength(1)
      expect(list[0].owner).toEqual({ formId: FORM_ID, field: "contract" })
    })

    // Same endpoint as the paperclip — one upload path, not a second copy.
    const url = String(vi.mocked(global.fetch).mock.calls[0][0])
    expect(url).toContain(`/api/v1/agents/agent-1/chats/${SESSION}/attachments`)

    await waitFor(() => {
      expect(lastValues(spy).contract).toEqual([`attachments/${SESSION}/contract.pdf`])
    })
    expect(lastValues(spy).identity).toEqual([])
    expect(within(slot("contract")).getByText("contract.pdf")).toBeInTheDocument()
    expect(within(slot("identity")).queryByText("contract.pdf")).toBeNull()
  })

  // The sheet closing is the end of its answers. Anything else leaves a file
  // in the session that nothing on screen can show — the composer draws only
  // the message's own chips — and that file would ride along in the block of
  // the next ordinary message.
  it("drops its own uploads when it closes, and leaves the message's alone", () => {
    const { unmount } = renderSheet(twoUploads)
    seed(owned("att-1", "contract.pdf", "contract"), plain("att-9", "notes.pdf"))

    unmount()

    expect((useComposerStore.getState().attachments[SESSION] ?? []).map((a) => a.id)).toEqual([
      "att-9",
    ])
  })

  it("offers a camera inside a photo field, and it belongs to that field too", () => {
    renderSheet(twoUploads)
    const camera = screen.getByTestId("ask-camera-identity") as HTMLInputElement
    expect(camera.getAttribute("accept")).toBe("image/*")
    expect(camera.getAttribute("capture")).toBe("environment")
    expect(within(slot("identity")).getByTestId("ask-camera-identity")).toBeInTheDocument()
  })
})
