// VLT-12 (#3491; PRD PNL-02, vault.md Q01–Q02): the Vault's facets open
// INSIDE the secondary panel, under the toolbar's Filter button — the kit's
// COM-01 block (v67 panel() → pk-fx) — not in the floating
// SidebarFilterPopover. A facet lists at most six values and offers the rest
// through "All N…".

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { fireEvent, render, screen, within, cleanup } from "@testing-library/react"

import { VaultPanel } from "../vault-panel"
import { ProvidersPanel } from "../providers-panel"
import { EMPTY_CREDENTIAL_FILTERS, type CredentialFacetOption } from "@/lib/credentials/facets"
import { EMPTY_LOGIN_FILTERS } from "@/lib/credentials/provider-logins"

const opts = (prefix: string, n: number): CredentialFacetOption[] =>
  Array.from({ length: n }, (_, i) => ({ value: `${prefix}${i + 1}`, label: `${prefix.toUpperCase()} ${i + 1}`, count: n - i }))

function secrets(over: Partial<React.ComponentProps<typeof VaultPanel>> = {}) {
  const props: React.ComponentProps<typeof VaultPanel> = {
    filters: EMPTY_CREDENTIAL_FILTERS,
    onFiltersChange: vi.fn(),
    viewCounts: { all: 9, attention: 1, recent: 2, unused: 3, "missing-tool": 0 },
    shapes: [{ value: "API_KEY", label: "API key", count: 5 }],
    brands: [{ value: "GITHUB", label: "GitHub", count: 2 }],
    crews: opts("crew", 8),
    tiers: [{ value: "L3", label: "L3", count: 1 }],
    classes: [],
    scopes: [],
    tags: [],
    agents: [],
    crewsById: {},
    spaces: null,
    ...over,
  }
  render(<VaultPanel {...props} />)
  return props
}

beforeEach(() => {
  cleanup()
  // jsdom lacks what cmdk's All N… list calls; environment only.
  Element.prototype.scrollIntoView ??= vi.fn()
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver
})

describe("Vault panel facets inline (VLT-12)", () => {
  it("Secrets: Filter opens the facet block in the panel, not a popover", () => {
    secrets()
    const trigger = screen.getByRole("button", { name: /^Filter/ })
    expect(trigger).not.toHaveAttribute("aria-haspopup")
    expect(screen.queryByRole("group", { name: "Filter secrets" })).toBeNull()
    fireEvent.click(trigger)
    const block = screen.getByRole("group", { name: "Filter secrets" })
    expect(block).toHaveAttribute("data-slot", "facet-block")
    expect(trigger).toHaveAttribute("aria-expanded", "true")
    for (const name of ["Type", "Service", "Used by crew", "Keeper tier"]) {
      expect(within(block).getByRole("button", { name: new RegExp(`^${name}`) })).toBeInTheDocument()
    }
  })

  it("Secrets: a facet shows six values and the rest under All N…; a pick filters", () => {
    const props = secrets()
    fireEvent.click(screen.getByRole("button", { name: /^Filter/ }))
    const block = screen.getByRole("group", { name: "Filter secrets" })
    fireEvent.click(within(block).getByRole("button", { name: /^Used by crew/ }))
    const crew = within(block).getByRole("group", { name: "Used by crew" })
    expect(within(crew).getAllByRole("button", { name: /^CREW \d/ })).toHaveLength(6)
    expect(within(crew).getByText("All 8…")).toBeInTheDocument()
    fireEvent.click(within(crew).getByRole("button", { name: /^CREW 2/ }))
    expect(props.onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ crew: ["crew2"] }))
  })

  it("AI providers: Filter opens the same inline block", () => {
    const onFiltersChange = vi.fn()
    render(
      <ProvidersPanel
        filters={EMPTY_LOGIN_FILTERS}
        onFiltersChange={onFiltersChange}
        viewCounts={{ all: 2, attention: 0, unassigned: 1, mine: 1 }}
        providers={[{ value: "ANTHROPIC", label: "Claude / Anthropic", count: 2 }]}
        modes={[{ value: "api_key", label: "api_key", count: 1 }, { value: "subscription", label: "subscription", count: 1 }]}
        owners={[{ value: "u1", label: "Pavel", count: 2 }]}
        pools={[]}
        selectedPool={null}
        onOpenPool={vi.fn()}
        spaces={null}
      />,
    )
    const trigger = screen.getByRole("button", { name: /^Filter/ })
    expect(trigger).not.toHaveAttribute("aria-haspopup")
    fireEvent.click(trigger)
    const block = screen.getByRole("group", { name: "Filter accounts" })
    fireEvent.click(within(block).getByRole("button", { name: /^How it pays/ }))
    fireEvent.click(within(block).getByRole("button", { name: /^API key · money/ }))
    expect(onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ mode: ["api_key"] }))
  })
})
