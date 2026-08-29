import { describe, it, expect, beforeEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import { ProvisioningEventSteps } from "../app-toolbar-provisioning"
import type { ProvisionStepState } from "@/hooks/use-provisioning-status"

/**
 * A run held by admission control (`capacity_hold`) already carries the
 * machine-readable cause over the wire — the hook parses it into
 * `ProvisionStepState.reason` — but nothing rendered it, so every held run
 * looked identical to a hung one (#2167). This locks in that the row shows
 * *why*, not just *that*, it's waiting.
 */
describe("ProvisioningEventSteps — capacity hold reason", () => {
  beforeEach(() => cleanup())

  it("renders a human reason alongside the step label for a known capacity_hold token", () => {
    const steps: ProvisionStepState[] = [
      { key: "step:capacity_hold", label: "Waiting for host capacity", status: "started", reason: "host_memory" },
    ]
    render(<ProvisioningEventSteps steps={steps} />)
    expect(screen.getByText("Waiting for host capacity")).toBeTruthy()
    expect(screen.getByTestId("provision-step-reason").textContent).toMatch(/not enough free host memory/i)
  })

  it("falls back to the raw token for a reason this build does not recognize, rather than hiding it", () => {
    const steps: ProvisionStepState[] = [
      { key: "step:capacity_hold", label: "Waiting for host capacity", status: "started", reason: "gpu_quota" },
    ]
    render(<ProvisioningEventSteps steps={steps} />)
    expect(screen.getByTestId("provision-step-reason").textContent).toMatch(/gpu_quota/)
  })

  it("renders no reason clause for a step without one", () => {
    const steps: ProvisionStepState[] = [
      { key: "feature:ansible", label: "ansible", status: "started" },
    ]
    render(<ProvisioningEventSteps steps={steps} />)
    expect(screen.getByText("ansible")).toBeTruthy()
    expect(screen.queryByTestId("provision-step-reason")).toBeNull()
  })
})
