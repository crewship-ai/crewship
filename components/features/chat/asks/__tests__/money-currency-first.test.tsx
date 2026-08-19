import { describe, it, expect, vi, beforeAll, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { useState } from "react"

// =============================================================================
// A money field is two controls and one value, and the user is not told which
// order to use them in. Picking the currency BEFORE typing the amount used to
// throw the pick away — the Select snapped back to the first currency in the
// list and the message went out in it.
// =============================================================================

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() },
}))

import { renderAskTemplate } from "@/lib/ask-template"
import { useComposerStore } from "@/stores/composer-store"

import { FormField } from "../form-field"
import { AskFormSheet } from "../ask-form-sheet"
import type { AskForm } from "../types"

beforeAll(() => {
  // Radix Select drives itself through the pointer-capture APIs and scrolls
  // the active item into view; happy-dom has neither.
  Object.assign(window.HTMLElement.prototype, {
    scrollIntoView: () => {},
    hasPointerCapture: () => false,
    releasePointerCapture: () => {},
    setPointerCapture: () => {},
  })
})

/** Open the currency Select and choose `code`. Keyboard opens it (Radix's own
 *  trigger binding); the item commits on pointerup. */
function pickCurrency(testId: string, code: string) {
  fireEvent.keyDown(screen.getByTestId(testId), { key: "ArrowDown" })
  const option = screen.getByRole("option", { name: code })
  fireEvent.pointerDown(option, { pointerType: "mouse", button: 0 })
  fireEvent.pointerUp(option, { pointerType: "mouse", button: 0 })
  fireEvent.click(option)
}

/** FormField is controlled by whoever renders it — the sheet holds one string
 *  per field name. This is that owner, and the string it holds is what the
 *  assertions read. */
function Harness({ initial = "" }: { initial?: string }) {
  const [value, setValue] = useState(initial)
  return (
    <>
      <FormField
        field={{ name: "amount", label: "Amount", type: "money", currency: ["CZK", "EUR", "USD"] }}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        testIdPrefix="ask-field"
      />
      <output data-testid="money-value">{value}</output>
    </>
  )
}

describe("a money field keeps the currency whichever control is used first", () => {
  it("carries a currency chosen before the amount into the value", () => {
    render(<Harness />)

    pickCurrency("ask-field-amount-currency", "USD")
    // The pick is on screen even though the value cannot encode it yet.
    expect(screen.getByTestId("ask-field-amount-currency").textContent).toContain("USD")

    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "1249" } })
    expect(screen.getByTestId("money-value").textContent).toBe("1249 USD")
  })

  it("still emits nothing at all while the amount is empty", () => {
    render(<Harness />)
    pickCurrency("ask-field-amount-currency", "USD")
    // An unanswered money field must not send a bare currency — the encoding
    // ask-form-sheet's toAskValues depends on is unchanged.
    expect(screen.getByTestId("money-value").textContent).toBe("")
  })

  it("keeps working in the other order, and after the amount is cleared", () => {
    render(<Harness />)

    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "1249" } })
    pickCurrency("ask-field-amount-currency", "EUR")
    expect(screen.getByTestId("money-value").textContent).toBe("1249 EUR")

    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "" } })
    expect(screen.getByTestId("money-value").textContent).toBe("")
    // Retyping picks the currency the user last saw, not the list's first.
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "990" } })
    expect(screen.getByTestId("money-value").textContent).toBe("990 EUR")
  })

  it("shows a default's own currency rather than the list's first", () => {
    render(<Harness initial="1249 USD" />)
    expect(screen.getByTestId("ask-field-amount-currency").textContent).toContain("USD")
  })
})

// -----------------------------------------------------------------------------
// …and the whole way out: what the agent reads, and what the envelope records.
// -----------------------------------------------------------------------------

const expense: AskForm = {
  id: "expense",
  label: "File an expense",
  template: "Expense\nAmount: {{amount}} {{amount_currency}}",
  attachment: "optional",
  fields: [{ name: "amount", label: "Amount", type: "money", currency: ["CZK", "EUR", "USD"] }],
}

describe("the currency the user picked is the one that is sent", () => {
  const onSubmit = vi.fn(async () => true)
  const onClose = vi.fn()

  beforeEach(() => {
    useComposerStore.setState({ attachments: {}, drafts: {} })
    onSubmit.mockClear()
    onClose.mockClear()
  })

  it("renders the message and the envelope in the chosen currency", async () => {
    render(
      <AskFormSheet
        form={expense}
        agentId="agent-1"
        sessionId="sess-1"
        onSubmit={onSubmit}
        renderTemplate={renderAskTemplate}
        onClose={onClose}
      />,
    )

    // Currency first — the order a form that opens on the amount invites.
    pickCurrency("ask-field-amount-currency", "USD")
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "1249" } })

    fireEvent.click(screen.getByTestId("ask-submit"))
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))

    const [, text, envelope] = onSubmit.mock.calls[0] as unknown as [
      AskForm,
      string,
      { values: Record<string, unknown> },
    ]
    expect(text).toBe("Expense\nAmount: 1249 USD")
    expect(envelope.values.amount_currency).toBe("USD")
  })
})
