import { describe, it, expect, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, within } from "@testing-library/react"

import { InboxPreview } from "../inbox-preview"

afterEach(cleanup)

// The preview exists to show the 1.0 design against real component tokens, so
// these tests pin the two behaviours the wireframe actually argues for: that
// the decision affordance follows the RBAC the server enforces, and that a
// row's bucket is derived rather than hand-assigned.

describe("InboxPreview — RBAC", () => {
  it("offers the skill-proposal decision to an OWNER", () => {
    render(<InboxPreview initialRole="OWNER" initialSelectedId="ibx_skill_logparser" />)

    const decision = screen.getByTestId("decision-card")
    expect(within(decision).getByRole("button", { name: /Schválit/ })).toBeEnabled()
  })

  it("withholds it from a MANAGER and names who decides", () => {
    render(<InboxPreview initialRole="MANAGER" initialSelectedId="ibx_skill_logparser" />)

    const decision = screen.getByTestId("decision-card")
    expect(within(decision).getByRole("button", { name: /Schválit/ })).toBeDisabled()
    expect(within(decision).getByText(/rozhodne OWNER nebo ADMIN/i)).toBeInTheDocument()
  })

  it("keeps the waitpoint decidable by a MANAGER", () => {
    render(<InboxPreview initialRole="MANAGER" initialSelectedId="ibx_wp_promote" />)

    const decision = screen.getByTestId("decision-card")
    expect(within(decision).getByRole("button", { name: /Schválit/ })).toBeEnabled()
  })

  it("hides MANAGER-targeted rows from a MEMBER", () => {
    render(<InboxPreview initialRole="MEMBER" />)

    expect(screen.queryByText("Schválit krok „promote“ v docs-publish")).not.toBeInTheDocument()
    // The chat reply is targeted at the user personally, so it survives.
    expect(screen.getByText("Atlas odpověděl v „migrace v167“")).toBeInTheDocument()
  })
})

describe("InboxPreview — buckets and facets", () => {
  it("counts the decision bucket from blocking rows", () => {
    render(<InboxPreview initialRole="OWNER" />)

    const chip = screen.getByTestId("facet-bucket-decisions")
    expect(chip).toHaveTextContent("4")
  })

  it("narrows the list when a bucket is picked", () => {
    render(<InboxPreview initialRole="OWNER" />)

    fireEvent.click(screen.getByTestId("facet-bucket-replies"))

    expect(screen.getByText("Atlas odpověděl v „migrace v167“")).toBeInTheDocument()
    expect(screen.queryByText("Schválit krok „promote“ v docs-publish")).not.toBeInTheDocument()
  })

  it("shows the resolved outcome and actor in the archive", () => {
    render(<InboxPreview initialRole="OWNER" initialView="archived" />)

    const row = screen.getByText("casey žádá GH_TOKEN pro crewship-ai/docs").closest("[data-row]")
    expect(row).not.toBeNull()
    expect(within(row as HTMLElement).getByText(/schváleno/i)).toBeInTheDocument()
    expect(within(row as HTMLElement).getByText("pavel")).toBeInTheDocument()
  })
})

describe("InboxPreview — the rail swaps its facets with the view", () => {
  it("replaces the bucket section with archive facets when Archiv is picked", () => {
    render(<InboxPreview initialRole="OWNER" />)

    expect(screen.getByTestId("facet-bucket-decisions")).toBeInTheDocument()

    fireEvent.click(screen.getByTestId("view-archived"))

    // Live buckets have no meaning over resolved rows, so they leave; the
    // questions the archive answers take their place.
    expect(screen.queryByTestId("facet-bucket-decisions")).not.toBeInTheDocument()
    expect(screen.getByTestId("outcome-approved")).toBeInTheDocument()
    expect(screen.getByText("Kdo rozhodl")).toBeInTheDocument()
    expect(screen.getByText("Období")).toBeInTheDocument()
  })

  it("narrows the archive by outcome from the rail", () => {
    render(<InboxPreview initialRole="OWNER" initialView="archived" />)

    expect(screen.getByText("casey žádá GH_TOKEN pro crewship-ai/web")).toBeInTheDocument()

    fireEvent.click(screen.getByTestId("outcome-approved"))

    expect(screen.getByText("casey žádá GH_TOKEN pro crewship-ai/docs")).toBeInTheDocument()
    expect(screen.queryByText("casey žádá GH_TOKEN pro crewship-ai/web")).not.toBeInTheDocument()
  })
})

describe("InboxPreview — kinds the current UI cannot render", () => {
  it("gives a tripped circuit breaker its own action", () => {
    render(<InboxPreview initialRole="OWNER" initialSelectedId="ibx_breaker_docs" />)

    expect(screen.getByRole("button", { name: /Zapnout rozvrh/i })).toBeInTheDocument()
  })

  it("gives a memory consolidation approve, reject and diff", () => {
    render(<InboxPreview initialRole="OWNER" initialSelectedId="ibx_memory_consol" />)

    const decision = screen.getByTestId("decision-card")
    expect(within(decision).getByRole("button", { name: /Přijmout/ })).toBeInTheDocument()
    expect(within(decision).getByRole("button", { name: /Diff/ })).toBeInTheDocument()
  })
})
