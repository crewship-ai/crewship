import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { readFileSync } from "node:fs"
import path from "node:path"

// The owner, 19 Oct: the Crewship sail stays visible at every rail width and
// leads to a short "what is Crewship" with the build. It sits at the foot of
// the rail, above the sidebar-mode row, in its own lit blue tile; the top of
// the rail is the workspace switcher and Search only.

vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/hooks/use-user-preference", () => ({ useUserPreference: (_k: string, d: unknown) => [d, () => {}] }))
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

import { SidebarProvider } from "@/components/ui/sidebar"
import { CrewshipBrand } from "@/components/layout/crewship-brand"
import { DOCS_URL, RELEASES_URL, SUPPORT_URL } from "@/lib/crewship-links"

beforeEach(() => {
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} }))
  apiFetch.mockImplementation((url: string) =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve(url.endsWith("/version") ? { current: "0.9.0", commit: "a41d725" } : { edition: "community" }),
    }),
  )
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  apiFetch.mockReset()
})

describe("Crewship brand in the rail", () => {
  it("shows the ship and the name, and the name folds away with the rail", () => {
    render(<SidebarProvider><CrewshipBrand /></SidebarProvider>)
    const button = screen.getByRole("button", { name: "About Crewship" })
    expect(button.querySelector(".rail-brand svg")).not.toBeNull()
    expect(screen.getByText("Crewship")).toHaveClass("rail-label")
  })

  it("opens what Crewship is, which build this is, and where to read more", async () => {
    render(<SidebarProvider><CrewshipBrand /></SidebarProvider>)
    fireEvent.click(screen.getByRole("button", { name: "About Crewship" }))
    const dialog = await screen.findByRole("dialog", { name: "About Crewship" })
    expect(dialog).toHaveTextContent(/AI agents/)
    expect(await screen.findByTestId("about-build")).toHaveTextContent("0.9.0")
    expect(screen.getByRole("link", { name: /Documentation/ })).toHaveAttribute("href", DOCS_URL)
    expect(screen.getByRole("link", { name: /What's new/ })).toHaveAttribute("href", RELEASES_URL)
    expect(screen.getByRole("link", { name: /Report a problem/ })).toHaveAttribute("href", SUPPORT_URL)
  })

  it("sits at the bottom of the rail, above the footer's rule, not in the header or the footer", () => {
    const src = readFileSync(path.resolve(__dirname, "../app-sidebar.tsx"), "utf8")
    const brand = src.indexOf("<CrewshipBrand")
    expect(brand).toBeGreaterThan(src.indexOf("</SidebarContent>"))
    expect(brand).toBeLessThan(src.indexOf("<SidebarFooter"))
    const header = src.slice(src.indexOf("<SidebarHeader"), src.indexOf("</SidebarHeader>"))
    expect(header).not.toContain("CrewshipBrand")
  })

  it("wears the brand's own lit blue, whatever the style", () => {
    const css = readFileSync(path.resolve(__dirname, "../../../app/globals.css"), "utf8")
    const rule = css.slice(css.indexOf(".rail-brand {"), css.indexOf("}", css.indexOf(".rail-brand {")))
    expect(rule).toMatch(/#1B75FE/i)
    expect(rule).toMatch(/box-shadow:.*rgb\(27 117 254/)
  })
})

// Owner, 19 Oct: the workspace is the main divide of the rail, so its mark is
// as large as the person's avatar at the foot (size-7), folded or open.
describe("workspace mark size", () => {
  it("matches the account avatar in the rail", () => {
    const ws = readFileSync(path.resolve(__dirname, "../workspace-switcher.tsx"), "utf8")
    const me = readFileSync(path.resolve(__dirname, "../user-menu.tsx"), "utf8")
    expect(me).toMatch(/UserAvatar[^\n]*className="size-7"/)
    const marks = ws.match(/className="rail-tile rail-ws[^"]*"/g) ?? []
    expect(marks.length).toBe(2)
    for (const m of marks) expect(m).toMatch(/\bsize-7\b/)
    const css = readFileSync(path.resolve(__dirname, "../../../app/globals.css"), "utf8")
    expect(css).toMatch(/\.rail-tile\.rail-ws\s*\{[^}]*width:\s*auto/)
  })
})
