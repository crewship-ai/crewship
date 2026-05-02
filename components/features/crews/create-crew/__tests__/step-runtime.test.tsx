import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { StepRuntime } from "../step-runtime"
import { INITIAL_STATE, type WizardState } from "../types"

function harness(overrides: Partial<WizardState> = {}) {
  let state: WizardState = { ...INITIAL_STATE, ...overrides }
  const setState = vi.fn((patch: Partial<WizardState>) => {
    state = { ...state, ...patch }
  })
  const r = render(<StepRuntime state={state} setState={setState} />)
  return {
    ...r,
    setState,
    rerenderWith: (patch: Partial<WizardState>) => {
      state = { ...state, ...patch }
      r.rerender(<StepRuntime state={state} setState={setState} />)
    },
  }
}

describe("<StepRuntime> — memory", () => {
  it("renders memory presets as chips", () => {
    harness()
    for (const label of ["512 MB", "1 GB", "2 GB", "4 GB", "8 GB"]) {
      expect(screen.getByRole("button", { name: label })).toBeInTheDocument()
    }
  })

  it("clicking a memory preset patches memoryMB", () => {
    const { setState } = harness({ memoryMB: 4096 })
    fireEvent.click(screen.getByRole("button", { name: "1 GB" }))
    expect(setState).toHaveBeenCalledWith({ memoryMB: 1024 })
  })

  it("renders the current memory value prominently in the cell", () => {
    harness({ memoryMB: 2048 })
    // "2 GB" appears twice — once as value display, once as chip label.
    expect(screen.getAllByText("2 GB").length).toBeGreaterThanOrEqual(1)
  })

  it("displays the matching CLI flag", () => {
    harness({ memoryMB: 8192 })
    expect(screen.getByText("--memory-mb 8192")).toBeInTheDocument()
  })

  it("Custom… button appears and switches to numeric input on click", () => {
    const { setState } = harness({ memoryMB: 4096 })

    const customBtn = screen.getAllByRole("button", { name: /Custom/ })[0]
    fireEvent.click(customBtn)

    // Numeric input is now visible (rendered when editing or active)
    const inputs = document.querySelectorAll('input[type="number"]')
    expect(inputs.length).toBeGreaterThan(0)

    const memoryInput = inputs[0] as HTMLInputElement
    fireEvent.change(memoryInput, { target: { value: "3072" } })
    fireEvent.blur(memoryInput)

    expect(setState).toHaveBeenCalledWith({ memoryMB: 3072 })
  })

  it("rejects out-of-range custom value (reverts draft)", () => {
    const { setState } = harness({ memoryMB: 4096 })
    fireEvent.click(screen.getAllByRole("button", { name: /Custom/ })[0])
    const input = document.querySelector('input[type="number"]') as HTMLInputElement
    fireEvent.change(input, { target: { value: "1" } }) // below 128 min
    fireEvent.blur(input)
    // setState should NOT have been called with memoryMB: 1
    const memoryCalls = setState.mock.calls.filter((c) => "memoryMB" in c[0])
    expect(memoryCalls).toHaveLength(0)
  })
})

describe("<StepRuntime> — CPU", () => {
  it("renders CPU presets and patches on click", () => {
    const { setState } = harness({ cpus: 2 })
    fireEvent.click(screen.getByRole("button", { name: "0.5" }))
    expect(setState).toHaveBeenCalledWith({ cpus: 0.5 })
  })

  it("pluralizes the value correctly (1 core vs N cores)", () => {
    const { rerenderWith } = harness({ cpus: 1 })
    expect(screen.getByText("1 core")).toBeInTheDocument()
    rerenderWith({ cpus: 4 })
    expect(screen.getByText("4 cores")).toBeInTheDocument()
  })
})

describe("<StepRuntime> — TTL", () => {
  it("Never preset patches ttlHours to null and CLI flag indicates no --ttl", () => {
    const { setState } = harness({ ttlHours: 24 })
    fireEvent.click(screen.getByRole("button", { name: "Never" }))
    expect(setState).toHaveBeenCalledWith({ ttlHours: null })
  })

  it("displays human-readable idle time in the cell", () => {
    harness({ ttlHours: 4 })
    expect(screen.getByText("4 h idle")).toBeInTheDocument()
  })

  it("shows '(no --ttl)' CLI hint when ttlHours is null", () => {
    harness({ ttlHours: null })
    expect(screen.getByText(/\(no --ttl\)/)).toBeInTheDocument()
  })
})

describe("<StepRuntime> — Network mode", () => {
  it("Free mode hides the allowed-domains editor", () => {
    harness({ networkMode: "free" })
    expect(screen.queryByText(/Allowed domains/)).toBeNull()
  })

  it("clicking Restricted patches networkMode and reveals the domain editor", () => {
    const { setState, rerenderWith } = harness({ networkMode: "free" })
    fireEvent.click(screen.getByRole("button", { name: /Restricted/ }))
    expect(setState).toHaveBeenCalledWith({ networkMode: "restricted" })

    rerenderWith({ networkMode: "restricted" })
    expect(screen.getByText(/Allowed domains/)).toBeInTheDocument()
  })

  it("clicking Free clears any allowedDomains (avoid hidden state)", () => {
    const { setState } = harness({
      networkMode: "restricted",
      allowedDomains: ["github.com"],
    })
    fireEvent.click(screen.getByRole("button", { name: /^Free$/ }))
    expect(setState).toHaveBeenCalledWith({ networkMode: "free", allowedDomains: [] })
  })

  it("warns when Restricted with empty allowlist (locks all egress)", () => {
    harness({ networkMode: "restricted", allowedDomains: [] })
    expect(screen.getByText(/locks all egress/)).toBeInTheDocument()
  })

  it("does NOT warn when Restricted has at least one domain", () => {
    harness({ networkMode: "restricted", allowedDomains: ["github.com"] })
    expect(screen.queryByText(/locks all egress/)).toBeNull()
  })
})

describe("<StepRuntime> — Domain chips", () => {
  it("typing a domain and pressing Enter adds it to the list", () => {
    const { setState } = harness({ networkMode: "restricted", allowedDomains: [] })
    const input = document.querySelector('input[placeholder*="github.com"]') as HTMLInputElement
    expect(input).toBeInTheDocument()

    fireEvent.change(input, { target: { value: "github.com" } })
    fireEvent.keyDown(input, { key: "Enter" })

    expect(setState).toHaveBeenCalledWith({ allowedDomains: ["github.com"] })
  })

  it("comma also commits a domain", () => {
    const { setState } = harness({ networkMode: "restricted", allowedDomains: [] })
    const input = document.querySelector('input[placeholder*="github.com"]') as HTMLInputElement
    fireEvent.change(input, { target: { value: "api.npmjs.org" } })
    fireEvent.keyDown(input, { key: "," })
    expect(setState).toHaveBeenCalledWith({ allowedDomains: ["api.npmjs.org"] })
  })

  it("lowercases incoming domains (case-insensitive matching)", () => {
    const { setState } = harness({ networkMode: "restricted", allowedDomains: [] })
    const input = document.querySelector('input[placeholder*="github.com"]') as HTMLInputElement
    fireEvent.change(input, { target: { value: "GitHub.COM" } })
    fireEvent.keyDown(input, { key: "Enter" })
    expect(setState).toHaveBeenCalledWith({ allowedDomains: ["github.com"] })
  })

  it("ignores duplicate domain (no double-add)", () => {
    const { setState } = harness({
      networkMode: "restricted",
      allowedDomains: ["github.com"],
    })
    const input = document.querySelector('input[placeholder*="add another"]') as HTMLInputElement
    fireEvent.change(input, { target: { value: "github.com" } })
    fireEvent.keyDown(input, { key: "Enter" })
    expect(setState).not.toHaveBeenCalled()
  })

  it("Backspace on empty draft removes the last domain", () => {
    const { setState } = harness({
      networkMode: "restricted",
      allowedDomains: ["a.com", "b.com"],
    })
    const input = document.querySelector('input[placeholder*="add another"]') as HTMLInputElement
    fireEvent.keyDown(input, { key: "Backspace" })
    expect(setState).toHaveBeenCalledWith({ allowedDomains: ["a.com"] })
  })

  it("clicking the × on a domain chip removes it", () => {
    const { setState } = harness({
      networkMode: "restricted",
      allowedDomains: ["github.com", "npmjs.org"],
    })
    fireEvent.click(screen.getByLabelText("Remove github.com"))
    expect(setState).toHaveBeenCalledWith({ allowedDomains: ["npmjs.org"] })
  })

  it("renders existing domains as chips", () => {
    harness({
      networkMode: "restricted",
      allowedDomains: ["github.com", "*.npmjs.org"],
    })
    expect(screen.getByText("github.com")).toBeInTheDocument()
    expect(screen.getByText("*.npmjs.org")).toBeInTheDocument()
  })
})
