import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

// =============================================================================
// B9 (#2362) — the reliability editor's schedule dialog. Every §13.2 row that
// has a PATCH .../pipeline-schedules door gets a control here: cron/timezone
// (+ live preview), catch-up policy, wake gate, breaker threshold, version
// pin, enabled. This test suite proves the dialog READS a schedule's current
// values in, WRITES the full edited body back out through onSave, and — the
// row this whole package exists to close — that a live preview is fetched as
// the cron/timezone fields change, without saving anything.
// =============================================================================

import { RoutineScheduleEditorDialog } from "../routine-schedule-editor-dialog"
import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"

function baseSchedule(overrides: Partial<PipelineSchedule> = {}): PipelineSchedule {
  return {
    id: "sch-1",
    workspace_id: "ws-1",
    name: "Morning digest",
    target_pipeline_id: "p1",
    target_pipeline_slug: "digest",
    cron_expr: "0 8 * * *",
    timezone: "UTC",
    inputs: {},
    enabled: true,
    consecutive_failures: 0,
    max_consecutive_failures: 5,
    catchup_policy: "once",
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    ...overrides,
  }
}

const onSave = vi.fn()
const onCancel = vi.fn()
const onPreview = vi.fn()

beforeEach(() => {
  vi.clearAllMocks()
  onPreview.mockResolvedValue({ cron_expr: "0 8 * * *", timezone: "UTC", occurrences: [] })
})

describe("RoutineScheduleEditorDialog", () => {
  it("renders nothing when no schedule is passed", () => {
    const { container } = render(
      <RoutineScheduleEditorDialog schedule={null} onCancel={onCancel} onSave={onSave} onPreview={onPreview} />,
    )
    expect(container).toBeEmptyDOMElement()
  })

  it("prefills every field from the schedule", () => {
    render(
      <RoutineScheduleEditorDialog
        schedule={baseSchedule({ wake_pipeline_slug: "is-ready", wake_fail_closed: true, target_pipeline_version: 4 })}
        onCancel={onCancel}
        onSave={onSave}
        onPreview={onPreview}
      />,
    )
    expect(screen.getByLabelText(/cron expression/i)).toHaveValue("0 8 * * *")
    expect(screen.getByLabelText(/timezone/i)).toHaveValue("UTC")
    expect(screen.getByDisplayValue("is-ready")).toBeInTheDocument()
    expect(screen.getByDisplayValue("4")).toBeInTheDocument()
  })

  it("fetches a live preview as the cron/timezone fields change, without calling onSave", async () => {
    render(
      <RoutineScheduleEditorDialog schedule={baseSchedule()} onCancel={onCancel} onSave={onSave} onPreview={onPreview} />,
    )
    // Mount itself triggers one preview call (useEffect on schedule change).
    await waitFor(() => expect(onPreview).toHaveBeenCalled())
    onPreview.mockClear()

    fireEvent.change(screen.getByLabelText(/cron expression/i), { target: { value: "30 2 * * *" } })
    fireEvent.change(screen.getByLabelText(/timezone/i), { target: { value: "Europe/Prague" } })

    await waitFor(
      () => expect(onPreview).toHaveBeenCalledWith("30 2 * * *", "Europe/Prague", 5),
      { timeout: 2000 },
    )
    expect(onSave).not.toHaveBeenCalled()
  })

  it("renders the preview occurrences once they resolve", async () => {
    onPreview.mockResolvedValue({
      cron_expr: "0 8 * * *",
      timezone: "UTC",
      occurrences: ["2026-09-06T08:00:00Z", "2026-09-07T08:00:00Z"],
    })
    render(
      <RoutineScheduleEditorDialog schedule={baseSchedule()} onCancel={onCancel} onSave={onSave} onPreview={onPreview} />,
    )
    await waitFor(() => {
      expect(screen.getByTestId("schedule-preview")).toHaveTextContent(/Next 2 fire times/)
    }, { timeout: 2000 })
  })

  it("shows a preview error instead of crashing on a bad cron expression", async () => {
    onPreview.mockRejectedValue(new Error("invalid cron expression"))
    render(
      <RoutineScheduleEditorDialog schedule={baseSchedule()} onCancel={onCancel} onSave={onSave} onPreview={onPreview} />,
    )
    await waitFor(() => {
      expect(screen.getByTestId("schedule-preview")).toHaveTextContent(/invalid cron expression/)
    }, { timeout: 2000 })
  })

  it("sends the full edited body on Save, including catch-up policy and breaker threshold", () => {
    render(
      <RoutineScheduleEditorDialog schedule={baseSchedule()} onCancel={onCancel} onSave={onSave} onPreview={onPreview} />,
    )
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "Renamed" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({
        name: "Renamed",
        cron_expr: "0 8 * * *",
        timezone: "UTC",
        catchup_policy: "once",
        max_consecutive_failures: 5,
        enabled: true,
      }),
    )
  })

  it("sends target_pipeline_version: null when the pin field is cleared (explicit unpin, not omission)", () => {
    render(
      <RoutineScheduleEditorDialog
        schedule={baseSchedule({ target_pipeline_version: 3 })}
        onCancel={onCancel}
        onSave={onSave}
        onPreview={onPreview}
      />,
    )
    fireEvent.change(screen.getByLabelText(/version/i), { target: { value: "" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ target_pipeline_version: null }))
  })

  it("disables the enabled toggle for a draft trigger awaiting activation", () => {
    render(
      <RoutineScheduleEditorDialog
        schedule={baseSchedule({ activation: "draft", enabled: false })}
        onCancel={onCancel}
        onSave={onSave}
        onPreview={onPreview}
      />,
    )
    expect(screen.getByRole("switch", { name: /awaiting MANAGER activation/i })).toBeDisabled()
  })

  it("clears the wake gate when the slug field is emptied", () => {
    render(
      <RoutineScheduleEditorDialog
        schedule={baseSchedule({ wake_pipeline_slug: "is-ready", wake_fail_closed: true })}
        onCancel={onCancel}
        onSave={onSave}
        onPreview={onPreview}
      />,
    )
    fireEvent.change(screen.getByDisplayValue("is-ready"), { target: { value: "" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({ wake_pipeline_slug: "", wake_fail_closed: undefined }),
    )
  })

  it("calls onCancel without saving", () => {
    render(
      <RoutineScheduleEditorDialog schedule={baseSchedule()} onCancel={onCancel} onSave={onSave} onPreview={onPreview} />,
    )
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(onCancel).toHaveBeenCalled()
    expect(onSave).not.toHaveBeenCalled()
  })
})
