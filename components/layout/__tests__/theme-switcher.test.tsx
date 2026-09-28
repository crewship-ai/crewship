import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

// The Harbor switcher: Day, Dusk, Night and System, one press each, and the
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

  it("offers the three Harbor themes plus System", () => {
    render(<ThemeSwitcher />)
    for (const name of ["Day", "Dusk", "Night", "System"]) {
      expect(screen.getByRole("radio", { name })).toBeTruthy()
    }
  })

  it.each([
    ["Day", "light"],
    ["Dusk", "dusk"],
    ["Night", "dark"],
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
    expect(screen.getByRole("radio", { name: "Night" }).getAttribute("aria-checked")).toBe("false")
  })
})
