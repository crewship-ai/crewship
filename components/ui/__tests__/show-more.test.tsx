import { fireEvent, render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import { ShowMore } from "@/components/ui/show-more"

describe("ShowMore", () => {
  it("names how many are shown and offers the next step", () => {
    const onMore = vi.fn()
    render(<ShowMore shown={25} total={412} nextCount={25} onMore={onMore} noun="people" />)
    expect(screen.getByText("25 of 412 people shown")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Show 25 more" }))
    expect(onMore).toHaveBeenCalledOnce()
  })

  it("says everything is shown once a paged list is exhausted", () => {
    render(<ShowMore shown={60} total={60} nextCount={0} onMore={() => {}} paged />)
    expect(screen.getByText("All 60 shown")).toBeTruthy()
    expect(screen.queryByRole("button")).toBeNull()
  })

  it("draws nothing for a list that never needed paging", () => {
    const { container } = render(<ShowMore shown={7} total={7} nextCount={0} onMore={() => {}} />)
    expect(container.firstChild).toBeNull()
  })
})
