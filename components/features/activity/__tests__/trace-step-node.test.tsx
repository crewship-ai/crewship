import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { toast } from "sonner"
import { waitpointDecide } from "@/lib/api/waitpoints"
import { TraceStepNode } from "../trace-step-node"
import type { NodeProps } from "@xyflow/react"
import type { StepStatus } from "@/lib/trace/types"

// Stale Approve/Deny bugfix (feedback 2026-07-02): a waitpoint that
// was approved elsewhere left the trace-canvas wait node with armed
// Approve/Deny buttons — clicking produced the red "waitpoint:
// already decided or expired" toast forever. The node must (a) derive
// a resolved label from the step status when the decision landed
// elsewhere, (b) swap its own buttons for the label after a
// successful decide, and (c) recover gracefully into the resolved
// state when the API answers "already decided".

// React Flow's Handle needs a zustand store provider; the node under
// test only needs the visual shell.
vi.mock("@xyflow/react", () => ({
  Handle: () => null,
  Position: { Left: "left", Right: "right" },
}))

// HoverCard chrome is irrelevant here — pass children through.
vi.mock("../step-hover-card", () => ({
  StepHoverCard: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}))

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

vi.mock("@/lib/api/waitpoints", () => ({
  waitpointDecide: vi.fn(),
}))

function renderWaitNode(
  status: StepStatus,
  waitpoint: { token: string; workspaceId: string } | undefined = {
    token: "tok-1",
    workspaceId: "ws-1",
  },
) {
  const data = {
    step: { id: "gate", type: "wait", wait: { kind: "approval" } },
    status,
    selected: false,
    waitpoint,
  }
  return render(<TraceStepNode {...({ data } as unknown as NodeProps)} />)
}

const approveBtn = () => screen.queryByRole("button", { name: /approve waitpoint/i })
const denyBtn = () => screen.queryByRole("button", { name: /deny waitpoint/i })

describe("<TraceStepNode> waitpoint actions", () => {
  beforeEach(() => {
    vi.mocked(waitpointDecide).mockResolvedValue({ ok: true })
  })

  it("renders active Approve/Deny while the waitpoint is pending", () => {
    renderWaitNode("waiting")
    expect(approveBtn()).toBeInTheDocument()
    expect(denyBtn()).toBeInTheDocument()
  })

  it("renders a resolved label instead of buttons when the step already succeeded", () => {
    // Approved elsewhere: run resumed, step status advanced to
    // success, but the stale token is still passed down.
    renderWaitNode("success")
    expect(approveBtn()).not.toBeInTheDocument()
    expect(denyBtn()).not.toBeInTheDocument()
    expect(screen.getByRole("status")).toHaveTextContent(/approved/i)
  })

  it("renders the denied label when the step failed", () => {
    renderWaitNode("failed")
    expect(approveBtn()).not.toBeInTheDocument()
    expect(screen.getByRole("status")).toHaveTextContent(/denied/i)
  })

  it("swaps buttons for the approved label after a successful approve", async () => {
    renderWaitNode("waiting")
    fireEvent.click(approveBtn()!)
    await waitFor(() => {
      expect(screen.getByRole("status")).toHaveTextContent(/approved/i)
    })
    expect(approveBtn()).not.toBeInTheDocument()
    expect(denyBtn()).not.toBeInTheDocument()
    expect(toast.success).toHaveBeenCalled()
  })

  it("recovers into the resolved state on an already-decided error", async () => {
    vi.mocked(waitpointDecide).mockResolvedValue({
      ok: false,
      error: "waitpoint: already decided or expired",
      status: 409,
    })
    renderWaitNode("waiting")
    fireEvent.click(approveBtn()!)
    await waitFor(() => {
      expect(screen.getByRole("status")).toHaveTextContent(/already decided/i)
    })
    // Buttons must NOT come back — that's the reported bug.
    expect(approveBtn()).not.toBeInTheDocument()
    expect(denyBtn()).not.toBeInTheDocument()
    // No red error toast for the graceful path.
    expect(toast.error).not.toHaveBeenCalled()
  })

  it("recognises already-decided from the error string without a status code", async () => {
    vi.mocked(waitpointDecide).mockResolvedValue({
      ok: false,
      error: "waitpoint: already decided or expired",
    })
    renderWaitNode("waiting")
    fireEvent.click(denyBtn()!)
    await waitFor(() => {
      expect(screen.getByRole("status")).toHaveTextContent(/already decided/i)
    })
  })

  it("keeps the buttons armed on an unrelated decide error", async () => {
    vi.mocked(waitpointDecide).mockResolvedValue({
      ok: false,
      error: "internal error",
      status: 500,
    })
    renderWaitNode("waiting")
    fireEvent.click(approveBtn()!)
    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith("internal error")
    })
    expect(approveBtn()).toBeInTheDocument()
    expect(denyBtn()).toBeInTheDocument()
  })
})
