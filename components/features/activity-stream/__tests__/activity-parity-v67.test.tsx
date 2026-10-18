// PAR-061 / PAR-062: v67 sizes on Activity — a 13 px semibold row title, and
// the Activity | Spend segment and the range control at the 28 px control height.

import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import type { ChangeItem } from "@/lib/activity-feed"
import { ActivityModeSegment } from "../activity-mode-segment"
import { ActivityRange } from "../activity-range"
import { ActivityPanel } from "../activity-panel"
import { SecondaryPanelContext } from "@/components/layout/secondary-panel"
import { EMPTY_FILTERS } from "@/lib/activity-feed"
import { ChangeRow } from "../activity-feed-table"

vi.mock("next/link", () => ({ default: ({ children, href, ...rest }: { children: React.ReactNode; href: string }) => <a href={href} {...rest}>{children}</a> }))

afterEach(cleanup)

const change = {
  type: "change",
  id: "c1",
  ts: Date.now(),
  kind: "artifact",
  object: "page palette-page-f381a9c0-very-long-internal-title",
  action: "Changed",
  who: "System",
  entry: {},
} as unknown as ChangeItem

describe("Activity v67 sizes", () => {
  it("sets a row title at 13 px semibold and truncates it", () => {
    render(<ChangeRow item={change} now={Date.now()} selected={false} onSelect={vi.fn()} />)
    const title = screen.getByText(/palette-page/)
    expect(title.className).toContain("text-control")
    expect(title.className).toContain("font-semibold")
    expect(title.className).toContain("truncate")
  })

  it("draws Activity | Spend so the whole control is 28 px (inner = control height - 6 px)", () => {
    render(<ActivityModeSegment mode="work" />)
    for (const link of screen.getAllByRole("link")) expect(link.className).toContain("h-[calc(var(--control-sm)-6px)]")
  })

  it("draws the range control at the 28 px control height", () => {
    render(<ActivityRange value={{ key: "7d" }} onChange={vi.fn()} now={new Date(2026, 9, 15).getTime()} />)
    expect(screen.getByRole("group", { name: "Time range" }).className).toContain("h-[var(--control-sm)]")
  })

  it("PAR-059: inside the secondary panel the Filter button shows its word next to the funnel", () => {
    render(
      <SecondaryPanelContext.Provider value={{ toggle: null, claim: () => () => {} }}>
        <ActivityPanel filters={EMPTY_FILTERS} onFilters={vi.fn()} viewCounts={{ all: 0, need: 0, live: 0, failed: 0 }} routines={[]} agents={[]} crews={[]} sources={[]} />
      </SecondaryPanelContext.Provider>,
    )
    const button = screen.getByRole("button", { name: /^Filter/ })
    expect(button).toHaveTextContent("Filter")
    expect(button.querySelector(".sr-only")).toBeNull()
  })
})
