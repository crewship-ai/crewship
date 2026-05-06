import { describe, it, expect } from "vitest"
import { render } from "@testing-library/react"

import { ActivityPanel } from "@/components/features/crows-nest/activity-panel"
import type { JournalEntry } from "@/lib/types/journal"

function entry(overrides: Partial<JournalEntry> = {}): JournalEntry {
  return {
    id: "j1",
    workspace_id: "ws_test",
    ts: "2026-05-05T11:15:55Z",
    entry_type: "exec.command",
    severity: "info",
    actor_type: "agent",
    summary: "filip runs claude --print …",
    payload: {},
    refs: {},
    ...overrides,
  } as JournalEntry
}

describe("ActivityPanel", () => {
  it("renders the empty state when no entries are passed", () => {
    const { getByText } = render(<ActivityPanel entries={[]} />)
    expect(getByText(/No activity yet/i)).toBeTruthy()
  })

  it("renders a row per entry with the typed badge label", () => {
    const entries: JournalEntry[] = [
      entry({ id: "j1", entry_type: "exec.command", summary: "filip runs claude" }),
      entry({ id: "j2", entry_type: "network.egress", summary: "CONNECT api.anthropic.com:443 → 200" }),
      entry({ id: "j3", entry_type: "agent.status_change", severity: "notice", summary: "filip: online → busy" }),
    ]
    const { container, queryByText } = render(<ActivityPanel entries={entries} />)
    // Typed badge labels live inside <code> nodes; collect them by tag
    // rather than getByText (which can fall over on dot-containing
    // labels under jsdom's text-match heuristic).
    const codeLabels = Array.from(container.querySelectorAll("code")).map((c) => c.textContent)
    expect(codeLabels).toEqual(["exec", "egress", "status"])
    // Summaries render verbatim into the row's main span.
    const rowText = Array.from(container.querySelectorAll("li")).map((li) => li.textContent)
    expect(rowText[0]).toContain("filip runs claude")
    expect(rowText[1]).toContain("CONNECT api.anthropic.com:443 → 200")
    expect(rowText[2]).toContain("filip: online → busy")
    // Empty state must NOT render alongside real rows.
    expect(queryByText(/No activity yet/i)).toBeNull()
  })

  it("emits a machine-readable dateTime attribute on <time>", () => {
    const { container } = render(
      <ActivityPanel entries={[entry({ id: "j1", ts: "2026-05-05T11:15:55Z" })]} />,
    )
    const timeEl = container.querySelector("time")
    expect(timeEl).not.toBeNull()
    expect(timeEl?.getAttribute("dateTime")).toBe("2026-05-05T11:15:55Z")
  })

  it("caps the rendered list at 80 rows so a chatty crew doesn't blow the panel", () => {
    const many = Array.from({ length: 200 }, (_, i) =>
      entry({ id: `j${i}`, summary: `event ${i}` }),
    )
    const { container } = render(<ActivityPanel entries={many} />)
    expect(container.querySelectorAll("li")).toHaveLength(80)
  })

  it("falls back to the raw entry_type when the type isn't in the friendly map", () => {
    const e = entry({ id: "j1", entry_type: "custom.unknown", summary: "x" })
    const { container } = render(<ActivityPanel entries={[e]} />)
    const code = container.querySelector("code")
    expect(code?.textContent).toBe("custom.unknown")
  })
})
