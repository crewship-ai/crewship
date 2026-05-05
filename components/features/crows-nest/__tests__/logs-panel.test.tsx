import { describe, it, expect, vi } from "vitest"
import { render, fireEvent, within } from "@testing-library/react"
import type { JournalEntry } from "@/lib/types/journal"

// Mock Virtuoso so all rows render synchronously into the DOM —
// happy-dom has no real layout, so the real Virtuoso renders zero items.
vi.mock("react-virtuoso", () => ({
  Virtuoso: ({
    data,
    itemContent,
  }: {
    data: JournalEntry[]
    itemContent: (i: number, e: JournalEntry) => React.ReactNode
  }) => (
    <div data-testid="virtuoso">
      {data.map((item, i) => (
        <div key={item.id} data-testid="virtuoso-row">
          {itemContent(i, item)}
        </div>
      ))}
    </div>
  ),
}))

// Mock recharts to a no-op div — happy-dom can't measure ResponsiveContainer.
vi.mock("recharts", () => ({
  Bar: () => null,
  BarChart: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
  ResponsiveContainer: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
  XAxis: () => null,
  Tooltip: () => null,
}))

import { LogsPanel } from "@/components/features/crows-nest/logs-panel"

function entry(overrides: Partial<JournalEntry> = {}): JournalEntry {
  return {
    id: `id-${Math.random().toString(36).slice(2, 8)}`,
    workspace_id: "ws_test",
    ts: new Date().toISOString(),
    entry_type: "exec.command",
    severity: "info",
    actor_type: "agent",
    summary: "viktor runs pnpm test",
    payload: {},
    refs: {},
    ...overrides,
  }
}

describe("LogsPanel", () => {
  it("renders one row per entry through the virtualized list", () => {
    const entries = [
      entry({ id: "a", entry_type: "exec.command", summary: "viktor: pnpm test" }),
      entry({ id: "b", entry_type: "network.egress", summary: "→ api.anthropic.com:443" }),
      entry({ id: "c", entry_type: "keeper.decision", severity: "notice", summary: "ALLOW read /tmp" }),
    ]
    const { getAllByTestId } = render(<LogsPanel entries={entries} />)
    expect(getAllByTestId("virtuoso-row")).toHaveLength(3)
  })

  it("filters by severity when a chip is clicked", () => {
    const entries = [
      entry({ id: "a", severity: "info", summary: "info-row" }),
      entry({ id: "b", severity: "warn", summary: "warn-row" }),
      entry({ id: "c", severity: "warn", summary: "another-warn" }),
    ]
    const { getAllByTestId, getByRole } = render(<LogsPanel entries={entries} />)
    expect(getAllByTestId("virtuoso-row")).toHaveLength(3)
    fireEvent.click(getByRole("button", { name: /^warn/i }))
    const rows = getAllByTestId("virtuoso-row")
    expect(rows).toHaveLength(2)
    expect(rows[0].textContent).toMatch(/warn/i)
  })

  it("hides a group when its type chip is toggled off", () => {
    const entries = [
      entry({ id: "a", entry_type: "exec.command", summary: "exec-a" }),
      entry({ id: "b", entry_type: "exec.output_chunk", summary: "stdout-b" }),
      entry({ id: "c", entry_type: "network.egress", summary: "egress-c" }),
    ]
    const { getAllByTestId, getByRole } = render(<LogsPanel entries={entries} />)
    expect(getAllByTestId("virtuoso-row")).toHaveLength(3)
    // Click the "exec" chip to mute it
    fireEvent.click(getByRole("button", { name: /^exec/i }))
    const rows = getAllByTestId("virtuoso-row")
    expect(rows).toHaveLength(1)
    expect(rows[0].textContent).toMatch(/egress-c/)
  })

  it("filters by free-text search across summary and entry_type", () => {
    const entries = [
      entry({ id: "a", summary: "ALLOW read /home/agent/secrets.env" }),
      entry({ id: "b", summary: "DENY  write /etc/passwd" }),
      entry({ id: "c", summary: "viktor: pnpm test --filter foo" }),
    ]
    const { getAllByTestId, getByPlaceholderText } = render(<LogsPanel entries={entries} />)
    const search = getByPlaceholderText(/search/i)
    fireEvent.change(search, { target: { value: "deny" } })
    const rows = getAllByTestId("virtuoso-row")
    expect(rows).toHaveLength(1)
    expect(rows[0].textContent).toMatch(/DENY/)
  })

  it("supports /regex/ search syntax", () => {
    const entries = [
      entry({ id: "a", summary: "user 42 logged in" }),
      entry({ id: "b", summary: "user abc logged in" }),
      entry({ id: "c", summary: "user 9 logged out" }),
    ]
    const { getAllByTestId, getByPlaceholderText } = render(<LogsPanel entries={entries} />)
    fireEvent.change(getByPlaceholderText(/search/i), { target: { value: "/user \\d+ logged in/" } })
    const rows = getAllByTestId("virtuoso-row")
    expect(rows).toHaveLength(1)
    expect(rows[0].textContent).toMatch(/user 42/)
  })

  it("shows the empty-state message when filters cull every entry", () => {
    const entries = [entry({ id: "a", severity: "info", summary: "x" })]
    const { getByPlaceholderText, getByText } = render(<LogsPanel entries={entries} />)
    fireEvent.change(getByPlaceholderText(/search/i), { target: { value: "no-such-string-anywhere" } })
    expect(getByText(/No entries match the current filters/i)).toBeTruthy()
  })

  it("expands a row to reveal payload JSON when clicked", () => {
    const entries = [
      entry({
        id: "a",
        entry_type: "keeper.decision",
        severity: "notice",
        summary: "ALLOW read",
        payload: { decision: "ALLOW", risk_score: 2 },
      }),
    ]
    const { getAllByTestId } = render(<LogsPanel entries={entries} />)
    const row = getAllByTestId("virtuoso-row")[0]
    const inner = within(row)
    fireEvent.click(inner.getByRole("button"))
    // Expanded detail should expose the payload key/value as raw text.
    expect(row.textContent).toMatch(/risk_score/)
    expect(row.textContent).toMatch(/ALLOW/)
  })
})
