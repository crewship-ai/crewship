import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

import { ToolchainPicker } from "../toolchain-picker"
import { CLI_ADAPTERS, CLI_ADAPTER_KEYS } from "@/lib/cli-adapters"

// The picker's whole job is to make the layout say what canContinue enforces:
// only production adapters can finish setup. A flat grid of six equal chips
// told a first-time user the opposite, and five of the six then refused to
// proceed. So these assert on WHICH adapters are on screen by default, not
// merely that something rendered.

const production = CLI_ADAPTER_KEYS.filter((k) => CLI_ADAPTERS[k].status === "production")
const experimental = CLI_ADAPTER_KEYS.filter((k) => CLI_ADAPTERS[k].status !== "production")

describe("ToolchainPicker", () => {
  it("shows every production adapter as a full card marked fully supported", () => {
    render(<ToolchainPicker value="CLAUDE_CODE" onChange={vi.fn()} />)
    const cards = screen.getAllByTestId("onboarding-toolchain-production")
    expect(cards).toHaveLength(production.length)
    for (const key of production) {
      expect(screen.getByText(CLI_ADAPTERS[key].label)).toBeTruthy()
    }
    expect(screen.getAllByText(/fully supported/i)).toHaveLength(production.length)
  })

  it("keeps the experimental adapters behind a disclosure until asked", () => {
    render(<ToolchainPicker value="CLAUDE_CODE" onChange={vi.fn()} />)
    expect(screen.queryAllByTestId("onboarding-toolchain-experimental-option")).toHaveLength(0)
    const toggle = screen.getByRole("button", { name: /show \d+ experimental toolchains/i })
    expect(toggle.getAttribute("aria-expanded")).toBe("false")
    fireEvent.click(toggle)
    expect(screen.getAllByTestId("onboarding-toolchain-experimental-option")).toHaveLength(experimental.length)
    expect(screen.getAllByText(/^experimental$/i)).toHaveLength(experimental.length)
  })

  it("selects with the adapter's registry key, production or experimental", () => {
    const onChange = vi.fn()
    render(<ToolchainPicker value="CLAUDE_CODE" onChange={onChange} />)
    fireEvent.click(screen.getByRole("button", { name: /show \d+ experimental toolchains/i }))
    fireEvent.click(screen.getByRole("button", { name: /codex cli/i }))
    expect(onChange).toHaveBeenLastCalledWith("CODEX_CLI")
    fireEvent.click(screen.getByRole("button", { name: /claude code/i }))
    expect(onChange).toHaveBeenLastCalledWith("CLAUDE_CODE")
  })

  it("never hides a selection that is experimental (resumed session, or the user's own pick)", () => {
    render(<ToolchainPicker value="GEMINI_CLI" onChange={vi.fn()} />)
    const gemini = screen.getByRole("button", { name: /gemini cli/i })
    expect(gemini.getAttribute("aria-pressed")).toBe("true")
  })

  it("marks exactly one option pressed", () => {
    render(<ToolchainPicker value="CLAUDE_CODE" onChange={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: /show \d+ experimental toolchains/i }))
    const pressed = screen.getAllByRole("button").filter((b) => b.getAttribute("aria-pressed") === "true")
    expect(pressed).toHaveLength(1)
    expect(pressed[0].textContent).toMatch(/Claude Code/)
  })
})
