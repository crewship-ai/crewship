// The Providers rail. Bounded facets, printed zeroes, and every click writes
// exactly one field of the filter.

import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { ProviderLoginsSidebar } from "../provider-logins-sidebar"
import { EMPTY_LOGIN_FILTERS, UNOWNED } from "@/lib/credentials/provider-logins"

function renderRail(filters = EMPTY_LOGIN_FILTERS) {
  const onFiltersChange = vi.fn()
  render(
    <ProviderLoginsSidebar
      filters={filters}
      onFiltersChange={onFiltersChange}
      counts={{ all: 5, at_limit: 1, expiring: 1, needs_relogin: 0, unassigned: 1 }}
      providers={[
        { value: "ANTHROPIC", label: "Anthropic", count: 2 },
        { value: "OPENAI", label: "OpenAI", count: 1 },
      ]}
      modes={[
        { value: "subscription", label: "Subscription", count: 4 },
        { value: "api_key", label: "API key", count: 1 },
      ]}
      owners={[
        { value: "pavel@unify.cz", label: "pavel@unify.cz", count: 3 },
        { value: UNOWNED, label: "Workspace (no owner)", count: 2 },
      ]}
      onToggleCollapse={() => {}}
    />,
  )
  return { onFiltersChange }
}

describe("status", () => {
  it("prints the five rows, zeroes included, with their counts", () => {
    renderRail()
    for (const [label, count] of [["All", 5], ["At limit", 1], ["Expiring ≤ 30 d", 1], ["Needs re-login", 0], ["Unassigned", 1]] as const) {
      const row = screen.getByText(label).closest("button, [role=button], div")!
      expect(row).toHaveTextContent(String(count))
    }
  })

  it("is single-select", () => {
    const { onFiltersChange } = renderRail()
    fireEvent.click(screen.getByText("At limit"))
    expect(onFiltersChange).toHaveBeenCalledWith({ ...EMPTY_LOGIN_FILTERS, status: "at_limit" })
  })
})

describe("provider, mode and owner", () => {
  it("toggle values in and out of a list without touching the other facets", () => {
    const { onFiltersChange } = renderRail({ ...EMPTY_LOGIN_FILTERS, provider: ["ANTHROPIC"], search: "jana" })
    fireEvent.click(screen.getByText("OpenAI"))
    expect(onFiltersChange).toHaveBeenLastCalledWith({ ...EMPTY_LOGIN_FILTERS, provider: ["ANTHROPIC", "OPENAI"], search: "jana" })
    fireEvent.click(screen.getByText("Anthropic"))
    expect(onFiltersChange).toHaveBeenLastCalledWith({ ...EMPTY_LOGIN_FILTERS, provider: [], search: "jana" })
    fireEvent.click(screen.getByText("API key"))
    expect(onFiltersChange).toHaveBeenLastCalledWith({ ...EMPTY_LOGIN_FILTERS, provider: ["ANTHROPIC"], mode: ["api_key"], search: "jana" })
    fireEvent.click(screen.getByText("Workspace (no owner)"))
    expect(onFiltersChange).toHaveBeenLastCalledWith({ ...EMPTY_LOGIN_FILTERS, provider: ["ANTHROPIC"], owner: [UNOWNED], search: "jana" })
  })

  it("the search box writes search", () => {
    const { onFiltersChange } = renderRail()
    fireEvent.change(screen.getByLabelText(/search provider logins/i), { target: { value: "plus" } })
    expect(onFiltersChange).toHaveBeenCalledWith({ ...EMPTY_LOGIN_FILTERS, search: "plus" })
  })
})
