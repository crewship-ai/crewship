// What the routine detail says about self-composition.
//
// Three facts landed in the product with no way to see any of them, and each
// one is a question the page cannot otherwise answer:
//
//   "What starts this?"      a routine a rule can fire, on a page that lists
//                            only cron schedules, reads as manual-or-cron. The
//                            reader concludes the wrong thing and is right to.
//   "Why did THIS run?"      a composed run looks identical to one somebody
//                            pressed a button for.
//   "What does it write?"    a `crewship` step is the difference between a
//                            routine that reads the board and one that edits
//                            it. Access is the card that answers "what can
//                            this reach", and it was silent about the reach
//                            that points back at us.
//
// The first test is the one that has to hold hardest: a routine with no
// automations must render NOTHING extra. Empty scaffolding — a card headed
// "Automations" saying "none" — costs every routine in the workspace a
// permanent slot for a feature most of them never use.

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

import type { Automation } from "@/lib/automations"
import type { RoutineDetail } from "../routines-detail-panel"

const h = vi.hoisted(() => ({
  automations: [] as unknown[],
  records: [] as unknown[],
}))

// Spreads the rest of the props: the real Link forwards data-* to the anchor,
// and a mock that drops them silently hides every testid put on a link.
vi.mock("next/link", () => ({
  default: ({
    children,
    href,
    ...rest
  }: {
    children: React.ReactNode
    href: string
  } & React.AnchorHTMLAttributes<HTMLAnchorElement>) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}))

// The graph and the code editor are heavy, unrelated, and mocked everywhere
// else this card is exercised.
vi.mock("../routine-definition-canvas", () => ({
  RoutineDefinitionCanvas: () => <div data-testid="canvas" />,
}))
vi.mock("../routine-editor-tab", () => ({ RoutineEditorTab: () => <div /> }))
vi.mock("../routine-schedules-tab", () => ({ RoutineSchedulesTab: () => <div /> }))
vi.mock("../routine-webhooks-tab", () => ({ RoutineWebhooksTab: () => <div /> }))
vi.mock("../routine-versions-tab", () => ({ RoutineVersionsTab: () => <div /> }))
vi.mock("../routine-runs-tab", () => ({ RoutineRunsTab: () => <div /> }))
vi.mock("../routine-budget-card", () => ({ RoutineBudgetCard: () => <div /> }))
vi.mock("../routine-reach-card", () => ({ RoutineReachCard: () => <div /> }))

vi.mock("@/hooks/use-pipeline-run-records", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/use-pipeline-run-records")>()),
  usePipelineRunRecords: () => ({
    records: h.records,
    legacy: false,
    loading: false,
    error: null,
    refresh: vi.fn(),
  }),
}))
vi.mock("@/hooks/use-pipeline-schedules", () => ({
  usePipelineSchedules: () => ({ schedules: [], loading: false, error: null, refresh: vi.fn() }),
}))
vi.mock("@/hooks/use-automations", () => ({
  useAutomations: () => ({ automations: h.automations, loading: false, error: null }),
}))

import { RoutineCardDetail } from "../routine-card-detail"

function routine(over: Partial<RoutineDetail> = {}): RoutineDetail {
  return {
    id: "pipe-1",
    slug: "daily-triage",
    name: "Daily triage",
    dsl_version: "1.0",
    definition: { steps: [{ id: "s1", type: "agent_run" }] },
    definition_hash: "abc123",
    ephemeral: false,
    workspace_visible: true,
    invocation_count: 0,
    authored_via: "ui",
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    ...over,
  } as RoutineDetail
}

function rule(over: Partial<Automation> = {}): Automation {
  return {
    id: "a1",
    workspace_id: "ws1",
    name: "Triage new bugs",
    enabled: true,
    event_type: "mission.status_change",
    matcher: {},
    action_kind: "routine",
    action: { routine_slug: "daily-triage" },
    debounce_seconds: 10,
    max_per_hour: 60,
    created_at: "2026-08-07T10:00:00Z",
    updated_at: "2026-08-07T10:00:00Z",
    ...over,
  }
}

function renderCard(r: RoutineDetail = routine()) {
  return render(<RoutineCardDetail routine={r} workspaceId="ws1" onChanged={vi.fn()} />)
}

beforeEach(() => {
  h.automations = []
  h.records = []
})

/** Opens the Automations pane of the Triggers card. */
function openAutomations() {
  fireEvent.click(screen.getByRole("button", { name: /^automations$/i }))
}

describe("automations bound to a routine", () => {
  it("renders nothing extra when no automation targets this routine", () => {
    h.automations = [rule({ id: "other", action: { routine_slug: "some-other-routine" } })]
    renderCard()

    // No pill, no pane, and — the point — no way to reach one. A routine with
    // no rules must not pay a permanent slot for the feature.
    expect(screen.queryByTestId("routine-automations-pill")).toBeNull()
    expect(screen.queryByRole("button", { name: /^automations$/i })).toBeNull()
    expect(screen.queryByTestId("routine-automations")).toBeNull()
  })

  it("says a rule can start this routine without being asked", () => {
    // The fact has to be legible at a glance. Behind a click it is a fact the
    // page technically contains and nobody reads.
    h.automations = [rule({ id: "a1" }), rule({ id: "a2" })]
    renderCard()

    expect(screen.getByTestId("routine-automations-pill")).toHaveTextContent("2 automations")
  })

  it("names each rule and the event it watches", () => {
    h.automations = [
      rule({ id: "a1", name: "Triage new bugs", event_type: "mission.status_change" }),
      rule({ id: "a2", name: "Escalate stalls", event_type: "assignment.failed" }),
    ]
    renderCard()
    openAutomations()

    const panel = screen.getByTestId("routine-automations")
    expect(panel).toHaveTextContent("Triage new bugs")
    expect(panel).toHaveTextContent("mission.status_change")
    expect(panel).toHaveTextContent("Escalate stalls")
    expect(panel).toHaveTextContent("assignment.failed")
    expect(screen.getByTestId("routine-automations-count")).toHaveTextContent("2")
  })

  it("marks a disabled rule as disabled rather than hiding it", () => {
    // A rule that is switched off is exactly what answers "why did nothing
    // happen". Hiding it makes the page complicit in the confusion.
    h.automations = [rule({ id: "a1", name: "Triage new bugs", enabled: false })]
    renderCard()
    openAutomations()

    expect(screen.getByTestId("automation-row-a1")).toHaveTextContent(/disabled/i)
  })

  it("points at where automations are managed", () => {
    h.automations = [rule()]
    renderCard()
    openAutomations()
    expect(screen.getByTestId("routine-automations")).toHaveTextContent("crewship automation")
  })
})

describe("chain depth on composed runs", () => {
  const baseRun = {
    id: "run-1",
    pipeline_id: "pipe-1",
    pipeline_slug: "daily-triage",
    status: "completed",
    mode: "run",
    started_at: "2026-08-07T09:00:00Z",
    cost_usd: 0,
    duration_ms: 1200,
    triggered_via: "manual",
    chain_depth: 0,
  }

  it("says nothing about chains for a run somebody started", () => {
    h.records = [baseRun]
    renderCard()
    expect(screen.queryByTestId("run-chain-depth-run-1")).toBeNull()
  })

  it("marks a composed run with how deep in the chain it sits", () => {
    h.records = [{ ...baseRun, triggered_via: "call_pipeline", chain_depth: 2 }]
    renderCard()
    expect(screen.getByTestId("run-chain-depth-run-1")).toHaveTextContent("2")
  })

  it("names the rule behind a run the enum calls a schedule", () => {
    // triggered_via is "schedule" for every deferred run, automations
    // included. Printing it verbatim would report a cron.
    h.records = [
      {
        ...baseRun,
        triggered_via: "schedule",
        automation_name: "Triage new bugs",
        chain_depth: 1,
      },
    ]
    renderCard()
    const row = screen.getByTestId("run-row-run-1")
    expect(row).toHaveTextContent("automation")
    expect(row).toHaveTextContent("Triage new bugs")
    expect(row).not.toHaveTextContent("schedule")
  })
})

describe("what a routine writes back to Crewship", () => {
  it("stays quiet for a routine with no crewship steps", () => {
    renderCard()
    expect(screen.queryByTestId("routine-crewship-actions")).toBeNull()
  })

  it("names the verbs a routine acts on the board with", () => {
    renderCard(
      routine({
        definition: {
          steps: [
            { id: "s1", type: "agent_run" },
            { id: "s2", type: "crewship", action: "issue.create" },
          ],
        },
      }),
    )
    const panel = screen.getByTestId("routine-crewship-actions")
    expect(panel).toHaveTextContent("issue.create")
  })

  it("finds a crewship step buried in a foreach body", () => {
    renderCard(
      routine({
        definition: {
          steps: [
            {
              id: "fan",
              type: "foreach",
              foreach: {
                items: "{{ steps.report.output }}",
                steps: [{ id: "file", type: "crewship", action: "issue.create" }],
              },
            },
          ],
        },
      }),
    )
    expect(screen.getByTestId("routine-crewship-actions")).toHaveTextContent("issue.create")
  })
})
