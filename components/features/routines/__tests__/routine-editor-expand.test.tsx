import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, act } from "@testing-library/react"

import { RoutineEditorTab } from "../routine-editor-tab"
import type { RoutineDetail } from "../routines-detail-panel"

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

// Beside the graph the editor is a 48%-wide column — right for reading
// one step, wrong for reading a routine. Expanded it takes the window
// with the page blurred behind it.
//
// The thing that must not happen is a SECOND editor mounted over the
// first: a duplicate carries its own CodeMirror state, so expanding
// mid-edit would show the buffer as it was on load and silently throw
// away what had been typed.

const codeProps: string[] = []
let emitDocChange: ((text: string) => void) | null = null

vi.mock("@/components/features/files/file-editor", () => ({
  FileEditor: ({ code, onDocChange }: { code: string; onDocChange?: (t: string) => void }) => {
    codeProps.push(code)
    emitDocChange = onDocChange ?? null
    return <div data-testid="editor" />
  },
}))

function routine(): RoutineDetail {
  return {
    id: "p1",
    slug: "nightly",
    name: "Nightly",
    dsl_version: "1.0",
    definition: { name: "nightly", dsl_version: "1.0", steps: [{ id: "a", type: "agent_run" }] },
    definition_hash: "abc",
    ephemeral: false,
    workspace_visible: true,
    invocation_count: 0,
    authored_via: "user_api",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
  } as RoutineDetail
}

describe("<RoutineEditorTab> full-screen", () => {
  beforeEach(() => {
    codeProps.length = 0
    emitDocChange = null
  })

  it("mounts exactly one editor when expanded", () => {
    render(<RoutineEditorTab routine={routine()} workspaceId="ws-1" onSaved={vi.fn()} />)
    fireEvent.click(screen.getByLabelText("Expand editor"))
    expect(screen.getAllByTestId("editor")).toHaveLength(1)
  })

  it("carries unsaved work into the full-screen editor", () => {
    // Expanding moves the editor to a different place in the tree, so
    // React remounts it and it is rebuilt from `text` — which by
    // design does not track typing. Without an explicit hand-off the
    // full-screen editor opens on the pre-edit document and the work
    // in between is gone with no error and no undo.
    render(<RoutineEditorTab routine={routine()} workspaceId="ws-1" onSaved={vi.fn()} />)
    const typed = "name: edited-while-small\nsteps: []\n"
    act(() => emitDocChange!(typed))
    fireEvent.click(screen.getByLabelText("Expand editor"))

    expect(codeProps[codeProps.length - 1]).toBe(typed)
  })

  it("carries it back when Escape collapses the editor", () => {
    render(<RoutineEditorTab routine={routine()} workspaceId="ws-1" onSaved={vi.fn()} />)
    fireEvent.click(screen.getByLabelText("Expand editor"))
    const typed = "name: edited-while-large\nsteps: []\n"
    act(() => emitDocChange!(typed))
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }))
    })
    expect(codeProps[codeProps.length - 1]).toBe(typed)
  })

  it("closes on Escape", () => {
    render(<RoutineEditorTab routine={routine()} workspaceId="ws-1" onSaved={vi.fn()} />)
    fireEvent.click(screen.getByLabelText("Expand editor"))
    expect(screen.getByLabelText("Collapse editor")).toBeInTheDocument()
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }))
    })
    expect(screen.getByLabelText("Expand editor")).toBeInTheDocument()
  })

  it("keeps Format reachable without a label", () => {
    // The word went; the action did not.
    render(<RoutineEditorTab routine={routine()} workspaceId="ws-1" onSaved={vi.fn()} />)
    expect(screen.getByLabelText("Format")).toBeInTheDocument()
  })
})
