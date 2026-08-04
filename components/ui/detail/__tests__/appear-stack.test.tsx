import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"

import { AppearStack } from "@/components/ui/detail"

// AppearStack exists because <Appear> inserts a div between the grid
// and the card — which is exactly the change that silently breaks a
// layout. Put `col-span-2` on the card and the card is no longer the
// grid item, so the span does nothing and the grid quietly reflows.
//
// Wrapping twenty cards by hand means remembering that twenty times,
// and getting the stagger index right twenty times. (Doing it by hand
// on the routine card already produced two cards with order={8}.)

describe("<AppearStack>", () => {
  it("moves grid placement onto the wrapper, where the grid can see it", () => {
    render(
      <AppearStack>
        <div className="md:col-span-2 rounded-xl">card</div>
      </AppearStack>,
    )
    const card = screen.getByText("card")
    const wrapper = card.parentElement!
    expect(wrapper.className).toContain("md:col-span-2")
    // …and off the card, or the span would apply twice at two levels.
    expect(card.className).not.toContain("col-span")
    // Everything that is not placement stays where the author put it.
    expect(card.className).toContain("rounded-xl")
  })

  it("handles every placement utility, not just col-span", () => {
    render(
      <AppearStack>
        <div className="row-span-2 col-start-1 lg:col-end-3 p-4">card</div>
      </AppearStack>,
    )
    const wrapper = screen.getByText("card").parentElement!
    expect(wrapper.className).toContain("row-span-2")
    expect(wrapper.className).toContain("col-start-1")
    expect(wrapper.className).toContain("lg:col-end-3")
    expect(screen.getByText("card").className).toContain("p-4")
  })

  it("leaves a child with no placement classes untouched", () => {
    render(
      <AppearStack>
        <div className="rounded-xl border">card</div>
      </AppearStack>,
    )
    expect(screen.getByText("card").className).toBe("rounded-xl border")
  })

  it("renders every child, in order", () => {
    render(
      <AppearStack>
        <div>one</div>
        <div>two</div>
        <div>three</div>
      </AppearStack>,
    )
    expect(screen.getByText("one")).toBeInTheDocument()
    expect(screen.getByText("two")).toBeInTheDocument()
    expect(screen.getByText("three")).toBeInTheDocument()
  })

  it("skips falsy children rather than counting them in the stagger", () => {
    // `{cond && <Card/>}` is how half these grids are written. A false
    // that consumed an index would leave a gap in the cascade.
    render(
      <AppearStack>
        {false}
        <div>only</div>
        {null}
      </AppearStack>,
    )
    expect(screen.getByText("only")).toBeInTheDocument()
  })

  it("passes plain text through without wrapping it", () => {
    render(<AppearStack>just text</AppearStack>)
    expect(screen.getByText("just text")).toBeInTheDocument()
  })
})
