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
  it("shows what the run will do and asks for confirmation even without inputs", () => {
    open([])
    expect(screen.getByRole("dialog")).toBeInTheDocument()
    const effects = screen.getByRole("region", { name: "Run effects" })
    expect(effects).toHaveTextContent(/^This run will:/)
    expect(effects).toHaveTextContent("Stopping does not undo what already happened.")
    expect(screen.queryByText("What this run can do")).toBeNull()
    expect(screen.getByText("Esc closes · nothing has started")).toBeInTheDocument()
    expect(onRun).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Run now" }))
    expect(onRun).toHaveBeenCalledWith({})
  })

  it("says which version the run uses and that the draft is not it, with the facts as chips", () => {
    render(
      <RoutineRunInputsDialog
        inputs={[]}
        routineName="Invoice intake"
        headVersion={3}
        draft={{ revision: 2 }}
        crewName="Finance"
        definition={{
          estimated_cost_usd: 0.05,
          estimated_duration_seconds: 120,
          credentials_required: [{ type: "erp_token" }],
          steps: [
            { id: "extract", type: "agent_run", agent_slug: "nora" },
            { id: "verify", type: "agent_run", agent_slug: "vale" },
            {
              id: "decide",
              type: "wait",
              if: "total > inputs.limit",
              wait: { kind: "approval", approvers: ["Finance approvers"] },
            },
            { id: "post", type: "script", script: { path: "scripts/ledger-post.go" } },
            { id: "notify", type: "notify", notify: { to: "#finance" } },
          ],
        }}
        onCancel={onCancel}
        onRun={onRun}
      />,
    )
    expect(screen.getByText(/Uses/).textContent).toBe(
      "Uses v3 — the unpublished draft r2 is not used · a new run is added to History.",
    )
    const facts = screen.getByRole("list", { name: "Run facts" })
    expect(facts.textContent).toBe(
      "Version v3Crew · FinanceFinance approvers decides when total > inputs.limit≈ $0.05≈ 2 min",
    )
    expect(screen.getByRole("region", { name: "Run effects" })).toHaveTextContent(
      "This run will: ask nora and vale to do the agent steps, stop and ask Finance approvers to decide, run a script on the crew's share and notify #finance. It uses erp_token from the vault. Stopping does not undo what already happened.",
    )
  })

  it("omits the facts the recipe does not declare — never a zero", () => {
    render(
      <RoutineRunInputsDialog
        inputs={[]}
        routineName="Quiet"
        headVersion={1}
        definition={{ estimated_cost_usd: 0, steps: [{ id: "t", type: "transform" }] }}
        onCancel={onCancel}
        onRun={onRun}
      />,
    )
    expect(screen.getByRole("list", { name: "Run facts" }).textContent).toBe("Version v1")
    expect(screen.queryByText(/\$0/)).toBeNull()
    expect(screen.getByText(/Uses/).textContent).toBe("Uses v1 · a new run is added to History.")
    expect(screen.getByRole("region", { name: "Run effects" })).toHaveTextContent(
      "This run will: record a result without agents, calls or scripts.",
    )
  })

  it("summarises the errors at the top on submit and focuses the first invalid field", () => {
    open([
      { name: "invoice_path", type: "string", required: true },
      { name: "limit", type: "number", max: 5000, default: 1500 },
    ])
    fireEvent.change(screen.getByLabelText(/limit/i), { target: { value: "9000" } })
    fireEvent.click(screen.getByRole("button", { name: "Run now" }))
    expect(onRun).not.toHaveBeenCalled()
    expect(screen.getByTestId("routine-input-error-summary")).toHaveTextContent(
      "2 answers need a fix — see below.",
    )
    expect(screen.getByTestId("routine-input-error-summary")).toHaveAttribute("role", "alert")
    expect(screen.getByLabelText(/invoice path/i)).toHaveFocus()
    expect(screen.getByText("Changed from the recipe default (1500)")).toBeInTheDocument()
    // Fixing one answer brings the count down; fixing both clears the summary.
    fireEvent.change(screen.getByLabelText(/invoice path/i), {
      target: { value: "/crew/shared/a.pdf" },
    })
    expect(screen.getByTestId("routine-input-error-summary")).toHaveTextContent(
      "1 answer needs a fix",
    )
    fireEvent.click(screen.getByRole("button", { name: "Restore default" }))
    expect(screen.queryByTestId("routine-input-error-summary")).toBeNull()
  })

  it("keeps the Schedule wording for a one-time start", () => {
    render(
      <RoutineRunInputsDialog
        inputs={[]}
        submitLabel="Schedule"
        routineName="recipe"
        headVersion={2}
        onCancel={onCancel}
        onRun={onRun}
      />,
    )
    expect(screen.getByText(/Uses/).textContent).toBe(
      "Uses v2 · the routine will start at the selected date and time.",
    )
    expect(screen.getByText("Esc closes · nothing is scheduled yet")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Schedule" }))
    expect(onRun).toHaveBeenCalledWith({})
  })
  it("stays closed until requested", () => {
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
    fireEvent.change(screen.getByLabelText(/obdobi/i), {
      target: { value: "2026-07" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Run now" }))

    expect(onRun).toHaveBeenCalledWith({
      obdobi: "2026-07",
      ucetnictvi_root: "Unify - Účetnictví",
      vypis_odesilatel: "info@rb.cz",
    })
  })

  it("omits a field left empty so the routine's own default applies", () => {
    open(msnInputs)
    fireEvent.change(screen.getByLabelText(/ucetnictvi root/i), {
      target: { value: "" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Run now" }))

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
    fireEvent.change(screen.getByLabelText(/count/i), {
      target: { value: "42" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Run now" }))

    // A `code` step sees inputs with their original types, so an integer
    // arriving as "42" fails the run at that step. An unticked checkbox
    // sends false rather than being omitted — it has no state that means
    // "leave this alone".
    expect(onRun).toHaveBeenCalledWith({ count: 42, dry_run: false })
  })

  it("refuses a required field left blank, without starting a run", () => {
    open([{ name: "obdobi", type: "string", required: true }])
    fireEvent.click(screen.getByRole("button", { name: "Run now" }))

    expect(onRun).not.toHaveBeenCalled()
    expect(screen.getByTestId("routine-input-error-obdobi")).toHaveTextContent(/required/i)
  })

  it("lets a required boolean be submitted as false", () => {
    // A checkbox emits "" when unticked. A blank-string required check
    // would report "Required" until it was TICKED, leaving the user no
    // way at all to answer `false`.
    open([{ name: "confirm", type: "boolean", required: true }])
    fireEvent.click(screen.getByRole("button", { name: "Run now" }))

    expect(screen.queryByTestId("routine-input-error-confirm")).toBeNull()
    expect(onRun).toHaveBeenCalledWith({ confirm: false })
  })

  it("refuses a value it cannot restore, naming the field under the box", () => {
    open([{ name: "opts", type: "object" }])
    fireEvent.change(screen.getByLabelText(/opts/i), {
      target: { value: "{not json" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Run now" }))

    expect(onRun).not.toHaveBeenCalled()
    // Under the field rather than in a toast: a form of six inputs and a
    // floating "not valid JSON" is a puzzle, not an error message.
    expect(screen.getByTestId("routine-input-error-opts")).toHaveTextContent(
      /not valid JSON/,
    )
  })

  it("clears a field's error as soon as it is edited", () => {
    open([{ name: "obdobi", type: "string", required: true }])
    fireEvent.click(screen.getByRole("button", { name: "Run now" }))
    expect(screen.getByTestId("routine-input-error-obdobi")).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText(/obdobi/i), {
      target: { value: "2026-07" },
    })
    // Leaving it up while the user fixes it reads as "still wrong".
    expect(screen.queryByTestId("routine-input-error-obdobi")).toBeNull()
  })

  it("cancels without running", () => {
    open(msnInputs)
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(onCancel).toHaveBeenCalled()
    expect(onRun).not.toHaveBeenCalled()
  })
  it("does not dismiss an in-flight start through the dialog close button", () => {
    render(
      <RoutineRunInputsDialog
        inputs={msnInputs}
        routineName="recipe"
        submitting
        onCancel={onCancel}
        onRun={onRun}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "Close" }))
    expect(onCancel).not.toHaveBeenCalled()
    expect(screen.getByRole("dialog")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Running…" })).toBeDisabled()
  })
})
