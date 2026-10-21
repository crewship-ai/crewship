import { describe, expect, it, vi } from "vitest"
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { ShieldCheck } from "lucide-react"

// Owner, real phone 19. 10. (Inbox approval detail on candidate15):
//  - the decision block's note sat beside Approve / Not now and squeezed them;
//    Approve "behaved strangely" when pressed;
//  - a run step named "approve" wrapped letter by letter and its "wait" chip
//    stood on end, squeezed by the step's description.

const api = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api.fetch(...a) }))

import { HumanDecisionForm } from "@/components/features/approvals/human-decision-form"
import { DecisionBlock, Dock } from "@/components/features/inbox-v2/inbox-detail-frame"
import { WaitpointRunDetail } from "../waitpoint-run-detail"

const form = {
  fields: [],
  actions: [
    { id: "approve", label: "Approve", approved: true },
    { id: "hold", label: "Not now", approved: false },
  ],
}

describe("decision form", () => {
  it("puts the declining action first and the approving one last (primary on the right)", () => {
    render(<HumanDecisionForm form={form} onDecide={vi.fn()} />)
    const names = screen.getAllByRole("button").map((b) => b.textContent)
    expect(names).toEqual(["Not now", "Approve"])
  })

  it("shares the row on a phone: each button takes an equal part", () => {
    render(<HumanDecisionForm form={form} onDecide={vi.fn()} />)
    for (const b of screen.getAllByRole("button")) expect(b.className).toContain("max-md:flex-1")
  })

  it("keeps the pressed button's size while it works, and sends once", async () => {
    let resolve!: (v: boolean) => void
    const decide = vi.fn().mockImplementation(() => new Promise<boolean>((r) => { resolve = r }))
    render(<HumanDecisionForm form={form} onDecide={decide} />)
    const approve = screen.getByRole("button", { name: "Approve" })
    fireEvent.click(approve)
    fireEvent.click(approve)
    await waitFor(() => expect(approve).toHaveAttribute("aria-busy", "true"))
    expect(decide).toHaveBeenCalledTimes(1)
    // The label stays in the layout (invisible), so the width cannot change.
    const label = approve.querySelector("[data-slot=decision-label]") as HTMLElement
    expect(label).toHaveTextContent("Approve")
    expect(label.className).toContain("invisible")
    expect(approve).toBeDisabled()
    await act(async () => { resolve(true) })
    expect(approve).not.toHaveAttribute("aria-busy")
  })
})

describe("decision block on a phone", () => {
  it("puts the note on its own line above the actions, and lets the row wrap", () => {
    render(<Dock note="Buttons named by the routine author"><button type="button">Approve</button></Dock>)
    const dock = screen.getByTestId("decision-dock")
    expect(dock.className).not.toContain("max-sm:flex-nowrap")
    const note = within(dock).getByText("Buttons named by the routine author")
    expect(note.className).toContain("max-md:basis-full")
  })

  it("draws no empty body when every fact in it rendered nothing", () => {
    const Nothing = () => null
    render(<DecisionBlock icon={ShieldCheck} heading="Waiting on your decision" actions={<button type="button">Approve</button>}><Nothing /></DecisionBlock>)
    const body = screen.getByTestId("decision-block").querySelector("[data-slot=decision-body]") as HTMLElement
    expect(body.className).toContain("empty:hidden")
  })
})

describe("run progress steps", () => {
  it("keeps the step name and its kind on one line, the description below", async () => {
    api.fetch.mockImplementation(async (url: string) => {
      if (url.includes("/pipeline-runs/")) {
        return { ok: true, json: async () => ({ id: "run_1", pipeline_slug: "overdue", status: "paused", current_step_id: "approve", step_outputs: { list: "[]" } }) }
      }
      return {
        ok: true,
        json: async () => ({
          slug: "overdue",
          name: "Overdue invoice reminders",
          definition: { steps: [
            { id: "list", type: "transform" },
            { id: "approve", type: "wait", wait: { approval_prompt: "{{ steps.list.output }}. The oldest is INV-2041 from Marlow & Finch, 21 days late." } },
          ] },
        }),
      }
    })
    render(<WaitpointRunDetail workspaceId="ws" pipelineRunId="run_1" />)
    const name = await screen.findByText("approve")
    expect(name.className).toContain("whitespace-nowrap")
    expect(name.className).toContain("shrink-0")
    const kind = screen.getByText("wait")
    expect(kind.className).toContain("whitespace-nowrap")
    expect(kind.className).toContain("shrink-0")
    const description = screen.getByText(/The oldest is INV-2041/)
    expect(description).toHaveAttribute("data-slot", "step-description")
    // Not in the name's row: a line of its own.
    expect(description.parentElement).not.toBe(name.parentElement)
  })
})
