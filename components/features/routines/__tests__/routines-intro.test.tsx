import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, within, cleanup, fireEvent } from "@testing-library/react"

import { RoutinesIntro } from "../routines-intro"

afterEach(cleanup)

// ODCH-RT-08: "In this workspace this week" comes from real runs, and only
// when there are some; the animated demo and sample figures stay out.
describe("<RoutinesIntro> workspace proof", () => {
  it("shows the week's real figures with a way to the overview", () => {
    const onOverview = vi.fn()
    render(<RoutinesIntro proof={{ runs: 58, finishedPct: 93, spendUsd: 32.7, month: "October" }} onOverview={onOverview} />)
    const proof = screen.getByRole("region", { name: "In this workspace this week" })
    expect(proof).toHaveTextContent("58runs")
    expect(proof).toHaveTextContent("93 %finished without help")
    expect(proof).toHaveTextContent("$32.70spent in October")
    fireEvent.click(within(proof).getByRole("button", { name: /Open the overview/ }))
    expect(onOverview).toHaveBeenCalled()
  })

  it("leaves out the finished rate when nothing finished, and the section when nothing ran", () => {
    const { rerender } = render(<RoutinesIntro proof={{ runs: 2, finishedPct: null, spendUsd: null, month: "October" }} />)
    const proof = screen.getByRole("region", { name: "In this workspace this week" })
    expect(proof).toHaveTextContent("2runs")
    expect(proof).not.toHaveTextContent("finished without help")
    expect(proof).not.toHaveTextContent("spent in")
    rerender(<RoutinesIntro proof={{ runs: 0, finishedPct: null, spendUsd: null, month: "October" }} />)
    expect(screen.queryByRole("region", { name: "In this workspace this week" })).toBeNull()
    rerender(<RoutinesIntro />)
    expect(screen.queryByRole("region", { name: "In this workspace this week" })).toBeNull()
  })
})
