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
        title="Čeká na tvoje rozhodnutí."
        body="Morgan chce GITHUB_TOKEN."
        detail="drží run 12 minut"
        actions={[
          { label: "Zamítnout", onClick: deny },
          { label: "Autorizovat", onClick: approve, primary: true },
        ]}
      />,
    )

    expect(screen.getByText("Čeká na tvoje rozhodnutí.")).toBeInTheDocument()
    expect(screen.getByText(/drží run 12 minut/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "Autorizovat" }))
    expect(approve).toHaveBeenCalledTimes(1)
    expect(deny).not.toHaveBeenCalled()
  })

  it("renders without actions", () => {
    render(<BlockingNotice title="Klíč bez nástroje." body="chybí github-cli" tone="notice" />)
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
    expect(screen.queryByRole("button", { name: "Zastavit" })).not.toBeInTheDocument()

    const onStop = vi.fn()
    rerender(<NowRunning label="run" onStop={onStop} />)
    fireEvent.click(screen.getByRole("button", { name: "Zastavit" }))
    expect(onStop).toHaveBeenCalled()
  })
})

const reach: ReachItem[] = [
  {
    id: "skills", icon: Sparkles, label: "Skilly", value: "2 / 5", tone: "purple",
    cell: {
      title: "Skilly",
      filters: [{ id: "all", label: "Vše" }],
      items: [{ id: "s1", icon: Sparkles, title: "incident-triage", tag: "all" }],
    },
  },
  {
    id: "creds", icon: KeyRound, label: "Klíče", value: "2 · 1 čeká", tone: "gold", alert: true,
    cell: {
      title: "Credentials",
      filters: [{ id: "all", label: "Vše" }],
      items: [{ id: "c1", icon: KeyRound, title: "GITHUB_TOKEN", tag: "all" }],
    },
  },
]

describe("<ReachStrip>", () => {
  it("renders one chip per relation with its count", () => {
    render(<ReachStrip items={reach} />)
    expect(screen.getByRole("button", { name: /Skilly/ })).toHaveTextContent("2 / 5")
    expect(screen.getByRole("button", { name: /Klíče/ })).toHaveTextContent("1 čeká")
  })

  it("does not render any list until a chip is clicked", () => {
    render(<ReachStrip items={reach} />)
    expect(screen.queryByText("incident-triage")).not.toBeInTheDocument()
  })

  it("slides out the matching cell and closes again", async () => {
    render(<ReachStrip items={reach} />)

    fireEvent.click(screen.getByRole("button", { name: /Skilly/ }))
    expect(await screen.findByRole("dialog", { name: "Skilly" })).toBeInTheDocument()
    expect(screen.getByText("incident-triage")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "Zavřít" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  })

  it("opens the credential list from its own chip, not the first one", async () => {
    render(<ReachStrip items={reach} />)
    fireEvent.click(screen.getByRole("button", { name: /Klíče/ }))
    expect(await screen.findByRole("dialog", { name: "Klíče" })).toBeInTheDocument()
    expect(screen.getByText("GITHUB_TOKEN")).toBeInTheDocument()
    expect(screen.queryByText("incident-triage")).not.toBeInTheDocument()
  })

  it("survives an empty relation list", () => {
    const empty: ReachItem[] = [{
      id: "memory", icon: Workflow, label: "Paměť", value: "0", tone: "gold",
      cell: { title: "Paměť", filters: [{ id: "all", label: "Vše" }], items: [] },
    }]
    render(<ReachStrip items={empty} />)
    fireEvent.click(screen.getByRole("button", { name: /Paměť/ }))
    expect(screen.getByText(/nic neodpovídá/i)).toBeInTheDocument()
  })
})
