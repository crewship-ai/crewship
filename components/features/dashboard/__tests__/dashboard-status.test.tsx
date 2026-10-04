import { cleanup, render, screen, within } from "@testing-library/react"
import { afterEach, expect, it } from "vitest"
import { ProjectProgress } from "@/components/features/dashboard/project-progress"
import { InboxTile, type InboxKind } from "@/components/features/dashboard/inbox-tile"

afterEach(cleanup)
it("distinguishes an empty project list and an explicitly supplied empty-state label", () => {
  const { rerender } = render(<ProjectProgress projects={[]} />)
  expect(screen.getByText("No active projects")).toBeInTheDocument()
  rerender(<ProjectProgress projects={[]} emptyLabel="Select a workspace" />)
  expect(screen.getByText("Select a workspace")).toBeInTheDocument()
})
it("clamps stale project counts and only links projects with a destination", () => {
  const { container } = render(<ProjectProgress projects={[
    { id: "normal", name: "Normal project", color: "#123456", issueCount: 3, completedCount: 1, href: "/projects/normal" },
    { id: "over", name: "Lagging total", color: "#abcdef", issueCount: 2, completedCount: 4 },
    { id: "negative", name: "Lagging complete", color: "#123456", issueCount: 2, completedCount: -1 },
    { id: "empty", name: "No issues", color: "#123456", issueCount: 0, completedCount: 0 },
  ]} />)
  expect(screen.getByText("1/3 · 33%")).toBeInTheDocument()
  expect(screen.getByText("4/2 · 100%")).toBeInTheDocument()
  expect(screen.getByText("-1/2 · 0%")).toBeInTheDocument()
  expect(screen.getByText("0/0 · 0%")).toBeInTheDocument()
  expect(screen.getByRole("link", { name: /Normal project/ })).toHaveAttribute("href", "/projects/normal")
  expect(screen.getAllByRole("link")).toHaveLength(1)
  expect(container.querySelector('[style*="--project-color"]')).toHaveStyle({ "--project-color": "#123456" })
  expect(screen.getAllByRole("progressbar")).toHaveLength(4)
})
it("renders inbox empty state and an alternate message", () => {
  const { rerender } = render(<InboxTile entries={[]} />)
  expect(screen.getByText("Inbox empty — you're clear ✓")).toBeInTheDocument()
  rerender(<InboxTile entries={[]} emptyLabel="No approvals pending" />)
  expect(screen.getByText("No approvals pending")).toBeInTheDocument()
})
it("renders all supported inbox kinds, optional details, relative times and action destinations", () => {
  const kinds: InboxKind[] = ["escalation", "keeper", "review", "proposal", "mention"]
  render(<InboxTile entries={kinds.map((kind, i) => ({ id: kind, kind, title: `${kind} item`, relative: `${i}m`, ...(i === 0 ? { subtitle: "Needs your decision", href: "/inbox/escalation" } : {}) }))} />)
  const link = screen.getByRole("link", { name: /escalation item/ })
  expect(link).toHaveAttribute("href", "/inbox/escalation")
  expect(within(link).getByText("Needs your decision")).toBeInTheDocument()
  expect(screen.getAllByRole("link")).toHaveLength(1)
  for (const [i, kind] of kinds.entries()) {
    expect(screen.getByText(`${kind} item`)).toBeInTheDocument()
    expect(screen.getByText(`${i}m`)).toBeInTheDocument()
  }
})
