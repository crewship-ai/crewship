import { describe, it, expect, vi } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import { IssuesListView } from "../issues-list-view"
import type { Mission } from "@/lib/types"

vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER", can: () => true }) }))
// The row's density preference is server-backed; the layout question here does
// not depend on it, and vitest.setup fails any test that reaches the network.
vi.mock("@/hooks/use-user-preference", () => ({
  useUserPreference: (_k: string, fallback: unknown) => [fallback, vi.fn(), { loading: false }],
}))

function issue(over: Partial<Mission> = {}): Mission {
  return {
    id: "iss-1",
    identifier: "ABC-1",
    title: "Re-validate issue endpoints",
    status: "TODO",
    priority: "none",
    created_at: "2026-09-01T10:00:00Z",
    updated_at: "2026-09-02T10:00:00Z",
    ...over,
  } as Mission
}

function renderList(issues: Mission[]) {
  cleanup()
  return render(
    <IssuesListView issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" />,
  )
}

// =============================================================================
// Nine fixed-width columns come to ~760px, so on a phone the list view was a
// grid you scrolled sideways through. Both forms are in the DOM at once and a
// container query picks between them, so what a test can assert is that the
// pair exists and stays in step — not which one a headless layout resolves to.
// =============================================================================

describe("the issues list carries a narrow form", () => {
  it("renders a table and a card list from the same rows", () => {
    const rows = [issue(), issue({ id: "iss-2", identifier: "ABC-2", title: "Second" })]
    const { container } = renderList(rows)

    expect(container.querySelector("table"), "no table form").toBeTruthy()
    const cards = container.querySelector("ul")
    expect(cards, "no card form").toBeTruthy()
    expect(cards!.children.length, "card count does not match the rows").toBe(rows.length)
  })

  it("hands the narrow form to a container query, not a viewport one", () => {
    // This view also renders in a narrowed pane beside an open issue, where the
    // width that decides is the pane's and not the window's.
    const { container } = renderList([issue()])
    const scope = container.querySelector('[class*="@container/issues"]')
    expect(scope, "no container scope declared").toBeTruthy()
    expect(container.querySelector('[class*="@md/issues:hidden"]'), "cards are not container-gated").toBeTruthy()
    expect(container.querySelector('[class*="@md/issues:block"]'), "table is not container-gated").toBeTruthy()
  })

  it("shows every issue in the narrow form, not a truncated preview", () => {
    const rows = [
      issue({ id: "a", identifier: "A-1", title: "Alpha" }),
      issue({ id: "b", identifier: "B-1", title: "Bravo" }),
      issue({ id: "c", identifier: "C-1", title: "Charlie" }),
    ]
    renderList(rows)
    for (const r of rows) {
      // Both forms render the title, so each appears twice — the point is that
      // none is missing from the narrow one.
      expect(screen.getAllByText(r.title!).length, `${r.title} missing`).toBeGreaterThanOrEqual(2)
    }
  })
})
