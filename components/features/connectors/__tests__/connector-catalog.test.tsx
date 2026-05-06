// Tests for ConnectorCatalog — the catalog-first replacement for the
// 4-step "Add MCP server" wizard. Search is the primary affordance,
// tile click fires onSelect, and the bottom-of-list "Add custom MCP
// server" link fires onCustom.
//
// TDD STUB — implementation throws; these tests fail until the
// component is built.

import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { ConnectorCatalog } from "../connector-catalog"
import type { ConnectorListItem } from "../types"

function tile(over: Partial<ConnectorListItem> = {}): ConnectorListItem {
  return {
    id: "linear",
    name: "Linear",
    description: "Issue tracking",
    category: "dev_tools",
    auth_mode: "mcp_oauth",
    brand_logo: "linear",
    brand_color: "#5E6AD2",
    ...over,
  }
}

const fixtureItems: ConnectorListItem[] = [
  tile({ id: "linear", name: "Linear", category: "dev_tools" }),
  tile({ id: "github", name: "GitHub", description: "Code repos & issues", category: "dev_tools", auth_mode: "pat" }),
  tile({ id: "slack", name: "Slack", description: "Send messages", category: "communication", auth_mode: "byo_oauth" }),
  tile({ id: "everything", name: "Everything (MCP demo)", description: "Official demo MCP", category: "testing", auth_mode: "none" }),
  tile({ id: "postgres", name: "PostgreSQL", description: "Database", category: "databases", auth_mode: "conn_string" }),
]

describe("ConnectorCatalog", () => {
  it("renders one tile per item with name and description", () => {
    render(
      <ConnectorCatalog
        items={fixtureItems}
        onSelect={vi.fn()}
        onCustom={vi.fn()}
      />,
    )
    for (const it of fixtureItems) {
      expect(screen.getByText(it.name)).toBeDefined()
    }
    expect(screen.getByText("Issue tracking")).toBeDefined()
    expect(screen.getByText("Code repos & issues")).toBeDefined()
  })

  it("renders the 'Add custom MCP server' escape hatch", () => {
    render(
      <ConnectorCatalog
        items={fixtureItems}
        onSelect={vi.fn()}
        onCustom={vi.fn()}
      />,
    )
    // Match by accessible name so the test isn't sensitive to wrapping
    // tags / spans inside the link or button.
    expect(screen.getByRole("button", { name: /custom MCP server/i })).toBeDefined()
  })

  it("filters tiles by case-insensitive substring search", () => {
    render(
      <ConnectorCatalog
        items={fixtureItems}
        onSelect={vi.fn()}
        onCustom={vi.fn()}
      />,
    )
    const search = screen.getByPlaceholderText(/search/i) as HTMLInputElement
    fireEvent.change(search, { target: { value: "post" } })

    expect(screen.queryByText("PostgreSQL")).not.toBeNull()
    expect(screen.queryByText("Linear")).toBeNull()
    expect(screen.queryByText("Slack")).toBeNull()
  })

  it("matches search against description as well as name", () => {
    render(
      <ConnectorCatalog
        items={fixtureItems}
        onSelect={vi.fn()}
        onCustom={vi.fn()}
      />,
    )
    const search = screen.getByPlaceholderText(/search/i) as HTMLInputElement
    // "messages" appears only in Slack's description.
    fireEvent.change(search, { target: { value: "messages" } })
    expect(screen.queryByText("Slack")).not.toBeNull()
    expect(screen.queryByText("Linear")).toBeNull()
  })

  it("shows an empty-state when search matches nothing", () => {
    render(
      <ConnectorCatalog
        items={fixtureItems}
        onSelect={vi.fn()}
        onCustom={vi.fn()}
      />,
    )
    const search = screen.getByPlaceholderText(/search/i) as HTMLInputElement
    fireEvent.change(search, { target: { value: "zzznothing" } })

    // No tiles, but custom escape-hatch + empty-state copy must remain.
    expect(screen.queryByText("Linear")).toBeNull()
    expect(screen.queryByText(/no connectors/i)).toBeDefined()
  })

  it("fires onSelect with the clicked tile's item", () => {
    const onSelect = vi.fn()
    render(
      <ConnectorCatalog
        items={fixtureItems}
        onSelect={onSelect}
        onCustom={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByText("Linear").closest("button")!)
    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect.mock.calls[0]?.[0]).toMatchObject({ id: "linear" })
  })

  it("fires onCustom when escape hatch clicked", () => {
    const onCustom = vi.fn()
    render(
      <ConnectorCatalog
        items={fixtureItems}
        onSelect={vi.fn()}
        onCustom={onCustom}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: /custom MCP server/i }))
    expect(onCustom).toHaveBeenCalledTimes(1)
  })

  it("renders a skeleton when loading=true and no items rendered", () => {
    render(
      <ConnectorCatalog
        items={[]}
        onSelect={vi.fn()}
        onCustom={vi.fn()}
        loading
      />,
    )
    // Loading state must NOT show empty-state copy ("no connectors")
    // because we don't yet know if the catalog is empty or just slow.
    expect(screen.queryByText(/no connectors/i)).toBeNull()
  })

  it("respects initialSearch", () => {
    render(
      <ConnectorCatalog
        items={fixtureItems}
        onSelect={vi.fn()}
        onCustom={vi.fn()}
        initialSearch="git"
      />,
    )
    expect(screen.queryByText("GitHub")).not.toBeNull()
    expect(screen.queryByText("Linear")).toBeNull()
  })
})
