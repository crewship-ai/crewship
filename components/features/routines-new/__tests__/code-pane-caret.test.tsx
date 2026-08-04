import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, act } from "@testing-library/react"

import { CodePane } from "../shared"
import { dslSource } from "@/lib/routines-preview/fixtures"
import { convertDsl } from "@/lib/routine-dsl-format"
import { stepLineRanges } from "@/lib/routine-dsl-lines"

// CodeMirror cannot mount in happy-dom and is not what is under test.
// The stub captures the onCursorLine callback so a test can move the
// caret directly — the assertion is about what CodePane does with a
// caret position, not about how the editor produces one.
let emitCursorLine: ((line: number) => void) | null = null
let emitDocChange: ((text: string) => void) | null = null
vi.mock("@/components/features/files/file-editor", () => ({
  FileEditor: ({
    onCursorLine,
    onDocChange,
  }: {
    onCursorLine?: (line: number) => void
    onDocChange?: (text: string) => void
  }) => {
    emitCursorLine = onCursorLine ?? null
    emitDocChange = onDocChange ?? null
    return <div data-testid="editor" />
  },
}))

// The caret fires on every arrow key. If CodePane forwarded each one,
// the canvas would be told to re-centre on the node it is already
// centred on dozens of times a second while someone types — the
// viewport would fight the user for control. Only a change of STEP is
// news, and that dedupe is CodePane's job, not the caller's.

// The pane renders YAML by default, so the line numbers a test drives
// the caret to have to come from the YAML rendering — measuring the
// JSON and typing those lines into a YAML buffer lands somewhere else
// entirely.
const yamlSource = (() => {
  const r = convertDsl(dslSource("granular"), "json", "yaml")
  if (!r.ok) throw new Error("fixture must convert to YAML")
  return r.text
})()
const ranges = stepLineRanges(yamlSource)

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


// The follow feature broke the moment anyone edited: line spans were
// computed once from the definition as first rendered, so inserting a
// line shifted every step below it and the caret resolved against
// positions that no longer existed. "It works until you use it" is the
// worst failure mode for a feature whose whole job is to track you.
describe("<CodePane> caret after an edit", () => {
  beforeEach(() => {
    emitCursorLine = null
    emitDocChange = null
  })

  it("re-reads the line spans from the edited buffer", async () => {
    const onStepAtCaret = vi.fn()
    render(<CodePane fidelity="granular" onStepAtCaret={onStepAtCaret} />)

    // Two blank lines pushed in at the top: every step is now two
    // lines lower than the pristine rendering said.
    const edited = "\n\n" + yamlSource
    await act(async () => {
      emitDocChange!(edited)
    })

    const shifted = stepLineRanges(edited)
    const worklist = shifted.find((r) => r.id === "worklist")!
    await act(async () => {
      emitCursorLine!(worklist.startLine + 1)
    })
    expect(onStepAtCaret).toHaveBeenLastCalledWith("worklist")
  })

  it("resolves the OLD position to something else once the buffer moved", async () => {
    const onStepAtCaret = vi.fn()
    render(<CodePane fidelity="granular" onStepAtCaret={onStepAtCaret} />)
    const before = ranges.find((r) => r.id === "sbirat")!

    await act(async () => {
      emitDocChange!("\n\n\n\n\n\n" + yamlSource)
    })
    await act(async () => {
      emitCursorLine!(before.startLine + 1)
    })
    // Six lines of shift means that row is no longer `sbirat`; the
    // point is that the mapper moved with the buffer rather than
    // confidently reporting a stale answer.
    const last = onStepAtCaret.mock.calls.at(-1)?.[0]
    expect(last).not.toBe("sbirat")
  })
})
