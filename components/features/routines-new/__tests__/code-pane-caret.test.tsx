import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, act } from "@testing-library/react"

import { CodePane } from "../shared"
import { dslSource } from "@/lib/routines-preview/fixtures"
import { stepLineRanges } from "@/lib/routine-dsl-lines"

// CodeMirror cannot mount in happy-dom and is not what is under test.
// The stub captures the onCursorLine callback so a test can move the
// caret directly — the assertion is about what CodePane does with a
// caret position, not about how the editor produces one.
let emitCursorLine: ((line: number) => void) | null = null
vi.mock("@/components/features/files/file-editor", () => ({
  FileEditor: ({ onCursorLine }: { onCursorLine?: (line: number) => void }) => {
    emitCursorLine = onCursorLine ?? null
    return <div data-testid="editor" />
  },
}))

// The caret fires on every arrow key. If CodePane forwarded each one,
// the canvas would be told to re-centre on the node it is already
// centred on dozens of times a second while someone types — the
// viewport would fight the user for control. Only a change of STEP is
// news, and that dedupe is CodePane's job, not the caller's.

const ranges = stepLineRanges(dslSource("granular"))

/** A line comfortably inside the given step's span. */
function lineInside(stepId: string): number {
  const r = ranges.find((x) => x.id === stepId)
  if (!r) throw new Error(`no range for ${stepId}`)
  return r.startLine + 1
}

describe("<CodePane> caret → step", () => {
  beforeEach(() => {
    emitCursorLine = null
  })

  it("reports the step the caret lands in", () => {
    const onStepAtCaret = vi.fn()
    render(<CodePane fidelity="granular" onStepAtCaret={onStepAtCaret} />)
    act(() => emitCursorLine!(lineInside("worklist")))
    expect(onStepAtCaret).toHaveBeenCalledWith("worklist")
  })

  it("stays silent while the caret moves WITHIN the same step", () => {
    const onStepAtCaret = vi.fn()
    render(<CodePane fidelity="granular" onStepAtCaret={onStepAtCaret} />)
    const r = ranges.find((x) => x.id === "worklist")!
    act(() => emitCursorLine!(r.startLine))
    act(() => emitCursorLine!(r.startLine + 1))
    act(() => emitCursorLine!(r.endLine))
    expect(onStepAtCaret).toHaveBeenCalledTimes(1)
  })

  it("reports again when the caret crosses into a different step", () => {
    const onStepAtCaret = vi.fn()
    render(<CodePane fidelity="granular" onStepAtCaret={onStepAtCaret} />)
    act(() => emitCursorLine!(lineInside("worklist")))
    act(() => emitCursorLine!(lineInside("sbirat")))
    expect(onStepAtCaret).toHaveBeenCalledTimes(2)
    expect(onStepAtCaret).toHaveBeenLastCalledWith("sbirat")
  })

  it("reports null when the caret leaves the steps array", () => {
    const onStepAtCaret = vi.fn()
    render(<CodePane fidelity="granular" onStepAtCaret={onStepAtCaret} />)
    act(() => emitCursorLine!(lineInside("worklist")))
    act(() => emitCursorLine!(1)) // the opening brace of the document
    expect(onStepAtCaret).toHaveBeenLastCalledWith(null)
  })

  it("does not blow up when no caret consumer is wired", () => {
    render(<CodePane fidelity="granular" />)
    expect(() => act(() => emitCursorLine!(lineInside("worklist")))).not.toThrow()
  })
})

describe("<CodePane> follow toggle", () => {
  it("is hidden when the pane has nothing to follow", () => {
    render(<CodePane fidelity="granular" />)
    expect(screen.queryByRole("button", { name: /sledovat pohyb/i })).not.toBeInTheDocument()
  })

  it("reports its state and flips on click", () => {
    const onFollowChange = vi.fn()
    render(<CodePane fidelity="granular" follow onFollowChange={onFollowChange} />)
    const btn = screen.getByRole("button", { name: /sledovat pohyb/i })
    expect(btn).toHaveAttribute("aria-pressed", "true")
    fireEvent.click(btn)
    expect(onFollowChange).toHaveBeenCalledWith(false)
  })
})
