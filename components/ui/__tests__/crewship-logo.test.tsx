import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, it } from "vitest"
import { CrewshipLogo, CrewshipLogoMark, CrewshipLogoTile } from "@/components/branding/crewship-logo"
import { SAIL_PATH, MARK_VIEWBOX_TIGHT } from "@/lib/brand-mark"
afterEach(cleanup)
it("labels the standalone sail and supports tight bounds without changing its geometry", () => {
  const { rerender } = render(<CrewshipLogo />)
  expect(screen.getByRole("img", { name: "Crewship" })).toHaveAttribute("viewBox", "0 0 1024 1024")
  rerender(<CrewshipLogo tight className="small-logo" aria-label="Crewship home" />)
  const logo = screen.getByRole("img", { name: "Crewship home" })
  expect(logo).toHaveAttribute("viewBox", MARK_VIEWBOX_TIGHT)
  expect(logo).toHaveClass("small-logo")
  expect(logo.querySelector("path")).toHaveAttribute("d", SAIL_PATH)
})
it.each(["navy-white", "navy-blue", "blue-white", "white-blue"] as const)("keeps %s gradient and clip references local to each rendered logo", variant => {
  const { container } = render(<><CrewshipLogoMark variant={variant} /><CrewshipLogoMark variant={variant} /></>)
  const logos = screen.getAllByRole("img", { name: "Crewship" })
  const ids = Array.from(container.querySelectorAll("[id]"), element => element.id)
  expect(new Set(ids).size).toBe(ids.length)
  for (const logo of logos) {
    expect(logo.querySelector("path")).toHaveAttribute("d", SAIL_PATH)
    for (const element of logo.querySelectorAll("[fill], [clip-path]")) {
      for (const attr of ["fill", "clip-path"]) {
        const ref = element.getAttribute(attr)
        if (ref?.startsWith("url(#")) expect(Array.from(logo.querySelectorAll("[id]"), e => e.id)).toContain(ref.slice(5, -1))
      }
    }
    expect(logo.querySelectorAll("linearGradient")).toHaveLength(variant === "navy-white" ? 0 : 1)
  }
})
it("uses the high contrast default and customizable tile dimensions", () => {
  const { rerender, container } = render(<CrewshipLogoMark />)
  expect(screen.getByRole("img").querySelector("g[clip-path] > rect")).toHaveAttribute("fill", "#253043")
  rerender(<CrewshipLogoTile />)
  expect(container.firstChild).toHaveClass("h-12", "w-12", "rounded-2xl")
  expect(screen.getByRole("img")).toHaveClass("h-6", "w-6")
  rerender(<CrewshipLogoTile size="h-8 w-8" rounded="rounded-sm" iconSize="h-4 w-4" className="custom-logo" />)
  expect(container.firstChild).toHaveClass("h-8", "w-8", "rounded-sm", "custom-logo")
  expect(screen.getByRole("img")).toHaveClass("h-4", "w-4")
})
