import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

import { StepInspector } from "../shared"
import { DSL_BY_FIDELITY } from "@/lib/routines-preview/fixtures"

// CodeMirror needs a real layout to mount and contributes nothing to
// the assertion — swap it for a plain element that exposes the source
// it was handed. What we are testing is WHICH definition reaches the
// editor, not how the editor draws it.
vi.mock("@/components/features/files/file-editor", () => ({
  FileEditor: ({ code }: { code: string }) => <pre data-testid="code">{code}</pre>,
}))

// A prior review caught the empty-selection branch printing a
// hardcoded "granular" definition while the canvas drew whatever the
// fidelity toggle said — on "Dnes" the graph showed 7 steps and the rail showed
// the 14-step JSON. Nothing caught it: it typechecks, and every other
// test passed. These pin the contract the whole split design rests on,
// which is that the two halves cannot disagree.

const code = () => screen.getByTestId("code").textContent ?? ""

describe("<StepInspector> with nothing selected", () => {
  it("prints the definition matching the fidelity being drawn — today", () => {
    render(<StepInspector dsl={DSL_BY_FIDELITY.today} fidelity="today" stepId={null} />)
    // `reconcile` exists only in the production shape…
    expect(code()).toContain("reconcile")
    // …and `kontrolni_soucet` only in the granulated one.
    expect(code()).not.toContain("kontrolni_soucet")
  })

  it("prints the definition matching the fidelity being drawn — granular", () => {
    render(<StepInspector dsl={DSL_BY_FIDELITY.granular} fidelity="granular" stepId={null} />)
    expect(code()).toContain("kontrolni_soucet")
    expect(code()).not.toContain('"id": "reconcile"')
  })
})

describe("<StepInspector> with a step selected", () => {
  it("shows that step's fragment, what it waits on, and what it unblocks", () => {
    render(
      <StepInspector dsl={DSL_BY_FIDELITY.granular} fidelity="granular" stepId="sbirat" />,
    )
    expect(screen.getByText("sbirat")).toBeInTheDocument()
    expect(screen.getByText("foreach")).toBeInTheDocument()
    // waits on
    expect(screen.getByText("worklist")).toBeInTheDocument()
    expect(screen.getByText("obdobi")).toBeInTheDocument()
    // unblocks
    expect(screen.getByText("dohledat_doklad")).toBeInTheDocument()
  })

  it("falls back to the whole definition when the id is not in this fidelity", () => {
    // Selecting a granular-only id and then flipping to "today" must
    // not blank the rail — it degrades to the full definition.
    render(
      <StepInspector dsl={DSL_BY_FIDELITY.today} fidelity="today" stepId="kontrolni_soucet" />,
    )
    expect(screen.getByTestId("code")).toBeInTheDocument()
    expect(code()).toContain("reconcile")
  })
})
