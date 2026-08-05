import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

import { RoutineApprovalBanner } from "../routine-approval-banner"
import type { PendingWaitpoint } from "@/hooks/use-pending-approval"

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))

const LONG_PROMPT = `Approve this production action?

**Change Plan: Restart auth-svc pods in production**

Execute a rolling restart of all auth-svc pods in the production cluster to
clear the suspected connection pool exhaustion affecting login latency.`

function waitpoint(overrides: Partial<PendingWaitpoint> = {}): PendingWaitpoint {
  return {
    token: "tok-1",
    step_id: "approve",
    prompt: LONG_PROMPT,
    timeout_at: new Date(Date.now() + 23 * 3600_000).toISOString(),
    ...overrides,
  } as PendingWaitpoint
}

describe("<RoutineApprovalBanner> Approve button", () => {
  // Shipped painting its label in its own background colour — `bg-warn`
  // and `text-warn` on the same element. The button was there, the
  // accessible name was there, and a human saw a blank yellow rectangle.
  // No DOM query catches that, so the class pair is asserted directly.
  it("does not paint its label in its own background colour", () => {
    render(<RoutineApprovalBanner waitpoint={waitpoint()} deciding={false} onDecide={vi.fn()} />)
    const approve = screen.getByRole("button", { name: /approve/i })
    const classes = approve.className
    expect(classes.includes("bg-warn") && classes.includes("text-warn")).toBe(false)
  })

  it("is reachable by its accessible name", () => {
    render(<RoutineApprovalBanner waitpoint={waitpoint()} deciding={false} onDecide={vi.fn()} />)
    expect(screen.getByRole("button", { name: /approve/i })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /reject/i })).toBeInTheDocument()
  })
})

describe("<RoutineApprovalBanner> compactness", () => {
  // The whole prompt inline turned a decision into a wall of text that
  // pushed the routine itself off screen. The strip carries enough to
  // decide from; the full request is one click away.
  it("does not dump the whole prompt inline", () => {
    render(<RoutineApprovalBanner waitpoint={waitpoint()} deciding={false} onDecide={vi.fn()} />)
    expect(screen.queryByText(/connection pool exhaustion/)).not.toBeInTheDocument()
  })

  it("shows the full request on demand", async () => {
    render(<RoutineApprovalBanner waitpoint={waitpoint()} deciding={false} onDecide={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: /view request/i }))
    await waitFor(() => {
      expect(screen.getByText(/connection pool exhaustion/)).toBeInTheDocument()
    })
  })

  it("still leads with the first line, so the strip is not anonymous", () => {
    render(<RoutineApprovalBanner waitpoint={waitpoint()} deciding={false} onDecide={vi.fn()} />)
    expect(screen.getByText(/Approve this production action\?/)).toBeInTheDocument()
  })
})

describe("<RoutineApprovalBanner> decisions", () => {
  it("approves through the supplied handler", async () => {
    const onDecide = vi.fn().mockResolvedValue(true)
    render(<RoutineApprovalBanner waitpoint={waitpoint()} deciding={false} onDecide={onDecide} />)
    fireEvent.click(screen.getByRole("button", { name: /approve/i }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledWith(true, ""))
  })

  it("rejects through the supplied handler", async () => {
    const onDecide = vi.fn().mockResolvedValue(true)
    render(<RoutineApprovalBanner waitpoint={waitpoint()} deciding={false} onDecide={onDecide} />)
    fireEvent.click(screen.getByRole("button", { name: /reject/i }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledWith(false, ""))
  })

  it("disables both while a decision is in flight", () => {
    render(<RoutineApprovalBanner waitpoint={waitpoint()} deciding onDecide={vi.fn()} />)
    expect(screen.getByRole("button", { name: /approve/i })).toBeDisabled()
    expect(screen.getByRole("button", { name: /reject/i })).toBeDisabled()
  })
})
