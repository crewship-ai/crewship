import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

// =============================================================================
// The form that opens before Run on the routines detail panel.
//
// Run used to POST {"inputs":{}} unconditionally — its own tooltip said
// "Invoke routine with empty inputs" — so a routine whose argument is which
// month to bill could only ever be run at its default month. This is the
// surface that fixes that, and it has to agree with the slash palette: same
// translation, same defaults, same typed body.
// =============================================================================

import { RoutineRunInputsDialog } from "../routine-run-inputs-dialog"
import type { RoutineInputSpec } from "@/lib/routine-inputs"

const msnInputs: RoutineInputSpec[] = [
  { name: "obdobi", type: "string" },
  { name: "ucetnictvi_root", type: "string", default: "Unify - Účetnictví" },
  { name: "vypis_odesilatel", type: "string", default: "info@rb.cz" },
]

const onRun = vi.fn()
const onCancel = vi.fn()

beforeEach(() => vi.clearAllMocks())

function open(inputs: RoutineInputSpec[] | null) {
  return render(
    <RoutineRunInputsDialog
      inputs={inputs}
      routineName="msn-etn-podklady"
      onCancel={onCancel}
      onRun={onRun}
    />,
  )
}

describe("routine run inputs dialog", () => {
  it("renders nothing for a routine that declares no inputs", () => {
    // That button keeps its single-click behaviour: the caller runs
    // straight away rather than opening an empty form.
    const { container } = open([])
    expect(container).toBeEmptyDOMElement()
    open(null)
    expect(screen.queryByRole("dialog")).toBeNull()
  })

  it("opens prefilled with the routine's declared defaults", () => {
    open(msnInputs)
    expect(screen.getByLabelText(/ucetnictvi root/i)).toHaveValue("Unify - Účetnictví")
    expect(screen.getByLabelText(/vypis odesilatel/i)).toHaveValue("info@rb.cz")
    // No default declared: an empty period is what means "last month".
    expect(screen.getByLabelText(/obdobi/i)).toHaveValue("")
  })

  it("hands back the typed inputs the user filled in", () => {
    open(msnInputs)
    fireEvent.change(screen.getByLabelText(/obdobi/i), { target: { value: "2026-07" } })
    fireEvent.click(screen.getByRole("button", { name: "Run" }))

    expect(onRun).toHaveBeenCalledWith({
      obdobi: "2026-07",
      ucetnictvi_root: "Unify - Účetnictví",
      vypis_odesilatel: "info@rb.cz",
    })
  })

  it("omits a field left empty so the routine's own default applies", () => {
    open(msnInputs)
    fireEvent.change(screen.getByLabelText(/ucetnictvi root/i), { target: { value: "" } })
    fireEvent.click(screen.getByRole("button", { name: "Run" }))

    const inputs = onRun.mock.calls[0][0]
    expect("ucetnictvi_root" in inputs).toBe(false)
    expect("obdobi" in inputs).toBe(false)
    expect(inputs.vypis_odesilatel).toBe("info@rb.cz")
  })

  it("sends declared types as themselves, not as the strings typed into the boxes", () => {
    open([
      { name: "count", type: "integer", default: 10 },
      { name: "dry_run", type: "boolean" },
    ])
    // The default arrived as the number 10 and renders as "10", not "10.0".
    expect(screen.getByLabelText(/count/i)).toHaveValue(10)
    fireEvent.change(screen.getByLabelText(/count/i), { target: { value: "42" } })
    fireEvent.click(screen.getByRole("button", { name: "Run" }))

    // A `code` step sees inputs with their original types, so an integer
    // arriving as "42" fails the run at that step. An unticked checkbox
    // sends false rather than being omitted — it has no state that means
    // "leave this alone".
    expect(onRun).toHaveBeenCalledWith({ count: 42, dry_run: false })
  })

  it("refuses a required field left blank, without starting a run", () => {
    open([{ name: "obdobi", type: "string", required: true }])
    fireEvent.click(screen.getByRole("button", { name: "Run" }))

    expect(onRun).not.toHaveBeenCalled()
    expect(screen.getByTestId("routine-input-error-obdobi")).toHaveTextContent("Required")
  })

  it("lets a required boolean be submitted as false", () => {
    // A checkbox emits "" when unticked. A blank-string required check
    // would report "Required" until it was TICKED, leaving the user no
    // way at all to answer `false`.
    open([{ name: "confirm", type: "boolean", required: true }])
    fireEvent.click(screen.getByRole("button", { name: "Run" }))

    expect(screen.queryByTestId("routine-input-error-confirm")).toBeNull()
    expect(onRun).toHaveBeenCalledWith({ confirm: false })
  })

  it("refuses a value it cannot restore, naming the field under the box", () => {
    open([{ name: "opts", type: "object" }])
    fireEvent.change(screen.getByLabelText(/opts/i), { target: { value: "{not json" } })
    fireEvent.click(screen.getByRole("button", { name: "Run" }))

    expect(onRun).not.toHaveBeenCalled()
    // Under the field rather than in a toast: a form of six inputs and a
    // floating "not valid JSON" is a puzzle, not an error message.
    expect(screen.getByTestId("routine-input-error-opts")).toHaveTextContent(/not valid JSON/)
  })

  it("clears a field's error as soon as it is edited", () => {
    open([{ name: "obdobi", type: "string", required: true }])
    fireEvent.click(screen.getByRole("button", { name: "Run" }))
    expect(screen.getByTestId("routine-input-error-obdobi")).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText(/obdobi/i), { target: { value: "2026-07" } })
    // Leaving it up while the user fixes it reads as "still wrong".
    expect(screen.queryByTestId("routine-input-error-obdobi")).toBeNull()
  })

  it("cancels without running", () => {
    open(msnInputs)
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(onCancel).toHaveBeenCalled()
    expect(onRun).not.toHaveBeenCalled()
  })
})
