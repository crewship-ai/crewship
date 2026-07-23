import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { StepRuntime } from "@/components/features/crews/create-crew/step-runtime"
import { INITIAL_STATE, type WizardState } from "@/components/features/crews/create-crew/types"
import { PACKAGE_REGISTRY_DOMAINS } from "@/components/features/crews/registry-presets"

function renderStep(overrides: Partial<WizardState> = {}) {
  const setState = vi.fn()
  const state: WizardState = { ...INITIAL_STATE, networkMode: "restricted", allowedDomains: [], ...overrides }
  render(<StepRuntime state={state} setState={setState} />)
  return { setState, state }
}

describe("<StepRuntime> NetworkCell — #1377 registry preset", () => {
  it("appends the registry preset onto existing domains", () => {
    const { setState } = renderStep({ allowedDomains: ["github.com"] })
    fireEvent.click(screen.getByRole("button", { name: /package registries/i }))

    expect(setState).toHaveBeenCalledTimes(1)
    const patch = setState.mock.calls[0][0] as Partial<WizardState>
    expect(patch.allowedDomains).toBeDefined()
    const domains = patch.allowedDomains!
    expect(domains).toContain("github.com") // preserved
    for (const host of PACKAGE_REGISTRY_DOMAINS) {
      expect(domains).toContain(host)
    }
  })

  it("advertises wildcard subdomain support", () => {
    renderStep()
    expect(screen.getByText(/\*\.github\.com/)).toBeInTheDocument()
  })
})
