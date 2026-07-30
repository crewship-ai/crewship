import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { BlockingNotice, NowRunning } from "@/components/features/crews/canvas/detail-blocks"

describe("<BlockingNotice>", () => {
  it("renders the decision and both actions", () => {
    const approve = vi.fn()
    const deny = vi.fn()
    render(
      <BlockingNotice
        title="Waiting on your decision."
        body="Morgan wants GITHUB_TOKEN."
        detail="holding the run for 12 minutes"
        actions={[
          { label: "Deny", onClick: deny },
          { label: "Approve", onClick: approve, primary: true },
        ]}
      />,
    )

    expect(screen.getByText("Waiting on your decision.")).toBeInTheDocument()
    expect(screen.getByText(/holding the run for 12 minutes/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "Approve" }))
    expect(approve).toHaveBeenCalledTimes(1)
    expect(deny).not.toHaveBeenCalled()
  })

  it("renders without actions", () => {
    render(<BlockingNotice title="Key without a tool." body="github-cli missing" tone="notice" />)
    expect(screen.queryAllByRole("button")).toHaveLength(0)
  })
})

describe("<NowRunning>", () => {
  it("shows the label, step and meta", () => {
    render(<NowRunning label="nightly-health-summary" step="krok 3 / 5" percent={60} meta="1 m 04 s" />)
    expect(screen.getByText("nightly-health-summary")).toBeInTheDocument()
    expect(screen.getByText("krok 3 / 5")).toBeInTheDocument()
    expect(screen.getByText("1 m 04 s")).toBeInTheDocument()
  })

  it("offers Stop only when a handler is supplied", () => {
    const { rerender } = render(<NowRunning label="run" />)
    expect(screen.queryByRole("button", { name: "Stop" })).not.toBeInTheDocument()

    const onStop = vi.fn()
    rerender(<NowRunning label="run" onStop={onStop} />)
    fireEvent.click(screen.getByRole("button", { name: "Stop" }))
    expect(onStop).toHaveBeenCalled()
  })
})
