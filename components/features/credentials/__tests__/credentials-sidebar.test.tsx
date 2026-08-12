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
    brands?: { value: string; label: string; count: number; providers?: string[] }[]
    shapes?: { value: string; label: string; count: number }[]
    scopes?: { value: string; label: string; count: number }[]
    tiers?: { value: string; label: string; count: number }[]
    agents?: { value: string; label: string; count: number }[]
    crewsById?: Record<string, { id: string; name: string; icon: string | null; color: string | null }>
    tags?: string[]
    credentials?: {
      id: string
      name: string
      provider: string
      type: string
      tier?: 1 | 2 | 3 | 4 | null
    }[]
    onSelectCredential?: (id: string) => void
  } = {},
) {
  const onFiltersChange = overrides.onFiltersChange ?? vi.fn()
  render(
    <CredentialsSidebar
      filters={{ ...EMPTY_CREDENTIAL_FILTERS, ...overrides.filters }}
      onFiltersChange={onFiltersChange}
      counts={overrides.counts ?? { all: 9, attention: 2, missingTool: 1 }}
      brands={
        overrides.brands ?? [
          { value: "ANTHROPIC", label: "Anthropic", count: 3, providers: ["ANTHROPIC"] },
          { value: "GITHUB", label: "GitHub", count: 2, providers: ["GITHUB"] },
        ]
      }
      shapes={overrides.shapes}
      scopes={
        overrides.scopes ?? [
          { value: "WORKSPACE", label: "Workspace", count: 4 },
          { value: "crew:c1", label: "Crew · engineering", count: 3 },
        ]
      }
      tiers={overrides.tiers}
      agents={overrides.agents}
      crewsById={overrides.crewsById}
      tags={(overrides.tags ?? []).map((t) => ({ value: t, label: t, count: 1 }))}
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

// Brand replaced Category. The category was derived from the provider through
// the registry and never chosen by anyone; the brand IS what the picker sets
// and what the icon on every row already shows.
describe("brand, shape and scope facets", () => {
  it("lists the brands the workspace actually uses, with counts", () => {
    renderSidebar()
    openFilter()
    expect(screen.getByRole("button", { name: /^Anthropic 3$/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^GitHub 2$/ })).toBeInTheDocument()
  })

  it("selects a brand, and clicking the selected one clears it", () => {
    const onFiltersChange = vi.fn()
    renderSidebar({ onFiltersChange, filters: { brand: "ANTHROPIC" } })
    openFilter()
    fireEvent.click(screen.getByRole("button", { name: /anthropic/i }))
    expect(onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ brand: null }))
  })

  // The wizard's first question, and until now the only answer it collected
  // that the rail could not filter on.
  it("offers the shapes in use, and selects one", () => {
    const { onFiltersChange } = renderSidebar({
      shapes: [{ value: "CERTIFICATE", label: "cert", count: 2 }],
    })
    openFilter()
    fireEvent.click(screen.getByRole("button", { name: /^cert 2$/ }))
    expect(onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ shape: "CERTIFICATE" }))
  })

  it("selects a crew scope by its composite value", () => {
    const { onFiltersChange } = renderSidebar()
    openFilter()
    fireEvent.click(screen.getByRole("button", { name: /crew · engineering/i }))
    expect(onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ scope: "crew:c1" }))
  })

  it("omits an empty facet section rather than showing a bare heading", () => {
    renderSidebar({ brands: [], scopes: [] })
    openFilter()
    expect(screen.queryByText("Brand")).not.toBeInTheDocument()
    expect(screen.queryByText("Shape")).not.toBeInTheDocument()
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
    fireEvent.click(screen.getByRole("button", { name: /^prod 1$/ }))
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
      filters: { brand: "ANTHROPIC", scope: "WORKSPACE", search: "gh", status: "attention" },
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
        brands={[]}
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

  it("puts brand, scope and tag behind the Filter button rather than in the body", () => {
    renderSidebar({ credentials: CREDS, tags: ["prod"] })
    // Closed: the facets are not on screen at all.
    expect(screen.queryByRole("button", { name: /Anthropic/ })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /filter/i }))
    expect(screen.getByRole("button", { name: /Anthropic/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Workspace/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^prod 1$/ })).toBeInTheDocument()
  })

  it("badges the Filter button with how many facets are narrowing the list", () => {
    renderSidebar({ credentials: CREDS, filters: { brand: "ANTHROPIC", scope: "WORKSPACE" } })
    // Status lives in its own section, so it must not inflate the badge.
    expect(screen.getByRole("button", { name: /filter/i })).toHaveTextContent("2")
  })

  it("keeps STATUS in the rail — it is the question asked most often", () => {
    renderSidebar({ credentials: CREDS })
    expect(screen.getByRole("button", { name: /^All 9$/ })).toBeInTheDocument()
  })
})

// The Keeper tier decides what happens when an agent asks for a secret — at L4
// every read stops for a human — and it appeared on no listing surface at all.
// The rail is where an operator scans, so it is where the tier has to be
// visible AND selectable.
describe("tier facet", () => {
  const CREDS = [
    { id: "c1", name: "GH_TOKEN", provider: "GITHUB", type: "CLI_TOKEN" },
    { id: "c2", name: "AWS_MAIN", provider: "AWS", type: "SECRET" },
  ]
  const TIERS = [
    { value: "1", label: "L1 · low", count: 7 },
    { value: "2", label: "L2 · medium", count: 2 },
    { value: "3", label: "L3 · high", count: 0 },
    { value: "4", label: "L4 · critical", count: 0 },
  ]

  it("is not rendered at all when the caller passes no tiers", () => {
    renderSidebar({ credentials: CREDS })
    expect(screen.queryByText("Tier")).not.toBeInTheDocument()
  })

  it("lists every tier in the rail, not behind the Filter button", () => {
    renderSidebar({ credentials: CREDS, tiers: TIERS })
    expect(screen.getByText("Tier")).toBeInTheDocument()
    for (const t of TIERS) {
      expect(screen.getByRole("button", { name: new RegExp(t.label) })).toBeInTheDocument()
    }
  })

  // The one section that prints zeroes. "L4 · 0" answers "does anything here
  // stop for a human?"; hiding the row leaves the reader unable to tell "none"
  // apart from "the console does not track that".
  it("keeps an empty tier row rather than omitting it the way the other facets do", () => {
    renderSidebar({ credentials: CREDS, tiers: TIERS })
    const l4 = screen.getByRole("button", { name: /L4 · critical/ })
    expect(l4).toHaveTextContent("0")
  })

  it("selects a tier, and clicking the selected one clears it", () => {
    const { onFiltersChange } = renderSidebar({ credentials: CREDS, tiers: TIERS })
    fireEvent.click(screen.getByRole("button", { name: /L2 · medium/ }))
    expect(onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ tier: "2" }))

    const onSecond = vi.fn()
    renderSidebar({ credentials: CREDS, tiers: TIERS, filters: { tier: "2" }, onFiltersChange: onSecond })
    fireEvent.click(screen.getAllByRole("button", { name: /L2 · medium/ })[1])
    expect(onSecond).toHaveBeenCalledWith(expect.objectContaining({ tier: null }))
  })

  it("offers Any tier as the way back to the whole vault", () => {
    const { onFiltersChange } = renderSidebar({
      credentials: CREDS,
      tiers: TIERS,
      filters: { tier: "4" },
    })
    fireEvent.click(screen.getByRole("button", { name: /any tier/i }))
    expect(onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ tier: null }))
  })

  it("marks the selected tier as pressed so the rail says what the list is showing", () => {
    renderSidebar({ credentials: CREDS, tiers: TIERS, filters: { tier: "1" } })
    expect(screen.getByRole("button", { name: /L1 · low/ })).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByRole("button", { name: /any tier/i })).toHaveAttribute("aria-pressed", "false")
  })

  // Tier lives in the rail, like status — so clearing the dropdown's facets
  // must not silently undo a selection the rail is still showing as pressed.
  it("survives Clear filters, the way status does", () => {
    const { onFiltersChange } = renderSidebar({
      credentials: CREDS,
      tiers: TIERS,
      filters: { tier: "4", brand: "ANTHROPIC" },
    })
    openFilter()
    fireEvent.click(screen.getByRole("button", { name: /clear filters/i }))
    expect(onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ tier: "4", brand: null }))
  })

  it("does not inflate the Filter button badge — it is already on screen", () => {
    renderSidebar({ credentials: CREDS, tiers: TIERS, filters: { tier: "4" } })
    expect(screen.getByRole("button", { name: /filter/i })).not.toHaveTextContent("1")
  })
})

describe("tier on the credential rows", () => {
  it("puts the tier beside the name, so the rail says how guarded each secret is", () => {
    renderSidebar({
      credentials: [
        { id: "c1", name: "GH_TOKEN", provider: "GITHUB", type: "CLI_TOKEN", tier: 2 },
        { id: "c2", name: "PROD_DB", provider: "NONE", type: "SECRET", tier: 4 },
      ],
    })
    expect(screen.getByRole("button", { name: /GH_TOKEN L2/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /PROD_DB L4/ })).toBeInTheDocument()
  })

  // L1 is the default and the majority, and it still gets a chip: silence would
  // read as "no tier", which is a different claim about the row.
  it("badges L1 too, rather than leaving the commonest tier unlabelled", () => {
    renderSidebar({
      credentials: [{ id: "c1", name: "NPM_READ", provider: "NONE", type: "CLI_TOKEN", tier: 1 }],
    })
    expect(screen.getByRole("button", { name: /NPM_READ L1/ })).toBeInTheDocument()
  })

  it("says L? — never L1 — for a credential whose server sent no tier", () => {
    renderSidebar({
      credentials: [{ id: "c1", name: "LEGACY", provider: "NONE", type: "SECRET", tier: null }],
    })
    const row = screen.getByRole("button", { name: /LEGACY/ })
    expect(row).toHaveTextContent("L?")
    expect(row).not.toHaveTextContent("L1")
  })

  it("omits the chip entirely when the caller does not supply a tier field", () => {
    renderSidebar({
      credentials: [{ id: "c1", name: "VAULTKEY", provider: "NONE", type: "SECRET" }],
    })
    // The row is the name and nothing else — no chip, and in particular no "L?"
    // claiming the server was asked and stayed silent.
    expect(screen.getByRole("button", { name: /VAULTKEY/ }).textContent).toBe("VAULTKEY")
  })
})

// Every group in the Filter dropdown used to repeat one lucide glyph down its
// rows, which is the same as no icon at all: rows that look identical are rows
// you have to read. Each group now draws its rows with the thing they are
// about, and the tag group finally carries the counts it always hid.
describe("filter rows carry their own marks", () => {
  const CREDS = [{ id: "c1", name: "GH_TOKEN", provider: "GITHUB", type: "CLI_TOKEN" }]

  it("draws a crew scope row with the crew's own tile, not a generic glyph", () => {
    renderSidebar({
      credentials: CREDS,
      scopes: [
        { value: "WORKSPACE", label: "Workspace", count: 4 },
        { value: "crew:c1", label: "Crew · engineering", count: 3 },
      ],
      crewsById: {
        c1: { id: "c1", name: "engineering", icon: "rocket", color: "#ff0000" },
      },
    })
    openFilter()
    const row = screen.getByRole("button", { name: /crew · engineering/i })
    // The crew tile paints its own colour; the fallback stack glyph does not.
    expect(row.querySelector('[style*="linear-gradient"]')).not.toBeNull()
  })

  it("still renders a crew row we hold no record of, rather than hiding its credentials", () => {
    renderSidebar({
      credentials: CREDS,
      scopes: [{ value: "crew:unknown", label: "Crew · unknown", count: 1 }],
      crewsById: {},
    })
    openFilter()
    expect(screen.getByRole("button", { name: /crew · unknown/i })).toBeInTheDocument()
  })

  it("shows the tag counts it used to hide", () => {
    renderSidebar({ credentials: CREDS, tags: ["prod", "infra"] })
    openFilter()
    expect(screen.getByRole("button", { name: /^prod 1$/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^infra 1$/ })).toBeInTheDocument()
  })
})

// An "assigned to" facet keyed by agent id — the id is the point, because it is
// what the avatar is keyed by, and deriving one from a name would render a
// different face for the same agent than every other page shows.
describe("the agent facet", () => {
  const CREDS = [{ id: "c1", name: "GH_TOKEN", provider: "GITHUB", type: "CLI_TOKEN" }]
  const AGENTS = [
    { value: "ag_1", label: "Deploy bot", count: 3 },
    { value: "ag_2", label: "Reviewer", count: 1 },
  ]

  it("is absent when nothing in the vault is assigned to an agent", () => {
    renderSidebar({ credentials: CREDS })
    openFilter()
    expect(screen.queryByText("Assigned to")).not.toBeInTheDocument()
  })

  it("lists each agent with its count", () => {
    renderSidebar({ credentials: CREDS, agents: AGENTS })
    openFilter()
    expect(screen.getByText("Assigned to")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^Deploy bot 3$/ })).toBeInTheDocument()
  })

  it("draws each agent's own avatar, keyed by id", () => {
    renderSidebar({ credentials: CREDS, agents: AGENTS })
    openFilter()
    const row = screen.getByRole("button", { name: /Deploy bot/ })
    // The avatar is derived from the id, which is why the facet is keyed by id
    // at all — a face derived from the name would differ from every other page.
    expect(row.querySelector('[data-agent-id="ag_1"]')).not.toBeNull()
  })

  it("selects the agent", () => {
    const { onFiltersChange } = renderSidebar({ credentials: CREDS, agents: AGENTS })
    openFilter()
    fireEvent.click(screen.getByRole("button", { name: /Deploy bot/ }))
    expect(onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ agentId: "ag_1" }))
  })

  it("clears the agent when the selected one is clicked again", () => {
    const { onFiltersChange } = renderSidebar({
      credentials: CREDS,
      agents: AGENTS,
      filters: { agentId: "ag_1" },
    })
    openFilter()
    fireEvent.click(screen.getByRole("button", { name: /Deploy bot/ }))
    expect(onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ agentId: null }))
  })

  it("counts toward the Filter button badge, since it is not visible in the rail", () => {
    renderSidebar({ credentials: CREDS, agents: AGENTS, filters: { agentId: "ag_1" } })
    expect(screen.getByRole("button", { name: /filter/i })).toHaveTextContent("1")
  })
})

// The sort control came here with the list. Without it the rail could show
// forty credentials in exactly one order.
describe("sorting the rail", () => {
  const CREDS = [{ id: "c1", name: "GH_TOKEN", provider: "GITHUB", type: "CLI_TOKEN" }]

  it("is absent when the caller does not own the order", () => {
    renderSidebar({ credentials: CREDS })
    expect(screen.queryByRole("button", { name: /sort credentials/i })).not.toBeInTheDocument()
  })

  it("names the current order and switches to another", () => {
    const onSortChange = vi.fn()
    render(
      <CredentialsSidebar
        filters={EMPTY_CREDENTIAL_FILTERS}
        onFiltersChange={() => {}}
        counts={{ all: 1, attention: 0, missingTool: 0 }}
        brands={[]}
        scopes={[]}
        tags={[]}
        credentials={CREDS}
        sort="last_used"
        onSortChange={onSortChange}
        onToggleCollapse={() => {}}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: /sort credentials — last used/i }))
    fireEvent.click(screen.getByRole("button", { name: /^Name$/ }))
    expect(onSortChange).toHaveBeenCalledWith("name")
  })
})

// The bulk-select checkbox moved to the rail when the table went. Dropping
// multi-delete along with the duplicate list would have been a capability lost
// to a layout change.
describe("bulk selection", () => {
  const CREDS = [
    { id: "c1", name: "GH_TOKEN", provider: "GITHUB", type: "CLI_TOKEN" },
    { id: "c2", name: "AWS_MAIN", provider: "AWS", type: "SECRET" },
  ]

  function renderSelectable(over: Record<string, unknown> = {}) {
    const onToggleSelected = vi.fn()
    const onSelectCredential = vi.fn()
    const onSelectModeChange = vi.fn()
    render(
      <CredentialsSidebar
        filters={EMPTY_CREDENTIAL_FILTERS}
        onFiltersChange={() => {}}
        counts={{ all: 2, attention: 0, missingTool: 0 }}
        brands={[]}
        scopes={[]}
        tags={[]}
        credentials={CREDS}
        selectedIds={new Set()}
        onToggleSelected={onToggleSelected}
        onSelectModeChange={onSelectModeChange}
        onSelectCredential={onSelectCredential}
        onToggleCollapse={() => {}}
        {...over}
      />,
    )
    return { onToggleSelected, onSelectCredential, onSelectModeChange }
  }

  it("offers neither the toggle nor a checkbox to a caller that cannot delete", () => {
    renderSidebar({ credentials: CREDS })
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: /select several credentials/i }),
    ).not.toBeInTheDocument()
  })

  // A checkbox on every row all the time says the list is a thing you tick,
  // when it is overwhelmingly a thing you click — and it leaves a bulk delete
  // one mis-click from every secret in the vault.
  it("shows no checkbox until selection mode is on", () => {
    renderSelectable()
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: /select several credentials/i })).toBeInTheDocument()
  })

  it("asks the caller to turn the mode on rather than owning it", () => {
    const { onSelectModeChange } = renderSelectable()
    fireEvent.click(screen.getByRole("button", { name: /select several credentials/i }))
    expect(onSelectModeChange).toHaveBeenCalledWith(true)
  })

  it("offers a way back out once the mode is on", () => {
    const { onSelectModeChange } = renderSelectable({ selectMode: true })
    fireEvent.click(screen.getByRole("button", { name: /leave selection mode/i }))
    expect(onSelectModeChange).toHaveBeenCalledWith(false)
  })

  it("ticks a row without opening it, once the mode is on", () => {
    const { onToggleSelected, onSelectCredential } = renderSelectable({ selectMode: true })
    fireEvent.click(screen.getByRole("checkbox", { name: "Select AWS_MAIN" }))
    expect(onToggleSelected).toHaveBeenCalledWith("c2")
    // Ticking several in a row is the point; navigating away after the first
    // would defeat it.
    expect(onSelectCredential).not.toHaveBeenCalled()
  })
})
