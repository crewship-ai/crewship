// The overview answers "what needs me, what broke" — and when the answer to
// both is "nothing", it says so in one line instead of four zero tiles and two
// empty cards. Events of one run fold into one row.

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import type { JournalEntry } from "@/lib/types/journal";
import { ActivityOverview } from "../activity-overview";

vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: () => <span />,
}));

let seq = 0;
function ev(over: Partial<JournalEntry> = {}): JournalEntry {
  seq += 1;
  return {
    id: `j${seq}`,
    workspace_id: "ws",
    ts: new Date(Date.now() - seq * 1000).toISOString(),
    entry_type: "pipeline.step.completed",
    severity: "info",
    actor_type: "system",
    summary: `event ${seq}`,
    ...over,
  } as JournalEntry;
}

const props = {
  rangeLabel: "Past 24 hours",
  labels: {},
  agentName: () => undefined,
  crewName: () => undefined,
  crewMeta: () => undefined,
  onSelect: vi.fn(),
  onSpineClick: vi.fn(),
  onScope: vi.fn(),
};

describe("<ActivityOverview>", () => {
  it("says a quiet window in one line, without zero tiles or empty cards", () => {
    render(<ActivityOverview {...props} entries={[ev(), ev()]} />);
    expect(screen.getByTestId("activity-quiet")).toHaveTextContent(
      /Nothing running, waiting on you or failing/,
    );
    expect(screen.queryByText("Open asks")).toBeNull();
    expect(screen.queryByText("What is broken")).toBeNull();
    expect(screen.queryByText("Waiting on you")).toBeNull();
  });

  it("keeps the tiles and the cards when something is broken", () => {
    render(
      <ActivityOverview
        {...props}
        entries={[
          ev({
            severity: "error",
            entry_type: "pipeline.run.failed",
            summary: "boom",
          }),
          ev(),
        ]}
      />,
    );
    expect(screen.queryByTestId("activity-quiet")).toBeNull();
    expect(screen.getByText("What is broken")).toBeInTheDocument();
    // The empty half is one line, not a card.
    expect(screen.queryByText("Open asks")).toBeNull();
    expect(screen.getByTestId("activity-no-asks")).toBeInTheDocument();
  });

  it("folds one run's consecutive events into a row with a count", () => {
    const run = { run_id: "run_abc", pipeline_slug: "coolify-ingest" };
    render(
      <ActivityOverview
        {...props}
        entries={[
          ev({ summary: "newest", payload: run }),
          ev({ payload: run }),
          ev({ payload: run }),
          ev({ summary: "other" }),
        ]}
      />,
    );
    const latest = screen.getByText("newest").closest("button")!;
    expect(latest).toHaveTextContent("+2 more");
    expect(screen.queryByText("event 2")).toBeNull();
    expect(screen.getByText("other")).toBeInTheDocument();
  });

  it("starts the sub-line with its first part, not a dangling separator", () => {
    const run = { run_id: "run_abc", pipeline_slug: "coolify-ingest" }
    render(
      <ActivityOverview
        {...props}
        crewName={(id) => (id ? "Infra" : undefined)}
        entries={[ev({ summary: "crew only", crew_id: "c1" }), ev({ summary: "spine only", entry_type: "pipeline.run.completed", payload: run })]}
      />,
    )
    expect(screen.getByText("crew only").closest("button")!.textContent).not.toContain("· Infra")
    expect(screen.getByText("spine only").closest("button")!.textContent).not.toMatch(/spine only›/)
  })

  it("does not print a zero cost", () => {
    render(
      <ActivityOverview
        {...props}
        entries={[
          ev({ summary: "free", payload: { cost_usd: 0, duration_ms: 346 } }),
        ]}
      />,
    );
    const row = screen.getByText("free").closest("button")!;
    expect(row).not.toHaveTextContent("$0.000");
  });
});
