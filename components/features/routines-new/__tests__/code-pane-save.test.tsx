import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

import { CodePane } from "../shared"

// A prior review caught the pane reporting "Uloženo · graf překreslen" while
// doing nothing of the kind: handleSave flipped local state, ignored
// the editor content, and could not reach the DSL the canvas draws. On
// a page whose entire thesis is "edit the code, the graph follows",
// claiming the redraw without performing it is the one lie that
// invalidates the design being reviewed.
//
// It now parses the buffer and hands the result up. These pin both
// halves: the applied path AND the refusal.

let emitSave: ((content: string) => void) | null = null
vi.mock("@/components/features/files/file-editor", () => ({
  FileEditor: ({
    onSave,
    saveRef,
  }: {
    onSave: (c: string) => void
    saveRef?: React.MutableRefObject<(() => void) | null>
  }) => {
    emitSave = onSave
    // Stand in for the real editor's saveRef, which reads the live
    // document. Tests drive the content through it.
    if (saveRef) saveRef.current = () => onSave(CURRENT_DOC)
    return <div data-testid="editor" />
  },
}))

let CURRENT_DOC = ""

const VALID = JSON.stringify(
  {
    name: "edited",
    steps: [
      { id: "only", type: "http", http: { method: "GET", url: "https://example.test" } },
    ],
  },
  null,
  2,
)

const saveButton = () => screen.getByRole("button", { name: /uložit|uloženo/i })

describe("<CodePane> save", () => {
  beforeEach(() => {
    emitSave = null
    CURRENT_DOC = VALID
  })

  it("hands the parsed definition up so the canvas can redraw", () => {
    const onApply = vi.fn()
    render(<CodePane fidelity="granular" onApply={onApply} />)
    fireEvent.click(saveButton())
    expect(onApply).toHaveBeenCalledTimes(1)
    const applied = onApply.mock.calls[0][0]
    expect(applied.steps).toHaveLength(1)
    expect(applied.steps[0].id).toBe("only")
  })

  it("confirms the redraw only after one actually happened", () => {
    render(<CodePane fidelity="granular" onApply={vi.fn()} />)
    expect(screen.queryByText(/graf překreslen/i)).not.toBeInTheDocument()
    fireEvent.click(saveButton())
    expect(screen.getByText(/graf překreslen/i)).toBeInTheDocument()
  })

  it("refuses a buffer that does not parse, and says so", () => {
    const onApply = vi.fn()
    CURRENT_DOC = "{ not json"
    render(<CodePane fidelity="granular" onApply={onApply} />)
    fireEvent.click(saveButton())
    expect(onApply).not.toHaveBeenCalled()
    expect(screen.queryByText(/graf překreslen/i)).not.toBeInTheDocument()
    expect(screen.getByText(/syntax/i)).toBeInTheDocument()
  })

  it("refuses valid JSON that is not a routine", () => {
    const onApply = vi.fn()
    CURRENT_DOC = JSON.stringify({ name: "no steps here" })
    render(<CodePane fidelity="granular" onApply={onApply} />)
    fireEvent.click(saveButton())
    expect(onApply).not.toHaveBeenCalled()
    expect(screen.queryByText(/graf překreslen/i)).not.toBeInTheDocument()
  })

  it("recovers once the buffer parses again", () => {
    const onApply = vi.fn()
    CURRENT_DOC = "{ not json"
    render(<CodePane fidelity="granular" onApply={onApply} />)
    fireEvent.click(saveButton())
    expect(onApply).not.toHaveBeenCalled()

    CURRENT_DOC = VALID
    fireEvent.click(saveButton())
    expect(onApply).toHaveBeenCalledTimes(1)
    expect(screen.getByText(/graf překreslen/i)).toBeInTheDocument()
  })

  it("does not claim anything when nothing is listening", () => {
    render(<CodePane fidelity="granular" />)
    expect(() => fireEvent.click(saveButton())).not.toThrow()
  })

  it("accepts a YAML buffer, which is what the pane renders by default", () => {
    const onApply = vi.fn()
    CURRENT_DOC = `name: edited
steps:
  - id: only
    type: agent_run
    prompt: |
      first line
      second line
`
    render(<CodePane fidelity="granular" onApply={onApply} />)
    fireEvent.click(saveButton())
    expect(onApply).toHaveBeenCalledTimes(1)
    const applied = onApply.mock.calls[0][0]
    // The block scalar has to survive as real newlines — this is the
    // entire reason for authoring in YAML.
    expect(applied.steps[0].prompt).toBe("first line\nsecond line\n")
  })

  it("reports the line of a YAML error, not just the message", () => {
    const onApply = vi.fn()
    CURRENT_DOC = "name: x\nsteps:\n\t- id: a\n"
    render(<CodePane fidelity="granular" onApply={onApply} />)
    fireEvent.click(saveButton())
    expect(onApply).not.toHaveBeenCalled()
    expect(screen.getByText(/řádek 3/)).toBeInTheDocument()
  })

  it("keeps Cmd+S and the button on the same path", () => {
    const onApply = vi.fn()
    render(<CodePane fidelity="granular" onApply={onApply} />)
    // The editor's own keymap calls onSave directly with the document.
    emitSave!(VALID)
    expect(onApply).toHaveBeenCalledTimes(1)
  })
})
