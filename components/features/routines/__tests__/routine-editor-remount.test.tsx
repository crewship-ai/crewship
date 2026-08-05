import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, act } from "@testing-library/react"

import { RoutineEditorTab } from "../routine-editor-tab"
import type { RoutineDetail } from "../routines-detail-panel"

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

// FileEditor rebuilds its CodeMirror EditorState whenever `code`
// changes — that is how Format, Revert and a refetch replace the
// buffer. So `code` must NOT be the live document: feeding the live
// document back rebuilds the editor on every keystroke, and a rebuilt
// editor starts the caret at position 0.
//
// The symptom is unmistakable and the cause is invisible from the
// outside: you type, the letter lands, and the caret snaps to line 1,
// so the next letter goes there instead. A routine gets shredded in
// about four seconds.
//
// This asserts the prop directly, because no user-facing query can see
// it: the DOM after a rebuild looks exactly like the DOM before one.

const codeProps: string[] = []
let emitDocChange: ((text: string) => void) | null = null

vi.mock("@/components/features/files/file-editor", () => ({
  FileEditor: ({
    code,
    onDocChange,
    onDirtyChange,
  }: {
    code: string
    onDocChange?: (t: string) => void
    onDirtyChange?: (d: boolean) => void
  }) => {
    codeProps.push(code)
    emitDocChange = (t: string) => {
      onDirtyChange?.(true)
      onDocChange?.(t)
    }
    return <div data-testid="editor" />
  },
}))

function routine(): RoutineDetail {
  return {
    id: "p1",
    slug: "approval-gate-demo",
    name: "Approval gate demo",
    description: "",
    dsl_version: "1.0",
    definition: {
      name: "approval-gate-demo",
      dsl_version: "1.0",
      steps: [{ id: "draft", type: "agent_run", agent_slug: "morgan" }],
    },
    definition_hash: "abc",
    ephemeral: false,
    workspace_visible: true,
    invocation_count: 0,
    authored_via: "user_api",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
  } as RoutineDetail
}

describe("<RoutineEditorTab> does not rebuild the editor while you type", () => {
  beforeEach(() => {
    codeProps.length = 0
    emitDocChange = null
  })

  it("keeps the `code` prop stable across document changes", () => {
    render(<RoutineEditorTab routine={routine()} workspaceId="ws-1" onSaved={vi.fn()} />)
    const initial = codeProps[codeProps.length - 1]

    act(() => emitDocChange!(initial + "\n# typing"))
    act(() => emitDocChange!(initial + "\n# typing more"))

    // Every render after the edits must still hand CodeMirror the same
    // document it was constructed with. A change here is a rebuild, and
    // a rebuild is a caret at line 1.
    const after = codeProps[codeProps.length - 1]
    expect(after).toBe(initial)
  })

  it("still reflects the live document in its validity state", () => {
    const { getByText } = render(
      <RoutineEditorTab routine={routine()} workspaceId="ws-1" onSaved={vi.fn()} />,
    )
    // Not rebuilding must not mean not noticing: break the buffer and
    // the header has to say so.
    act(() => emitDocChange!("\t\tnot valid yaml"))
    expect(getByText(/tabs are not allowed|invalid|error/i)).toBeTruthy()
  })
})

// Switching YAML↔JSON threw away unsaved work.
//
// `initial` was recomputed whenever `format` changed, and the reset
// effect keyed on `initial` — so the moment switchFormat committed the
// new format, the effect fired and overwrote the freshly converted
// buffer with the stored definition, then cleared `dirty` so Save went
// disabled. The conversion the switch performed was undone one tick
// later by the effect that exists to seed the editor from the server.
//
// CodeRabbit caught this on the PR. The failure is silent: the editor
// still shows a valid document, just not yours.

describe("<RoutineEditorTab> format switch", () => {
  beforeEach(() => {
    codeProps.length = 0
    emitDocChange = null
  })

  it("keeps unsaved edits when the format changes", () => {
    const { getByRole } = render(
      <RoutineEditorTab routine={routine()} workspaceId="ws-1" onSaved={vi.fn()} />,
    )
    act(() => emitDocChange!("name: edited-before-switch\nsteps: []\n"))

    act(() => {
      getByRole("button", { name: /^json$/i }).click()
    })

    // The converted buffer must carry the edit, not the stored definition.
    expect(codeProps[codeProps.length - 1]).toContain("edited-before-switch")
  })

  it("does not silently disable Save by clearing the dirty flag", () => {
    const { getByRole, getByText } = render(
      <RoutineEditorTab routine={routine()} workspaceId="ws-1" onSaved={vi.fn()} />,
    )
    act(() => emitDocChange!("name: edited-before-switch\nsteps: []\n"))
    act(() => {
      getByRole("button", { name: /^json$/i }).click()
    })
    // The unsaved marker is how a reader knows there is work to lose.
    expect(getByText("unsaved")).toBeTruthy()
  })
})
