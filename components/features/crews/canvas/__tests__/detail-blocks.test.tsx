import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { KeyRound, Sparkles, Workflow } from "lucide-react"

import {
  BlockingNotice,
  NowRunning,
  ReachStrip,
  type ReachItem,
} from "@/components/features/crews/canvas/detail-blocks"

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

const reach: ReachItem[] = [
  {
    id: "skills", icon: Sparkles, label: "Skills", value: "2 / 5", tone: "purple",
    cell: {
      title: "Skills",
      filters: [{ id: "all", label: "All" }],
      items: [{ id: "s1", icon: Sparkles, title: "incident-triage", tag: "all" }],
    },
  },
  {
    id: "creds", icon: KeyRound, label: "Keys", value: "2 · 1 pending", tone: "gold", alert: true,
    cell: {
      title: "Credentials",
      filters: [{ id: "all", label: "All" }],
      items: [{ id: "c1", icon: KeyRound, title: "GITHUB_TOKEN", tag: "all" }],
    },
  },
]

describe("<ReachStrip>", () => {
  it("renders one chip per relation with its count", () => {
    render(<ReachStrip items={reach} />)
    expect(screen.getByRole("button", { name: /Skills/ })).toHaveTextContent("2 / 5")
    expect(screen.getByRole("button", { name: /Keys/ })).toHaveTextContent("1 pending")
  })

  it("does not render any list until a chip is clicked", () => {
    render(<ReachStrip items={reach} />)
    expect(screen.queryByText("incident-triage")).not.toBeInTheDocument()
  })

  it("slides out the matching cell and closes again", async () => {
    render(<ReachStrip items={reach} />)

    fireEvent.click(screen.getByRole("button", { name: /Skills/ }))
    expect(await screen.findByRole("dialog", { name: "Skills" })).toBeInTheDocument()
    expect(screen.getByText("incident-triage")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "Close" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  })

  it("opens the credential list from its own chip, not the first one", async () => {
    render(<ReachStrip items={reach} />)
    fireEvent.click(screen.getByRole("button", { name: /Keys/ }))
    expect(await screen.findByRole("dialog", { name: "Keys" })).toBeInTheDocument()
    expect(screen.getByText("GITHUB_TOKEN")).toBeInTheDocument()
    expect(screen.queryByText("incident-triage")).not.toBeInTheDocument()
  })

  it("survives an empty relation list", () => {
    const empty: ReachItem[] = [{
      id: "memory", icon: Workflow, label: "Memory", value: "0", tone: "gold",
      cell: { title: "Memory", filters: [{ id: "all", label: "All" }], items: [] },
    }]
    render(<ReachStrip items={empty} />)
    fireEvent.click(screen.getByRole("button", { name: /Memory/ }))
    expect(screen.getByText(/nothing matches/i)).toBeInTheDocument()
  })
})
