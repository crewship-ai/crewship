import { describe, it, expect, afterEach, vi } from "vitest"
import { render, screen, fireEvent, cleanup, within } from "@testing-library/react"

// The page reads the caller's role from the workspace rather than a picker, so
// the hook has to exist here. Every test passes initialRole explicitly, which
// takes precedence — this mock only keeps the hook from reaching the network.
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws_preview", role: "OWNER" }),
}))

import { InboxPreview } from "../inbox-preview"

afterEach(cleanup)

/** Facets live behind the Filter button now, so a facet test has to open it. */
function openFilter() {
  fireEvent.click(screen.getByRole("button", { name: /Filter/ }))
}

// The preview exists to show the 1.0 design against real component tokens, so
// these tests pin the two behaviours the wireframe actually argues for: that
// the decision affordance follows the RBAC the server enforces, and that a
// row's bucket is derived rather than hand-assigned.

describe("InboxPreview — RBAC", () => {
  it("offers the skill-proposal decision to an OWNER", () => {
    render(<InboxPreview initialRole="OWNER" initialSelectedId="ibx_skill_logparser" />)

    const decision = screen.getByTestId("decision-card")
    expect(within(decision).getByRole("button", { name: /Approve/ })).toBeEnabled()
  })

  it("withholds it from a MANAGER and names who decides", () => {
    render(<InboxPreview initialRole="MANAGER" initialSelectedId="ibx_skill_logparser" />)

    const decision = screen.getByTestId("decision-card")
    expect(within(decision).getByRole("button", { name: /Approve/ })).toBeDisabled()
    expect(within(decision).getByText(/OWNER or ADMIN decides this/i)).toBeInTheDocument()
  })

  it("keeps the waitpoint decidable by a MANAGER", () => {
    render(<InboxPreview initialRole="MANAGER" initialSelectedId="ibx_wp_promote" />)

    const decision = screen.getByTestId("decision-card")
    expect(within(decision).getByRole("button", { name: /Approve/ })).toBeEnabled()
  })

  it("hides MANAGER-targeted rows from a MEMBER", () => {
    render(<InboxPreview initialRole="MEMBER" />)

    expect(screen.queryByText("Approve step \u201cpromote\u201d in docs-publish")).not.toBeInTheDocument()
    // The chat reply is targeted at the user personally, so it survives.
    expect(screen.getByText("Atlas replied in \u201cmigration v167\u201d")).toBeInTheDocument()
  })
})

describe("InboxPreview — buckets and facets", () => {
  it("counts the decision bucket from blocking rows", () => {
    render(<InboxPreview initialRole="OWNER" />)
    openFilter()

    expect(screen.getByTestId("facet-bucket-decisions")).toHaveTextContent("4")
  })

  it("narrows the list when a bucket is picked", () => {
    render(<InboxPreview initialRole="OWNER" />)
    openFilter()

    fireEvent.click(screen.getByTestId("facet-bucket-replies"))

    expect(screen.getByText("Atlas replied in \u201cmigration v167\u201d")).toBeInTheDocument()
    expect(screen.queryByText("Approve step \u201cpromote\u201d in docs-publish")).not.toBeInTheDocument()
  })

  it("reads the archive through the same list", () => {
    render(<InboxPreview initialRole="OWNER" initialView="archived" />)

    expect(screen.getByText("casey requested GH_TOKEN for crewship-ai/docs")).toBeInTheDocument()
    // A live-only row must not leak in. (An archived waitpoint shares its
    // title with a live one, so the assertion picks a title that does not.)
    expect(screen.queryByText("Skill log-parser proposed for review")).not.toBeInTheDocument()
  })
})

describe("InboxPreview — finding a subject at scale", () => {
  it("shows only the subjects that have items, and says how many it is holding back", () => {
    render(<InboxPreview initialRole="OWNER" />)
    openFilter()

    expect(screen.getByTestId("subject-casey")).toBeInTheDocument()
    // The roster is far larger than the inbox; the picker admits that instead
    // of pretending the loaded rows are the whole workspace.
    expect(screen.getByText(/in the workspace — type to find one/i)).toBeInTheDocument()
  })

  it("finds an agent that has no items in the loaded rows", () => {
    render(<InboxPreview initialRole="OWNER" />)
    openFilter()

    // harper never appears in the fixtures, so a facet built from the rows
    // could not offer them — which is the bug this picker exists to fix.
    expect(screen.queryByTestId("subject-harper")).not.toBeInTheDocument()

    fireEvent.change(screen.getByTestId("subject-search"), { target: { value: "harp" } })

    expect(screen.getByTestId("subject-harper")).toBeInTheDocument()
  })

  it("groups matches by kind", () => {
    render(<InboxPreview initialRole="OWNER" />)
    openFilter()

    fireEvent.change(screen.getByTestId("subject-search"), { target: { value: "e" } })

    expect(screen.getByText("Agents")).toBeInTheDocument()
    expect(screen.getByText("Routines")).toBeInTheDocument()
  })

  it("says so when nothing matches", () => {
    render(<InboxPreview initialRole="OWNER" />)
    openFilter()

    fireEvent.change(screen.getByTestId("subject-search"), { target: { value: "zzzz" } })

    expect(screen.getByText(/No agent or routine matches/i)).toBeInTheDocument()
  })
})

describe("InboxPreview — selecting messages", () => {
  function enterSelectMode() {
    fireEvent.click(screen.getByRole("button", { name: /Select items/i }))
  }

  it("ticks one row at a time", () => {
    render(<InboxPreview initialRole="OWNER" />)
    enterSelectMode()

    fireEvent.click(screen.getByTestId("check-0").parentElement as HTMLElement)

    expect(screen.getByText("1 selected")).toBeInTheDocument()
  })

  it("takes a range on shift-click, in the order the rows are shown", () => {
    render(<InboxPreview initialRole="OWNER" />)
    enterSelectMode()

    fireEvent.click(screen.getByTestId("check-0").parentElement as HTMLElement)
    fireEvent.click(screen.getByTestId("check-3").parentElement as HTMLElement, { shiftKey: true })

    // 0..3 inclusive — the anchor, the shift target and everything between.
    expect(screen.getByText("4 selected")).toBeInTheDocument()
  })

  it("warns that decisions will not be closed in bulk", () => {
    render(<InboxPreview initialRole="OWNER" />)
    enterSelectMode()

    fireEvent.click(screen.getByTestId("check-0").parentElement as HTMLElement)

    expect(screen.getByText(/waiting on/i)).toBeInTheDocument()
  })

  it("drops the selection when select mode is left", () => {
    render(<InboxPreview initialRole="OWNER" />)
    enterSelectMode()
    fireEvent.click(screen.getByTestId("check-0").parentElement as HTMLElement)
    expect(screen.getByText("1 selected")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: /Done selecting/i }))

    expect(screen.queryByText("1 selected")).not.toBeInTheDocument()
  })
})

describe("InboxPreview — the filter swaps its facets with the view", () => {
  it("replaces the bucket section with archive facets when Archived is picked", () => {
    render(<InboxPreview initialRole="OWNER" />)
    openFilter()
    expect(screen.getByTestId("facet-bucket-decisions")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("tab", { name: /Archived/ }))
    openFilter()

    // Live buckets have no meaning over resolved rows, so they leave; the
    // questions the archive answers take their place.
    expect(screen.queryByTestId("facet-bucket-decisions")).not.toBeInTheDocument()
    expect(screen.getByTestId("outcome-approved")).toBeInTheDocument()
    expect(screen.getByText("Decided by")).toBeInTheDocument()
    expect(screen.getByText("Period")).toBeInTheDocument()
  })

  it("narrows the archive by outcome from the rail", () => {
    render(<InboxPreview initialRole="OWNER" initialView="archived" />)

    expect(screen.getByText("casey requested GH_TOKEN for crewship-ai/web")).toBeInTheDocument()

    openFilter()
    fireEvent.click(screen.getByTestId("outcome-approved"))

    expect(screen.getByText("casey requested GH_TOKEN for crewship-ai/docs")).toBeInTheDocument()
    expect(screen.queryByText("casey requested GH_TOKEN for crewship-ai/web")).not.toBeInTheDocument()
  })
})

describe("InboxPreview — kinds the current UI cannot render", () => {
  it("gives a tripped circuit breaker its own action", () => {
    render(<InboxPreview initialRole="OWNER" initialSelectedId="ibx_breaker_docs" />)

    expect(screen.getByRole("button", { name: /Re-enable schedule/i })).toBeInTheDocument()
  })

  it("gives a memory consolidation approve, reject and diff", () => {
    render(<InboxPreview initialRole="OWNER" initialSelectedId="ibx_memory_consol" />)

    const decision = screen.getByTestId("decision-card")
    expect(within(decision).getByRole("button", { name: /Accept/ })).toBeInTheDocument()
    expect(within(decision).getByRole("button", { name: /Diff/ })).toBeInTheDocument()
  })
})
