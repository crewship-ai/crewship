/**
 * §9: `needs_reconciliation` is neither success nor failure and must not be
 * rendered as either. It means a runtime MAY still be alive under a locator
 * nobody has verified, it is still holding capacity, and a person has to look.
 *
 * The assertion below is deliberately about what a READER can tell apart, not
 * about a class string: three states, three different renderings, and the
 * unresolved one carrying a paragraph that the other two do not get.
 */
import { describe, it, expect, afterEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import { NeedsReconciliationNotice, WorkStatePill } from "../work-state-pill"
import { WORK_STATES } from "@/hooks/use-work-items"

afterEach(cleanup)

function outcomeOf(state: Parameters<typeof WorkStatePill>[0]["state"]): string {
  const { container } = render(<WorkStatePill state={state} />)
  const el = container.querySelector("[data-work-outcome]")
  return el?.getAttribute("data-work-outcome") ?? ""
}

describe("WorkStatePill", () => {
  it("renders needs_reconciliation as neither succeeded nor failed", () => {
    const unresolved = outcomeOf("needs_reconciliation")
    cleanup()
    const succeeded = outcomeOf("succeeded")
    cleanup()
    const failed = outcomeOf("failed")

    expect(unresolved).toBe("unresolved")
    expect(unresolved).not.toBe(succeeded)
    expect(unresolved).not.toBe(failed)
  })

  it("does not call it a result — the word is 'Needs reconciliation'", () => {
    render(<WorkStatePill state="needs_reconciliation" />)
    expect(screen.getByText("Needs reconciliation")).toBeTruthy()
  })

  it("gives every state a label and a meaning, so no raw column value reaches a reader", () => {
    for (const state of WORK_STATES) {
      cleanup()
      const { container } = render(<WorkStatePill state={state} />)
      const el = container.querySelector("[data-work-state]")
      expect(el?.getAttribute("data-work-state")).toBe(state)
      expect(el?.getAttribute("title")).toBeTruthy()
      expect(el?.textContent?.trim()).not.toBe(state)
    }
  })
})

describe("NeedsReconciliationNotice", () => {
  it("says a runtime may still be alive, and names the locator to look at", () => {
    render(<NeedsReconciliationNotice state="needs_reconciliation" runtimeLocator="container://abc" />)
    const notice = screen.getByTestId("needs-reconciliation-notice")
    expect(notice.textContent).toContain("this is not a result")
    expect(notice.textContent).toContain("may still be alive")
    expect(notice.textContent).toContain("container://abc")
  })

  it("appears for no other state — including the two it is most often mistaken for", () => {
    for (const state of ["succeeded", "failed", "cancelled", "running"] as const) {
      cleanup()
      render(<NeedsReconciliationNotice state={state} />)
      expect(screen.queryByTestId("needs-reconciliation-notice")).toBeNull()
    }
  })
})
