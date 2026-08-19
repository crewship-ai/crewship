// The brand picker is the one control that changes a credential's face, and
// the registry behind it is on its way past several hundred entries. That
// scale is the whole subject of this file: a picker that was fine at 166
// icon tiles becomes a wall of 8px labels at 400, so the promises worth
// pinning are the ones that survive the growth — search is the fast path, the
// list never paints more rows than a person can scan, the count never lies
// about what was held back, and the panel fits a 390px phone.
//
// The registry itself is owned elsewhere and grows without asking, so nothing
// here hard-codes a brand count; the assertions are written against
// BRAND_REGISTRY.length so they stay true as it moves.

import { describe, it, expect, afterEach, vi } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"

import { BRAND_REGISTRY } from "@/lib/credential-providers/registry"
import { BrandPicker, BRAND_RESULT_CAP } from "../brand-picker"

function open(value = "NONE") {
  const onChange = vi.fn()
  render(<BrandPicker value={value} onChange={onChange} />)
  fireEvent.click(screen.getByRole("button", { name: /^provider:/i }))
  return { onChange }
}

/** The brand rows, as opposed to the chips, the trigger and the footer. */
function rows() {
  const list = screen.queryByTestId("brand-results")
  return list ? within(list).queryAllByRole("button") : []
}

const searchBox = () => screen.getByPlaceholderText("Search brands…")

// innerWidth is global and jsdom does not reset it between files. A test that
// leaves it at 390 silently puts every later suite on a phone.
const REAL_INNER_WIDTH = window.innerWidth
afterEach(() => {
  Object.defineProperty(window, "innerWidth", {
    configurable: true,
    writable: true,
    value: REAL_INNER_WIDTH,
  })
})

describe("the trigger", () => {
  it("names the current brand, so it reads as something you can change", () => {
    render(<BrandPicker value="NONE" onChange={vi.fn()} />)
    expect(
      screen.getByRole("button", { name: /provider: generic secret\. click to change\./i }),
    ).toBeInTheDocument()
  })
})

describe("search is the primary path", () => {
  it("narrows to what was typed, matching label, key and keywords", () => {
    open()
    fireEvent.change(searchBox(), { target: { value: "notion" } })
    const labels = rows().map((b) => b.getAttribute("title"))
    expect(labels).toContain("Notion")
    expect(labels.length).toBeLessThan(10)
  })

  it("puts a prefix match ahead of a mid-word one", () => {
    open()
    fireEvent.change(searchBox(), { target: { value: "git" } })
    // "GitHub"/"GitLab" start with the query; anything that merely contains
    // it must not outrank them, or the top row is never the obvious answer.
    expect(rows()[0].getAttribute("title")).toMatch(/^git/i)
  })

  it("commits the top result on Enter, so the keyboard path never needs the mouse", () => {
    const { onChange } = open()
    fireEvent.change(searchBox(), { target: { value: "notion" } })
    fireEvent.keyDown(searchBox(), { key: "Enter" })
    expect(onChange).toHaveBeenCalledWith("NOTION")
  })

  it("offers the generic icon when nothing matches, instead of a dead end", () => {
    const { onChange } = open()
    fireEvent.change(searchBox(), { target: { value: "zzzz-no-such-brand" } })
    expect(rows()).toHaveLength(0)
    fireEvent.click(screen.getByRole("button", { name: /use generic icon/i }))
    expect(onChange).toHaveBeenCalledWith("NONE")
  })
})

describe("the list stays scannable at several hundred brands", () => {
  // Written against the registry's own length rather than a number, because
  // another surface owns that file and it is still growing.
  it("never paints more rows than the cap", () => {
    expect(BRAND_REGISTRY.length).toBeGreaterThan(BRAND_RESULT_CAP)
    open()
    expect(rows().length).toBe(BRAND_RESULT_CAP)
  })

  it("says how many it held back rather than quietly truncating", () => {
    open()
    expect(
      screen.getByText(new RegExp(`showing ${BRAND_RESULT_CAP} of ${BRAND_REGISTRY.length}`, "i")),
    ).toBeInTheDocument()
    expect(screen.getByText(/keep typing to narrow/i)).toBeInTheDocument()
  })

  it("drops the warning once the result set fits", () => {
    open()
    fireEvent.change(searchBox(), { target: { value: "notion" } })
    expect(screen.queryByText(/keep typing to narrow/i)).not.toBeInTheDocument()
  })

  it("groups an unsearched list by category, so browsing has landmarks", () => {
    open()
    // The chips filter; the headings orient. Both, not either.
    expect(screen.getByRole("heading", { name: "AI" })).toBeInTheDocument()
  })
})

describe("the category chips are still a browsing aid", () => {
  it("filters to one category and drops the now-redundant heading", () => {
    open()
    fireEvent.click(screen.getByRole("button", { name: "AI", pressed: false }))
    const titles = rows().map((b) => b.getAttribute("title"))
    const aiLabels = BRAND_REGISTRY.filter((b) => b.category === "AI").map((b) => b.label)
    expect(titles.every((t) => aiLabels.includes(t!))).toBe(true)
    expect(screen.queryByRole("heading", { name: "AI" })).not.toBeInTheDocument()
  })

  it("keeps the chip row inside the panel instead of letting 18 categories push it open", () => {
    open()
    // Eighteen chips wrap to four rows on a phone. Bounded + scrollable is
    // what keeps the search box and the results above the fold.
    expect(screen.getByTestId("brand-categories").className).toMatch(/overflow-y-auto/)
  })
})

describe("on a phone", () => {
  it("never renders wider than the viewport it opens on", () => {
    open()
    // 26rem on a laptop, but clamped to the viewport minus a gutter — a fixed
    // 420px panel is 30px wider than a 390px phone, which is a horizontal
    // scrollbar on the whole page.
    expect(screen.getByTestId("brand-panel").className).toContain("w-[min(26rem,calc(100vw-1.5rem))]")
  })

  it("gives each row a thumb-sized target and one column to itself", () => {
    open()
    expect(screen.getByTestId("brand-results").className).toContain("grid-cols-1")
    expect(screen.getByTestId("brand-results").className).toContain("sm:grid-cols-2")
    expect(rows()[0].className).toContain("min-h-9")
  })

  it("does not raise the keyboard over the results it just opened", () => {
    Object.defineProperty(window, "innerWidth", { value: 390, writable: true, configurable: true })
    open()
    expect(searchBox()).not.toHaveFocus()
  })

  it("still opens focused on a laptop, where the keyboard costs nothing", () => {
    Object.defineProperty(window, "innerWidth", { value: 1280, writable: true, configurable: true })
    open()
    expect(searchBox()).toHaveFocus()
  })
})

describe("clearing the brand", () => {
  it("resets to the generic icon from the footer", () => {
    const { onChange } = open("NOTION")
    fireEvent.click(screen.getByRole("button", { name: /^no brand$/i }))
    expect(onChange).toHaveBeenCalledWith("NONE")
  })
})
