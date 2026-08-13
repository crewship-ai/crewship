import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

// =============================================================================
// The form sheet: every field type renders, an unknown type still renders,
// the preview shows exactly what will go, and the two ways a submit can be
// blocked each say WHY.
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
import { useComposerStore } from "@/stores/composer-store"

const everyType: AskForm = {
  id: "kitchen-sink",
  label: "Every field",
  template: "supplier={{supplier}} note={{note}} qty={{qty}}",
  attachment: "optional",
  fields: [
    { name: "supplier", label: "Supplier", type: "text", placeholder: "Vodafone" },
    { name: "note", label: "Note", type: "textarea" },
    { name: "qty", label: "Quantity", type: "number" },
    { name: "amount", label: "Amount", type: "money" },
    { name: "issued", label: "Issued", type: "date" },
    { name: "period", label: "Period", type: "month" },
    { name: "category", label: "Category", type: "select", options: ["Telco", "Rent"] },
    { name: "tags", label: "Tags", type: "multiselect", options: ["a", "b"] },
    { name: "paid", label: "Already paid", type: "checkbox" },
    { name: "doc", label: "Document", type: "file" },
    { name: "shot", label: "Photo", type: "photo" },
    // The server is allowed to invent a type without a frontend release.
    { name: "future", label: "Future thing", type: "quantum-flux" },
  ],
}

const receipt: AskForm = {
  id: "receipt",
  label: "Add a receipt",
  template: "Zaúčtuj fakturu od {{supplier}}",
  attachment: "required",
  fields: [{ name: "supplier", label: "Supplier", type: "text", required: true }],
}

const onSubmit = vi.fn(async () => true)
const onClose = vi.fn()

// The product renderer is lib/ask-template.ts, pinned against Go by
// testdata/ask-templates.json and tested in lib/__tests__/ask-template.test.ts.
// The sheet takes it as a PARAMETER, so what is tested here is the wiring —
// which values reach the renderer, and where its output goes — against a
// stand-in that does the minimum. Re-testing the renderer's own rules here
// would be testing them in the wrong place, and forking them would be the
// exact defect the shared fixture exists to prevent.
const renderTemplate = (form: AskForm, values: AskValues, chatId: string) =>
  form.template.replace(/\{\{(\w+)\}\}/g, (_m, k) => {
    const v = values[k]
    if (Array.isArray(v)) return v.join(", ")
    if (v === true) return "yes"
    if (v === false || v == null) return ""
    return `${v}`
  }) + (chatId ? "" : "")

function renderSheet(form: AskForm, render_ = renderTemplate) {
  return render(
    <AskFormSheet
      form={form}
      agentId="agent-1"
      sessionId="sess-1"
      onSubmit={onSubmit}
      renderTemplate={render_}
      onClose={onClose}
    />,
  )
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

/** The same file, refused by the server. A chip in the list, no path behind
 *  it — which is exactly what a `required` attachment must NOT count as. */
function attachFailed(name = "IMG_4821.heic") {
  useComposerStore.setState({
    attachments: {
      "sess-1": [
        {
          id: "att-1",
          name,
          size: 2100,
          type: "image/heic",
          status: "error",
          error: "create parent dir: permission denied",
        },
      ],
    },
  })
}

describe("ask form sheet", () => {
  beforeEach(() => {
    useComposerStore.setState({ attachments: {}, drafts: {} })
    onSubmit.mockClear()
    onClose.mockClear()
    toastError.mockClear()
  })

  it("renders every field type, and falls back to text for one it has never heard of", () => {
    renderSheet(everyType)

    expect(screen.getByLabelText("Supplier").tagName).toBe("INPUT")
    expect(screen.getByLabelText("Note").tagName).toBe("TEXTAREA")
    expect(screen.getByLabelText("Quantity")).toHaveAttribute("type", "number")
    expect(screen.getByLabelText("Amount")).toHaveAttribute("type", "number")
    expect(screen.getByTestId("ask-field-amount-currency")).toBeTruthy()
    expect(screen.getByLabelText("Issued")).toHaveAttribute("type", "date")
    expect(screen.getByLabelText("Period")).toHaveAttribute("type", "month")
    expect(screen.getByLabelText("Category")).toHaveAttribute("role", "combobox")
    expect(screen.getByLabelText("a")).toHaveAttribute("role", "checkbox")
    expect(screen.getByLabelText("Already paid")).toHaveAttribute("role", "checkbox")
    expect(screen.getByTestId("ask-field-doc-attachment")).toBeTruthy()
    expect(screen.getByTestId("ask-field-shot-attachment")).toBeTruthy()

    // The unknown type is the property that lets the server ship a new field
    // type without a coordinated frontend release. It must render a text
    // input, not nothing.
    const unknown = screen.getByLabelText("Future thing")
    expect(unknown.tagName).toBe("INPUT")
    // An <input> with no explicit type IS a text input — the same shape the
    // slash modal has always emitted for a type it does not recognise.
    expect(unknown.getAttribute("type") ?? "text").toBe("text")
  })

  it("previews the rendered message and updates it as fields change", () => {
    renderSheet(receipt)

    fireEvent.click(screen.getByTestId("ask-preview-toggle"))
    expect(screen.getByTestId("ask-preview").textContent).toBe("Zaúčtuj fakturu od ")

    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: "Vodafone" } })
    expect(screen.getByTestId("ask-preview").textContent).toBe("Zaúčtuj fakturu od Vodafone")

    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: "O2" } })
    expect(screen.getByTestId("ask-preview").textContent).toBe("Zaúčtuj fakturu od O2")
  })

  it("shows the attachment in the preview, because the preview is what will go", () => {
    renderSheet(receipt)
    attachReady()
    fireEvent.click(screen.getByTestId("ask-preview-toggle"))
    expect(screen.getByTestId("ask-preview").textContent).toContain("attachments/sess-1/IMG_4821.heic")
  })

  it("blocks submit on an empty required field, and names the field", async () => {
    renderSheet(receipt)
    attachReady()

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toMatch(/supplier/i)
    expect(toastError.mock.calls[0][0]).toMatch(/required/i)
    expect(onSubmit).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
  })

  it("blocks submit on a missing required attachment, and says it is the attachment", async () => {
    renderSheet(receipt)
    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: "Vodafone" } })

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toMatch(/attach/i)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  // A chip on screen is not a document on the agent. A form whose whole point
  // is "send me the receipt" must not accept a receipt that never uploaded —
  // it would go out as a receipt-shaped message with no receipt in it.
  it("does not accept a FAILED upload as the required attachment", async () => {
    renderSheet(receipt)
    attachFailed()
    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: "Vodafone" } })

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toMatch(/attach/i)
    expect(onSubmit).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
  })

  it("does not accept a failed upload for a required file FIELD either", async () => {
    const withFileField: AskForm = {
      ...receipt,
      id: "receipt-file-field",
      attachment: "optional",
      template: "Zaúčtuj fakturu od {{supplier}}: {{doc}}",
      fields: [
        { name: "supplier", label: "Supplier", type: "text", required: true },
        { name: "doc", label: "Document", type: "file", required: true },
      ],
    }
    renderSheet(withFileField)
    attachFailed()
    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: "Vodafone" } })

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toMatch(/document/i)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("keeps a failed upload out of the preview, which is what will go", () => {
    renderSheet(receipt)
    attachFailed()
    fireEvent.click(screen.getByTestId("ask-preview-toggle"))
    expect(screen.getByTestId("ask-preview").textContent).not.toContain("IMG_4821.heic")
  })

  it("submits the rendered template once every requirement is met", async () => {
    renderSheet(receipt)
    attachReady()
    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: "Vodafone" } })

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
    expect(onSubmit.mock.calls[0][0].id).toBe("receipt")
    expect(onSubmit.mock.calls[0][1]).toBe("Zaúčtuj fakturu od Vodafone")
    await waitFor(() => expect(onClose).toHaveBeenCalled())
  })

  it("closes on Escape and sends nothing", () => {
    renderSheet(receipt)
    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: "Vodafone" } })

    fireEvent.keyDown(screen.getByTestId("ask-sheet"), { key: "Escape" })

    expect(onClose).toHaveBeenCalledTimes(1)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("cancels without sending", () => {
    renderSheet(receipt)
    fireEvent.click(screen.getByTestId("ask-cancel"))
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  // ---------------------------------------------------------------------
  // The values the renderer receives.
  //
  // The sheet holds one string per field because that is what the shared
  // FormField takes; lib/ask-template.ts wants arrays, booleans and a money
  // field's two entries. This is the conversion, and it is where the two
  // halves of the feature actually meet — a `checkbox` arriving as the string
  // "true" would render the word "true" into somebody's message.
  // ---------------------------------------------------------------------
  it("hands the renderer the value shape each field type is defined in", () => {
    const spy = vi.fn(() => "rendered")
    renderSheet(everyType, spy)
    attachReady("page 1.heic")

    fireEvent.change(screen.getByLabelText("Supplier"), { target: { value: "Vodafone" } })
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "1249" } })
    fireEvent.click(screen.getByLabelText("Already paid"))
    fireEvent.click(screen.getByLabelText("a"))

    const [form, values, chatId] = spy.mock.calls[spy.mock.calls.length - 1] as unknown as [
      AskForm,
      AskValues,
      string,
    ]
    expect(form.id).toBe("kitchen-sink")
    // The chat id, so file values render as attachments/<chatId>/<name>.
    expect(chatId).toBe("sess-1")

    expect(values.supplier).toBe("Vodafone")
    // A money field is TWO placeholders, the second derived from the first.
    expect(values.amount).toBe("1249")
    expect(values.amount_currency).toBe("CZK")
    // A ticked box is a boolean, not the string "true" — the renderer turns it
    // into "yes" and drops the line when it is false.
    expect(values.paid).toBe(true)
    // A multiselect is an array, which the renderer joins with ", ".
    expect(values.tags).toEqual(["a"])
    // Files are an array of the paths the upload already returned.
    expect(values.doc).toEqual(["attachments/sess-1/page 1.heic"])
  })

  it("an empty money field sends no bare currency", () => {
    const spy = vi.fn(() => "rendered")
    renderSheet(everyType, spy)
    const [, values] = spy.mock.calls[spy.mock.calls.length - 1] as unknown as [AskForm, AskValues]
    expect(values.amount).toBe("")
    expect(values.amount_currency).toBe("")
  })

  it("fits the real renderer end to end, not just a stand-in", async () => {
    const realForm: AskForm = {
      id: "receipt",
      label: "Add a receipt",
      attachment: "required",
      template: "Please file this receipt.\n\nSupplier: {{supplier}}\nAmount: {{amount}} {{amount_currency}}\nCategory: {{category}}\nDocument: {{document}}",
      fields: [
        { name: "supplier", label: "Supplier", type: "text", required: true },
        { name: "amount", label: "Amount", type: "money", currency: ["CZK", "EUR"] },
        { name: "category", label: "Category", type: "select", options: ["Telco"] },
        { name: "document", label: "Document", type: "file" },
      ],
    }
    renderSheet(realForm, renderAskTemplate)
    attachReady("IMG_4821.heic")
    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: "Vodafone" } })
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "1249" } })

    fireEvent.click(screen.getByTestId("ask-submit"))
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))

    expect(onSubmit.mock.calls[0][1]).toBe(
      "Please file this receipt.\n\nSupplier: Vodafone\nAmount: 1249 CZK\nDocument: attachments/sess-1/IMG_4821.heic",
    )
  })
})
