import { describe, expect, it, vi } from "vitest"
import { render, screen } from "@testing-library/react"
import { Users } from "lucide-react"
import { readFileSync } from "node:fs"
import path from "node:path"

vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => ({ ok: true, status: 200, json: async () => ({}) })) }))

import { SidebarRow, SidebarRowTile, SidebarSection } from "../sidebar-kit"
import { DrillNavItem } from "../drill-page"
import { PanelSpaces } from "../panel-spaces"

// PAR-154: panel rows are 30 px with 24 px icon tiles; group headers 11.5 px.

const css = readFileSync(path.resolve(__dirname, "../../../app/globals.css"), "utf8")

describe("panel row density (PAR-154)", () => {
  it("SidebarRow is a 30 px row", () => {
    render(<ul><SidebarRow aria-label="Mine">Mine</SidebarRow></ul>)
    expect(screen.getByLabelText("Mine").className).toContain("min-h-[30px]")
  })

  it("SidebarRowTile is a 24 px grey tile around a 14 px icon", () => {
    const { container } = render(<SidebarRowTile icon={Users} />)
    const tile = container.firstElementChild as HTMLElement
    expect(tile).toHaveAttribute("data-slot", "row-tile")
    expect(tile).toHaveClass("size-6")
    expect(tile.querySelector("svg")).toHaveClass("size-3.5")
  })

  it("DrillNavItem is a 30 px row and puts a given icon on the tile", () => {
    render(<DrillNavItem label="Members" tile={Users} onSelect={() => {}} />)
    const row = screen.getByRole("button", { name: "Members" })
    expect(row.className).toContain("min-h-[30px]")
    expect(row.querySelector('[data-slot="row-tile"]')).not.toBeNull()
  })

  it("PanelSpaces rows are 30 px with a tile", () => {
    render(<PanelSpaces items={[{ key: "a", label: "Alpha", icon: Users }]} value="a" onChange={() => {}} />)
    const row = screen.getByRole("tab", { name: /alpha/i })
    expect(row.className).toContain("min-h-[30px]")
    expect(row.querySelector('[data-slot="row-tile"]')).not.toBeNull()
  })

  it("group headers use the 11.5 px group-head role", () => {
    render(<SidebarSection label="Projects" count={2}><div /></SidebarSection>)
    expect(screen.getByText("Projects")).toHaveClass("type-group-head")
    const i = css.indexOf(".type-group-head {")
    expect(css.slice(i, css.indexOf("}", i))).toMatch(/font-size:\s*max\(11\.5px/)
  })
})
