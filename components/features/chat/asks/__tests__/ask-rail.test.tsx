import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"

// =============================================================================
// The chip rail carries two kinds of chip and they must not look the same.
//
// A question chip SENDS. A form chip OPENS. A user who taps "Add a receipt"
// and finds a message already on its way has been lied to, so the difference
// is asserted here on the markers a screen reader and a human both get —
// aria-haspopup, the accessible name, a glyph, a trailing ellipsis — never on
// a class name, which is styling and may change.
// =============================================================================

import { AskRail } from "../ask-rail"
import type { AskForm } from "../types"

const receipt: AskForm = {
  id: "receipt",
  label: "Add a receipt",
  template: "Zaúčtuj {{supplier}}",
  attachment: "required",
  fields: [{ name: "supplier", label: "Supplier", type: "text", required: true }],
}

const close: AskForm = {
  id: "close",
  label: "Monthly close",
  template: "Close {{month}}",
  attachment: "none",
  fields: [{ name: "month", label: "Month", type: "month" }],
}

const onPickQuestion = vi.fn()
const onPickForm = vi.fn()

function renderRail(props: Partial<React.ComponentProps<typeof AskRail>> = {}) {
  return render(
    <AskRail
      questions={["What's unpaid?"]}
      forms={[receipt]}
      limit={6}
      onPickQuestion={onPickQuestion}
      onPickForm={onPickForm}
      {...props}
    />,
  )
}

describe("ask rail: a chip that opens is not a chip that sends", () => {
  beforeEach(() => {
    onPickQuestion.mockClear()
    onPickForm.mockClear()
  })

  it("marks the form chip as opening something and the question chip as not", () => {
    renderRail()

    const form = screen.getByTestId("ask-chip-form-receipt")
    const question = screen.getByTestId("ask-chip-question-0")

    // 1. Semantics: a form chip announces that it opens a dialog.
    expect(form).toHaveAttribute("aria-haspopup", "dialog")
    expect(question).not.toHaveAttribute("aria-haspopup")

    // 2. Accessible name says so in words.
    expect(form.getAttribute("aria-label")).toMatch(/opens a form/i)

    // 3. Visible glyph, only on the form chip.
    expect(within(form).getByTestId("ask-chip-glyph")).toBeTruthy()
    expect(within(question).queryByTestId("ask-chip-glyph")).toBeNull()

    // 4. Trailing ellipsis, only on the form chip.
    expect(form.textContent?.trim().endsWith("…")).toBe(true)
    expect(question.textContent?.trim().endsWith("…")).toBe(false)
  })

  it("sends on a question chip and sends NOTHING on a form chip", () => {
    renderRail()

    fireEvent.click(screen.getByTestId("ask-chip-question-0"))
    expect(onPickQuestion).toHaveBeenCalledWith("What's unpaid?")
    expect(onPickForm).not.toHaveBeenCalled()

    onPickQuestion.mockClear()
    fireEvent.click(screen.getByTestId("ask-chip-form-receipt"))
    expect(onPickForm).toHaveBeenCalledTimes(1)
    expect(onPickForm.mock.calls[0][0].id).toBe("receipt")
    // The whole point: opening a form must not send a message.
    expect(onPickQuestion).not.toHaveBeenCalled()
  })

  it("caps at the limit and collapses the rest into a working +N", () => {
    const questions = ["q1", "q2", "q3", "q4", "q5", "q6", "q7", "q8"]
    renderRail({ questions, forms: [receipt, close], limit: 6 })

    // 10 items, 6 shown, 4 behind +N.
    expect(screen.getAllByTestId(/^ask-chip-(form|question)-/)).toHaveLength(6)
    const more = screen.getByTestId("ask-rail-more")
    expect(more.textContent).toContain("+4")

    // Nothing overflowing is reachable until +N is opened.
    expect(screen.queryByTestId("ask-rail-overflow")).toBeNull()
    fireEvent.click(more)

    const panel = screen.getByTestId("ask-rail-overflow")
    // The overflow lists ALL of them, not only the hidden tail — it is the
    // full catalogue, which is what "+N opens a list of all of them" means.
    expect(within(panel).getAllByRole("option")).toHaveLength(10)

    fireEvent.click(within(panel).getByText("q8"))
    expect(onPickQuestion).toHaveBeenCalledWith("q8")
  })

  it("caps at 3 when used as follow-ups", () => {
    renderRail({ questions: ["a", "b", "c", "d"], forms: [], limit: 3 })
    expect(screen.getAllByTestId(/^ask-chip-(form|question)-/)).toHaveLength(3)
    expect(screen.getByTestId("ask-rail-more").textContent).toContain("+1")
  })

  it("an agent with no forms renders exactly the chips it renders today", () => {
    renderRail({ questions: ["Help me get started", "What can you do?"], forms: [], limit: 6 })

    expect(screen.queryByTestId("ask-rail-more")).toBeNull()
    const chips = screen.getAllByTestId(/^ask-chip-(form|question)-/)
    expect(chips).toHaveLength(2)
    for (const chip of chips) {
      expect(chip).not.toHaveAttribute("aria-haspopup")
      expect(within(chip).queryByTestId("ask-chip-glyph")).toBeNull()
    }
  })

  it("renders nothing at all when there is nothing to offer", () => {
    const { container } = renderRail({ questions: [], forms: [] })
    expect(container.firstChild).toBeNull()
  })
})
