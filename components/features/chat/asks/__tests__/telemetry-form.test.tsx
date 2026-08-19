import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor } from "@testing-library/react"

// =============================================================================
// A questionnaire either gets finished or it does not, and when it does not,
// the only fact worth having is WHERE it stopped. `ask_form_abandoned` carries
// the id of the last field the user touched — the schema key the pack author
// wrote, never the value the user typed — because a form everybody abandons on
// the same field is a form with one bad question, and no amount of aggregate
// completion rate says which one it is.
//
// The load-bearing promises pinned here:
//   · opening, submitting and abandoning each emit exactly ONE event;
//   · a submitted form is never also recorded as abandoned;
//   · nothing the user typed appears in any of them.
// =============================================================================

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: { error: (m: string) => toastError(m), success: vi.fn(), info: vi.fn() },
}))

import { renderAskTemplate } from "@/lib/ask-template"
import { resetChatTelemetry, setChatTelemetrySink, type ChatEvent } from "@/lib/telemetry"

import { AskFormSheet } from "../ask-form-sheet"
import type { AskForm } from "../types"

const receipt: AskForm = {
  id: "receipt",
  label: "Add a receipt",
  template: "Zaúčtuj fakturu od {{supplier}} — {{note}}",
  attachment: "optional",
  fields: [
    { name: "supplier", label: "Supplier", type: "text", required: true },
    { name: "note", label: "Note", type: "textarea" },
  ],
}

const SECRET = "Vodafone CZ, IBAN CZ6508000000192000145399"

const onSubmit = vi.fn(async () => true)
const onClose = vi.fn()

let events: ChatEvent[]
const named = (name: string) => events.filter((e) => e.name === name)

function renderSheet(props: Partial<React.ComponentProps<typeof AskFormSheet>> = {}) {
  return render(
    <AskFormSheet
      form={receipt}
      agentId="agent-1"
      sessionId="sess-1"
      onSubmit={onSubmit}
      renderTemplate={renderAskTemplate}
      onClose={onClose}
      {...props}
    />,
  )
}

beforeEach(() => {
  onSubmit.mockClear()
  onSubmit.mockResolvedValue(true)
  onClose.mockClear()
  toastError.mockClear()
  resetChatTelemetry()
  events = []
  setChatTelemetrySink((e) => events.push(e))
})

afterEach(cleanup)

describe("opening", () => {
  it("records one open, with the template id and how many fields it asks", () => {
    renderSheet()
    const opened = named("ask_form_opened")
    expect(opened).toHaveLength(1)
    expect(opened[0].payload).toMatchObject({
      template_id: "receipt",
      field_count: 2,
      session_id: "sess-1",
      agent_id: "agent-1",
    })
  })

  it("records nothing at all for a null form — there is no sheet", () => {
    renderSheet({ form: null })
    expect(events).toHaveLength(0)
  })
})

describe("submitting", () => {
  it("records one submit, with how many fields were filled", async () => {
    renderSheet()
    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: SECRET } })
    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(named("ask_form_submitted")).toHaveLength(1))
    expect(named("ask_form_submitted")[0].payload).toMatchObject({
      template_id: "receipt",
      field_count: 2,
      filled_count: 1,
    })
  })

  it("does not also record an abandonment when the sheet then unmounts", async () => {
    const { unmount } = renderSheet()
    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: "ACME" } })
    fireEvent.click(screen.getByTestId("ask-submit"))
    await waitFor(() => expect(named("ask_form_submitted")).toHaveLength(1))

    unmount()
    expect(named("ask_form_abandoned")).toHaveLength(0)
  })

  it("records an abandonment when the send was refused and the sheet stays open", async () => {
    // A refused send leaves everything the user typed on screen. If they then
    // give up, that is an abandonment — and it must not be counted as a
    // submission, which is what "≥ 70 % completion once opened" would otherwise
    // quietly inflate.
    onSubmit.mockResolvedValue(false)
    const { unmount } = renderSheet()
    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: "ACME" } })
    fireEvent.click(screen.getByTestId("ask-submit"))
    await waitFor(() => expect(onSubmit).toHaveBeenCalled())

    expect(named("ask_form_submitted")).toHaveLength(0)
    unmount()
    expect(named("ask_form_abandoned")).toHaveLength(1)
  })

  it("records nothing for a submit the form's own rules refused", () => {
    renderSheet()
    // `supplier` is required and empty.
    fireEvent.click(screen.getByTestId("ask-submit"))
    expect(toastError).toHaveBeenCalled()
    expect(named("ask_form_submitted")).toHaveLength(0)
  })
})

describe("abandoning", () => {
  it("records the last field touched, and says the user cancelled", () => {
    renderSheet()
    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: "ACME" } })
    fireEvent.change(screen.getByLabelText(/Note/), { target: { value: SECRET } })
    fireEvent.click(screen.getByTestId("ask-cancel"))

    const abandoned = named("ask_form_abandoned")
    expect(abandoned).toHaveLength(1)
    expect(abandoned[0].payload).toMatchObject({
      template_id: "receipt",
      field_count: 2,
      filled_count: 2,
      last_field_id: "note",
      reason: "cancelled",
    })
    expect(onClose).toHaveBeenCalled()
  })

  it("distinguishes a dismissal from a cancellation", () => {
    renderSheet()
    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: "ACME" } })
    fireEvent.click(screen.getByTestId("ask-close"))

    expect(named("ask_form_abandoned")[0].payload.reason).toBe("dismissed")
  })

  it("treats Escape as a dismissal", () => {
    renderSheet()
    fireEvent.keyDown(screen.getByTestId("ask-sheet"), { key: "Escape" })
    expect(named("ask_form_abandoned")[0].payload.reason).toBe("dismissed")
  })

  it("treats a sheet that simply went away as navigated off", () => {
    const { unmount } = renderSheet()
    unmount()
    const abandoned = named("ask_form_abandoned")
    expect(abandoned).toHaveLength(1)
    expect(abandoned[0].payload.reason).toBe("navigated")
    expect(abandoned[0].payload.filled_count).toBe(0)
  })

  it("records the abandonment exactly once, however many ways it is closed", () => {
    const { unmount } = renderSheet()
    fireEvent.click(screen.getByTestId("ask-cancel"))
    fireEvent.click(screen.getByTestId("ask-close"))
    fireEvent.keyDown(screen.getByTestId("ask-sheet"), { key: "Escape" })
    unmount()
    expect(named("ask_form_abandoned")).toHaveLength(1)
  })
})

describe("the sheet carries no answers into telemetry", () => {
  it("emits neither the values, the labels, nor the rendered message", async () => {
    renderSheet()
    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: SECRET } })
    fireEvent.change(screen.getByLabelText(/Note/), { target: { value: "pay it today" } })
    fireEvent.click(screen.getByTestId("ask-submit"))
    await waitFor(() => expect(named("ask_form_submitted")).toHaveLength(1))

    const serialized = JSON.stringify(events)
    expect(serialized).not.toContain("Vodafone")
    expect(serialized).not.toContain("CZ6508")
    expect(serialized).not.toContain("pay it today")
    expect(serialized).not.toContain("Zaúčtuj")
    expect(serialized).not.toContain("Add a receipt")
  })
})

describe("telemetry cannot break the sheet", () => {
  it("a throwing sink does not stop the form from sending", async () => {
    setChatTelemetrySink(() => {
      throw new Error("sink exploded")
    })
    renderSheet()
    fireEvent.change(screen.getByLabelText(/Supplier/), { target: { value: "ACME" } })
    expect(() => fireEvent.click(screen.getByTestId("ask-submit"))).not.toThrow()
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
    expect(onClose).toHaveBeenCalled()
  })

  it("a throwing sink does not stop the form from closing", () => {
    setChatTelemetrySink(() => {
      throw new Error("sink exploded")
    })
    renderSheet()
    expect(() => fireEvent.click(screen.getByTestId("ask-cancel"))).not.toThrow()
    expect(onClose).toHaveBeenCalled()
  })
})
