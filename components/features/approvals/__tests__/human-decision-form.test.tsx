import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { HumanDecisionForm } from "../human-decision-form"
import { RoutineApprovalBanner } from "@/components/features/routines/routine-approval-banner"
import type { DecisionForm } from "@/lib/decision-form"

const form: DecisionForm = {
  fields: [
    { name: "count", type: "integer", default: 0, required: true },
    { name: "enabled", type: "boolean", default: false, required: true },
    { name: "note", type: "string", required: true },
  ],
  actions: [
    { id: "ship", label: "Ship", approved: true },
    { id: "revise", label: "Revise", approved: true },
    { id: "stop", label: "Stop", approved: false },
  ],
}

describe("human decisions", () => {
  it.each(["object", "array", "integer"])(
    "N10 rejects despite malformed %s input",
    async (type) => {
      const decide = vi.fn().mockResolvedValue(true)
      render(
        <HumanDecisionForm
          form={{ ...form, fields: [{ name: "value", type }] }}
          onDecide={decide}
        />,
      )
      fireEvent.change(screen.getByLabelText(/value/i), {
        target: { value: type === "integer" ? "4.5" : "{broken" },
      })
      fireEvent.click(screen.getByRole("button", { name: "Stop" }))
      await waitFor(() =>
        expect(decide).toHaveBeenCalledWith(false, { action_id: "stop", data: {} }),
      )
    },
  )
  it("requires answers for continuing actions and preserves false and zero", async () => {
    const decide = vi.fn().mockResolvedValue(true)
    render(<HumanDecisionForm form={form} onDecide={decide} />)
    fireEvent.click(screen.getByRole("button", { name: "Ship" }))
    expect(decide).not.toHaveBeenCalled()
    expect(screen.getByRole("alert")).toHaveTextContent("Required")
    fireEvent.change(screen.getByLabelText(/note/), { target: { value: "ok" } })
    fireEvent.click(screen.getByRole("button", { name: "Revise" }))
    await waitFor(() =>
      expect(decide).toHaveBeenCalledWith(true, {
        action_id: "revise",
        data: { count: 0, enabled: false, note: "ok" },
      }),
    )
  })
  it("allows rejecting without required answers and sends just one concurrent decision", async () => {
    let resolve!: (value: boolean) => void
    const decide = vi.fn().mockImplementation(
      () =>
        new Promise<boolean>((r) => {
          resolve = r
        }),
    )
    render(<HumanDecisionForm form={form} onDecide={decide} />)
    const stop = screen.getByRole("button", { name: "Stop" })
    fireEvent.click(stop)
    fireEvent.click(stop)
    expect(decide).toHaveBeenCalledTimes(1)
    expect(decide.mock.calls[0][0]).toBe(false)
    resolve(false)
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("not accepted"))
  })
  it("the run banner exposes named actions instead of bypass buttons", async () => {
    const decide = vi.fn().mockResolvedValue(true)
    render(
      <RoutineApprovalBanner
        waitpoint={{
          token: "t",
          pipeline_run_id: "r",
          step_id: "gate",
          kind: "approval",
          prompt: "Review",
          timeout_at: "2099-01-01T00:00:00Z",
          created_at: "2026-01-01T00:00:00Z",
          decision_form: form,
        }}
        deciding={false}
        onDecide={decide}
      />,
    )
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Stop" }))
    await waitFor(() =>
      expect(decide).toHaveBeenCalledWith(
        false,
        "",
        expect.objectContaining({ action_id: "stop" }),
      ),
    )
  })
})
