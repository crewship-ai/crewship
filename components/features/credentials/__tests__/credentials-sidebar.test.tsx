// The credentials left rail mirrors the Integrations explorer, which is the
// house pattern. What these tests protect is not the layout but the promises
// the rail makes: every row carries a count, clicking a row selects exactly
// that facet, clicking it again clears it, and a facet with nothing behind it
// is not offered at all (a zero row is a dead end that looks like a filter).

import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"
import { EMPTY_CREDENTIAL_FILTERS, type CredentialFilters } from "@/lib/credentials/facets"
import { CredentialsSidebar } from "../credentials-sidebar"

function renderSidebar(
  overrides: {
    filters?: Partial<CredentialFilters>
    onFiltersChange?: (f: CredentialFilters) => void
    counts?: { all: number; attention: number; missingTool: number }
    categories?: { value: string; label: string; count: number }[]
    scopes?: { value: string; label: string; count: number }[]
    tags?: string[]
  } = {},
) {
  const onFiltersChange = overrides.onFiltersChange ?? vi.fn()
  render(
    <CredentialsSidebar
      filters={{ ...EMPTY_CREDENTIAL_FILTERS, ...overrides.filters }}
      onFiltersChange={onFiltersChange}
      counts={overrides.counts ?? { all: 9, attention: 2, missingTool: 1 }}
      categories={
        overrides.categories ?? [
          { value: "AI", label: "AI & inference", count: 3 },
          { value: "Source", label: "Source control", count: 2 },
        ]
      }
      scopes={
        overrides.scopes ?? [
          { value: "WORKSPACE", label: "Workspace", count: 4 },
          { value: "crew:c1", label: "Crew · engineering", count: 3 },
        ]
      }
      tags={overrides.tags ?? []}
      onToggleCollapse={() => {}}
    />,
  )
  return { onFiltersChange }
}

describe("status rows", () => {
  it("offers All, Needs attention and Missing tool with their counts", () => {
    renderSidebar()
    expect(screen.getByRole("button", { name: /^All 9$/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^Needs attention 2$/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^Missing tool 1$/ })).toBeInTheDocument()
  })

  it("selects the attention filter when its row is clicked", () => {
    const { onFiltersChange } = renderSidebar()
    fireEvent.click(screen.getByRole("button", { name: /needs attention/i }))
    expect(onFiltersChange).toHaveBeenCalledWith(
      expect.objectContaining({ status: "attention" }),
    )
  })

  it("marks the active status row as pressed so the rail says what the list is showing", () => {
    renderSidebar({ filters: { status: "attention" } })
    expect(screen.getByRole("button", { name: /needs attention/i })).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByRole("button", { name: /^All 9$/ })).toHaveAttribute("aria-pressed", "false")
  })

  // Readiness is aggregated per crew; a workspace with no gaps has nothing to
  // filter by, and a "Missing tool 0" row is a control that does nothing.
  it("hides Missing tool when nothing is missing", () => {
    renderSidebar({ counts: { all: 9, attention: 0, missingTool: 0 } })
    expect(screen.queryByRole("button", { name: /missing tool/i })).not.toBeInTheDocument()
  })

  it("hides Needs attention when everything is healthy", () => {
    renderSidebar({ counts: { all: 9, attention: 0, missingTool: 0 } })
    expect(screen.queryByRole("button", { name: /needs attention/i })).not.toBeInTheDocument()
  })
})

describe("category and scope facets", () => {
  it("lists the categories the workspace actually uses, with counts", () => {
    renderSidebar()
    const section = screen.getByRole("button", { name: /^AI & inference 3$/ })
    expect(section).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^Source control 2$/ })).toBeInTheDocument()
  })

  it("selects a category, and clicking the selected one clears it", () => {
    const onFiltersChange = vi.fn()
    renderSidebar({ onFiltersChange, filters: { category: "AI" } })
    fireEvent.click(screen.getByRole("button", { name: /ai & inference/i }))
    expect(onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ category: null }))
  })

  it("selects a crew scope by its composite value", () => {
    const { onFiltersChange } = renderSidebar()
    fireEvent.click(screen.getByRole("button", { name: /crew · engineering/i }))
    expect(onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ scope: "crew:c1" }))
  })

  it("omits an empty facet section rather than showing a bare heading", () => {
    renderSidebar({ categories: [], scopes: [] })
    expect(screen.queryByText("Category")).not.toBeInTheDocument()
    expect(screen.queryByText("Scope")).not.toBeInTheDocument()
  })

  it("shows the Tag section only when the workspace has tags", () => {
    renderSidebar({ tags: [] })
    expect(screen.queryByText("Tag")).not.toBeInTheDocument()
  })

  it("selects a tag", () => {
    const { onFiltersChange } = renderSidebar({ tags: ["prod", "billing"] })
    fireEvent.click(screen.getByRole("button", { name: /^prod$/ }))
    expect(onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ tag: "prod" }))
  })
})

describe("search", () => {
  it("passes the typed query up as part of the filter object", () => {
    const { onFiltersChange } = renderSidebar()
    fireEvent.change(screen.getByPlaceholderText(/search a secret or tool/i), {
      target: { value: "github" },
    })
    expect(onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ search: "github" }))
  })
})

describe("clearing", () => {
  it("offers Clear filters only while something is filtered, and resets everything but the search", () => {
    const { onFiltersChange } = renderSidebar({ filters: { category: "AI", scope: "WORKSPACE", search: "gh" } })
    const clear = screen.getByRole("button", { name: /clear filters/i })
    fireEvent.click(clear)
    expect(onFiltersChange).toHaveBeenCalledWith({
      ...EMPTY_CREDENTIAL_FILTERS,
      search: "gh",
    })
  })

  it("does not offer Clear filters when nothing is filtered", () => {
    renderSidebar()
    expect(screen.queryByRole("button", { name: /clear filters/i })).not.toBeInTheDocument()
  })
})

describe("collapse", () => {
  it("hands the collapse toggle to the caller", () => {
    const onToggleCollapse = vi.fn()
    render(
      <CredentialsSidebar
        filters={EMPTY_CREDENTIAL_FILTERS}
        onFiltersChange={() => {}}
        counts={{ all: 1, attention: 0, missingTool: 0 }}
        categories={[]}
        scopes={[]}
        tags={[]}
        onToggleCollapse={onToggleCollapse}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: /collapse sidebar/i }))
    expect(onToggleCollapse).toHaveBeenCalled()
  })
})

describe("section headings carry their own totals", () => {
  it("counts the facet rows, not the credentials, on the heading", () => {
    renderSidebar()
    const heading = screen.getByText("Category").parentElement!
    expect(within(heading).getByText("2")).toBeInTheDocument()
  })
})
