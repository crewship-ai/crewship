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
    credentials?: { id: string; name: string; provider: string; type: string }[]
    onSelectCredential?: (id: string) => void
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
      credentials={overrides.credentials ?? []}
      onSelectCredential={overrides.onSelectCredential}
      onToggleCollapse={() => {}}
    />,
  )
  return { onFiltersChange }
}

/** The facets moved behind the Filter button when the rail took the
 *  /routines shape. These tests still assert the same promises — a facet
 *  carries a count, selecting it filters, re-selecting clears — they just
 *  have to open the dropdown to reach them. */
function openFilter() {
  fireEvent.click(screen.getByRole("button", { name: /filter/i }))
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
    openFilter()
    const section = screen.getByRole("button", { name: /^AI & inference 3$/ })
    expect(section).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^Source control 2$/ })).toBeInTheDocument()
  })

  it("selects a category, and clicking the selected one clears it", () => {
    const onFiltersChange = vi.fn()
    renderSidebar({ onFiltersChange, filters: { category: "AI" } })
    openFilter()
    fireEvent.click(screen.getByRole("button", { name: /ai & inference/i }))
    expect(onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ category: null }))
  })

  it("selects a crew scope by its composite value", () => {
    const { onFiltersChange } = renderSidebar()
    openFilter()
    fireEvent.click(screen.getByRole("button", { name: /crew · engineering/i }))
    expect(onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ scope: "crew:c1" }))
  })

  it("omits an empty facet section rather than showing a bare heading", () => {
    renderSidebar({ categories: [], scopes: [] })
    openFilter()
    expect(screen.queryByText("Category")).not.toBeInTheDocument()
    expect(screen.queryByText("Scope")).not.toBeInTheDocument()
  })

  it("shows the Tag section only when the workspace has tags", () => {
    renderSidebar({ tags: [] })
    openFilter()
    expect(screen.queryByText("Tag")).not.toBeInTheDocument()
  })

  it("selects a tag", () => {
    const { onFiltersChange } = renderSidebar({ tags: ["prod", "billing"] })
    openFilter()
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
    const { onFiltersChange } = renderSidebar({
      filters: { category: "AI", scope: "WORKSPACE", search: "gh", status: "attention" },
    })
    openFilter()
    fireEvent.click(screen.getByRole("button", { name: /clear filters/i }))
    // Status survives alongside the search: both were chosen somewhere other
    // than this dropdown, and clearing the dropdown's facets should not
    // silently undo a selection the rail is still showing as pressed.
    expect(onFiltersChange).toHaveBeenCalledWith({
      ...EMPTY_CREDENTIAL_FILTERS,
      search: "gh",
      status: "attention",
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
  it("counts what its own section contains — status rows, and credentials", () => {
    renderSidebar({
      credentials: [
        { id: "c1", name: "GH_TOKEN", provider: "GITHUB", type: "CLI_TOKEN" },
        { id: "c2", name: "AWS_MAIN", provider: "AWS", type: "SECRET" },
      ],
    })
    const status = screen.getByText("Status").parentElement!
    expect(within(status).getByText("3")).toBeInTheDocument()
    const creds = screen.getByText("Credentials").parentElement!
    expect(within(creds).getByText("2")).toBeInTheDocument()
  })
})

// The rail was built as a stack of facet sections — Status, then Category,
// then Scope, then Tag — which is not the house pattern. /routines puts the
// facets behind a Filter button in the toolbar and gives the body to the
// ROUTINES themselves, so the rail answers "which one?" and the filter
// answers "narrow it how?". A rail that only narrows makes the reader hunt
// for the list in the main pane, and with thirty credentials the facet stack
// is longer than the thing it filters.
describe("routines-shaped rail", () => {
  const CREDS = [
    { id: "c1", name: "GH_TOKEN", provider: "GITHUB", type: "CLI_TOKEN" },
    { id: "c2", name: "AWS_MAIN", provider: "AWS", type: "SECRET" },
    { id: "c3", name: "ANTHROPIC_API_KEY", provider: "ANTHROPIC", type: "API_KEY" },
  ]

  it("lists the credentials themselves, the way /routines lists routines", () => {
    renderSidebar({ credentials: CREDS })
    for (const c of CREDS) {
      expect(screen.getByRole("button", { name: new RegExp(c.name) })).toBeInTheDocument()
    }
  })

  it("selects a credential when its row is clicked", () => {
    const onSelect = vi.fn()
    renderSidebar({ credentials: CREDS, onSelectCredential: onSelect })
    fireEvent.click(screen.getByRole("button", { name: /AWS_MAIN/ }))
    expect(onSelect).toHaveBeenCalledWith("c2")
  })

  it("puts category, scope and tag behind the Filter button rather than in the body", () => {
    renderSidebar({ credentials: CREDS, tags: ["prod"] })
    // Closed: the facets are not on screen at all.
    expect(screen.queryByRole("button", { name: /AI & inference/ })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /filter/i }))
    expect(screen.getByRole("button", { name: /AI & inference/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Workspace/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /prod/ })).toBeInTheDocument()
  })

  it("badges the Filter button with how many facets are narrowing the list", () => {
    renderSidebar({ credentials: CREDS, filters: { category: "AI", scope: "WORKSPACE" } })
    // Status lives in its own section, so it must not inflate the badge.
    expect(screen.getByRole("button", { name: /filter/i })).toHaveTextContent("2")
  })

  it("keeps STATUS in the rail — it is the question asked most often", () => {
    renderSidebar({ credentials: CREDS })
    expect(screen.getByRole("button", { name: /^All 9$/ })).toBeInTheDocument()
  })
})
