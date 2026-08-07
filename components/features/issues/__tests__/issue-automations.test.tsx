// What the issue detail says about the rules watching it.
//
// An issue is now something a routine can WRITE to, and something whose own
// changes can start a routine. Neither direction was visible: the page showed
// a bound routine and a "Starting this issue runs that routine" sentence, and
// said nothing about the rules that would run it without anybody starting
// anything.
//
// The load-bearing test here is the exclusion one. Listing a rule whose
// matcher provably cannot match this issue is worse than listing none: it
// tells the reader a change here will set something off, they plan around
// that, and nothing fires.

import * as React from "react"
import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

import type { Automation } from "@/lib/automations"
import type { Mission } from "@/lib/types/mission"
import { IssueCardDetail } from "../issue-card-detail"

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
vi.mock("@/components/features/issues/tiptap-editor", () => ({
  TiptapEditor: () => <div data-testid="tiptap" />,
}))

function issue(over: Partial<Mission> = {}): Mission {
  return {
    id: "m-1",
    crew_id: "crew-1",
    identifier: "ENG-15",
    title: "Login times out",
    status: "IN_PROGRESS",
    priority: "high",
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-06T00:00:00Z",
    ...over,
  } as Mission
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
    action: { routine_slug: "triage" },
    debounce_seconds: 10,
    max_per_hour: 60,
    created_at: "2026-08-07T10:00:00Z",
    updated_at: "2026-08-07T10:00:00Z",
    ...over,
  }
}

function renderIssue(props: Partial<React.ComponentProps<typeof IssueCardDetail>> = {}) {
  return render(
    <IssueCardDetail
      issue={issue()}
      comments={[]}
      activities={[]}
      relations={[]}
      {...props}
    />,
  )
}

describe("automations that could react to an issue", () => {
  it("renders nothing extra when no rule is in scope", () => {
    renderIssue({ automations: [] })
    expect(screen.queryByTestId("issue-automations")).toBeNull()
  })

  it("does NOT show an automation whose matcher excludes this issue", () => {
    // Right event type, wrong issue. Naming it here would promise a reaction
    // the matcher forbids.
    renderIssue({ automations: [rule({ id: "a1", matcher: { mission_ids: ["m-999"] } })] })
    expect(screen.queryByTestId("issue-automations")).toBeNull()
    expect(screen.queryByTestId("automation-row-a1")).toBeNull()
  })

  it("does NOT show an automation scoped to another crew", () => {
    renderIssue({ automations: [rule({ id: "a1", matcher: { crew_ids: ["crew-other"] } })] })
    expect(screen.queryByTestId("automation-row-a1")).toBeNull()
  })

  it("shows a rule that names this issue, with the event it watches", () => {
    renderIssue({
      automations: [
        rule({ id: "a1", name: "Triage new bugs", matcher: { mission_ids: ["m-1"] } }),
      ],
    })
    const panel = screen.getByTestId("issue-automations")
    expect(panel).toHaveTextContent("Triage new bugs")
    expect(panel).toHaveTextContent("mission.status_change")
  })

  it("shows a crew-scoped rule on an issue in that crew", () => {
    renderIssue({ automations: [rule({ id: "a1", matcher: { crew_ids: ["crew-1"] } })] })
    expect(screen.getByTestId("automation-row-a1")).toBeInTheDocument()
  })

  it("does not claim a rule WILL fire — only that it could", () => {
    // agent_ids / severities / payload_equals narrow further and cannot be
    // decided from an issue. The card has to say so rather than overpromise.
    renderIssue({ automations: [rule({ id: "a1" })] })
    expect(screen.getByTestId("issue-automations")).toHaveTextContent(/could|may/i)
  })
})

describe("how the bound routine's runs were started", () => {
  const run = (over: Record<string, unknown> = {}) => ({
    id: "run-1",
    pipeline_id: "pipe-1",
    pipeline_slug: "triage",
    status: "completed",
    mode: "run",
    started_at: "2026-08-07T09:00:00Z",
    cost_usd: 0,
    duration_ms: 900,
    triggered_via: "manual",
    ...over,
  })

  it("says nothing when the issue has no bound routine", () => {
    renderIssue({ routineRuns: [run()] })
    expect(screen.queryByTestId("issue-routine-provenance")).toBeNull()
  })

  it("says nothing when the bound routine has never run", () => {
    renderIssue({
      issue: issue({ routine_slug: "triage", routine_name: "Triage" }),
      routineRuns: [],
    })
    expect(screen.queryByTestId("issue-routine-provenance")).toBeNull()
  })

  it("separates a human start from a schedule from a rule", () => {
    renderIssue({
      issue: issue({ routine_slug: "triage", routine_name: "Triage" }),
      routineRuns: [
        run({ id: "r1", triggered_via: "issue", triggered_by_id: "ENG-15" }),
        run({ id: "r2", triggered_via: "manual" }),
        run({ id: "r3", triggered_via: "schedule" }),
        // The trap: an automation-fired run is stored as "schedule". Counted
        // as one would tell the reader a cron ran it.
        run({ id: "r4", triggered_via: "schedule", automation_name: "Triage new bugs" }),
      ],
    })
    const panel = screen.getByTestId("issue-routine-provenance")
    expect(panel).toHaveTextContent("issue")
    expect(panel).toHaveTextContent("manual")
    expect(panel).toHaveTextContent("schedule")
    expect(panel).toHaveTextContent("automation")
    expect(panel).toHaveTextContent("Triage new bugs")
  })

  it("counts repeats of the same source rather than listing each run", () => {
    renderIssue({
      issue: issue({ routine_slug: "triage", routine_name: "Triage" }),
      routineRuns: [
        run({ id: "r1", triggered_via: "schedule" }),
        run({ id: "r2", triggered_via: "schedule" }),
        run({ id: "r3", triggered_via: "schedule" }),
      ],
    })
    expect(screen.getByTestId("issue-routine-source-schedule")).toHaveTextContent("3")
  })
})
