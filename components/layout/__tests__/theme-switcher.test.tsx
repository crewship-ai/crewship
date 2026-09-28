import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

// The Harbor switcher: Dark, Light and System, one press each, and the
// pressed state follows the stored choice rather than the resolved one — a
// user on System must see System selected, not whichever palette it produced.

const setTheme = vi.fn()
let theme = "dark"

vi.mock("next-themes", () => ({
  useTheme: () => ({ theme, setTheme, resolvedTheme: theme === "system" ? "dark" : theme }),
}))

import { ThemeSwitcher } from "../theme-switcher"

describe("ThemeSwitcher", () => {
  beforeEach(() => {
    setTheme.mockReset()
    theme = "dark"
  })
  afterEach(cleanup)

  it("offers Dark and Light plus System, and nothing else", () => {
    render(<ThemeSwitcher />)
    expect(screen.getAllByRole("radio")).toHaveLength(3)
    for (const name of ["Dark", "Light", "System"]) {
      expect(screen.getByRole("radio", { name })).toBeTruthy()
    }
  })

  it.each([
    ["Dark", "dark"],
    ["Light", "light"],
    ["System", "system"],
  ])("%s sets the %s theme", (label, value) => {
    render(<ThemeSwitcher />)
    fireEvent.click(screen.getByRole("radio", { name: label }))
    expect(setTheme).toHaveBeenCalledWith(value)
  })

  it("marks the stored choice, not the resolved palette", () => {
    theme = "system"
    render(<ThemeSwitcher />)
    expect(screen.getByRole("radio", { name: "System" }).getAttribute("aria-checked")).toBe("true")
    expect(screen.getByRole("radio", { name: "Dark" }).getAttribute("aria-checked")).toBe("false")
  })
})
