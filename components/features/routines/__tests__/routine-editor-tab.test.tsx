import { describe, it, expect, vi, beforeEach, afterAll } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { RoutineEditorTab } from "../routine-editor-tab"
import type { RoutineDetail } from "../routines-detail-panel"

// Mock sonner toast
vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
    info: vi.fn(),
  },
}))

// Mock FileEditor with the same contract as the real CodeMirror-backed
// component: the buffer lives INSIDE the editor, and the parent only
// receives it through onSave(buffer) — fired on ⌘S or when the parent
// invokes saveRef.current() (synchronously, like the real handleSave
// at components/features/files/file-editor.tsx). Typing does NOT call
// onSave, which is exactly the gap the stale-save regression lives in.
vi.mock("@/components/features/files/file-editor", async () => {
  const { useEffect, useRef } = await import("react")
  function FileEditor({
    code,
    onSave,
    onDirtyChange,
    saveRef,
  }: {
    code: string
    language: string
    onSave: (content: string) => void
    onDirtyChange?: (dirty: boolean) => void
    saveRef?: React.MutableRefObject<(() => void) | null>
  }) {
    const bufRef = useRef(code)
    useEffect(() => {
      if (saveRef) saveRef.current = () => onSave(bufRef.current)
      return () => {
        if (saveRef) saveRef.current = null
      }
    })
    return (
      <textarea
        data-testid="mock-editor"
        defaultValue={code}
        onChange={(e) => {
          bufRef.current = e.target.value
          onDirtyChange?.(bufRef.current !== code)
        }}
      />
    )
  }
  return { FileEditor }
})

const routine: RoutineDetail = {
  id: "pl_1",
  slug: "daily-etl",
  name: "Daily ETL",
  dsl_version: "1",
  definition: { name: "Daily ETL", steps: [{ id: "s1", kind: "agent" }] },
  definition_hash: "hash",
  ephemeral: false,
  workspace_visible: true,
  invocation_count: 0,
  authored_via: "user_api",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
}

describe("RoutineEditorTab", () => {
  const originalFetch = global.fetch

  beforeEach(() => {
    vi.clearAllMocks()
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({}),
    })
  })

  afterAll(() => {
    global.fetch = originalFetch
  })

  it("saves the editor's current buffer, not the stale text state", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    render(
      <RoutineEditorTab routine={routine} workspaceId="ws-1" onSaved={vi.fn()} />,
    )

    // Type into the editor WITHOUT pressing ⌘S — the buffer diverges
    // from the parent's `text` state.
    const next = JSON.stringify(
      { name: "Daily ETL v2", steps: [{ id: "s1", kind: "agent" }] },
      null,
      2,
    )
    fireEvent.change(screen.getByTestId("mock-editor"), { target: { value: next } })

    fireEvent.click(screen.getByText("Save"))

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalled()
    })
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain("/api/v1/workspaces/ws-1/pipelines/save")
    const body = JSON.parse(init.body as string)
    // The POST must carry the NEW buffer content — before the fix it
    // silently shipped the previous definition and reported success.
    expect(body.name).toBe("Daily ETL v2")
    expect(body.definition).toEqual({
      name: "Daily ETL v2",
      steps: [{ id: "s1", kind: "agent" }],
    })
  })

  it("blocks save when the current buffer is invalid, even if state is stale-valid", async () => {
    const { toast } = await import("sonner")
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    render(
      <RoutineEditorTab routine={routine} workspaceId="ws-1" onSaved={vi.fn()} />,
    )

    // Buffer is broken JSON; the `text` state still holds the valid
    // initial definition, so the Save button is enabled.
    fireEvent.change(screen.getByTestId("mock-editor"), {
      target: { value: "{ not valid json" },
    })
    fireEvent.click(screen.getByText("Save"))

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalled()
    })
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
