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

  it("sits at the foot of the sidebar, above the sidebar-mode row, not in the header", () => {
    const src = readFileSync(path.resolve(__dirname, "../app-sidebar.tsx"), "utf8")
    const brand = src.indexOf("<CrewshipBrand")
    expect(brand).toBeGreaterThan(src.indexOf("<SidebarFooter"))
    expect(brand).toBeLessThan(src.indexOf("<SidebarModeMenu"))
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
