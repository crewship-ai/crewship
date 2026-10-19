// #3590: below ADMIN the server leaves cost fields out of mixed responses
// (absent, not 0). Every surface a member can open must stay calm: no
// "$0.00", no "none reported", no "$NaN", no crash.

import * as React from "react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import { RoutineBudgetCard } from "../routine-budget-card"
import { RoutineRunPeek } from "../routine-run-peek"
import { RoutinesOverviewBoard } from "../routines-overview-board"
import { RunPreflight, type ChatRunPreflight } from "@/components/features/chat/run-preflight"
import { routineHealthRows } from "@/lib/routine-health"
import { readComparisonResult } from "@/lib/routine-comparison"
import { isCostShown } from "@/lib/cost-visibility"
import type { RoutineBudget } from "@/hooks/use-routine-budget"
import type { PipelineRunRecord } from "@/hooks/use-pipeline-run-records"

const h = vi.hoisted(() => ({ budget: null as RoutineBudget | null, fetch: vi.fn() }))
vi.mock("@/hooks/use-routine-budget", () => ({
  useRoutineBudget: () => ({ budget: h.budget, loading: false, error: null, refresh: vi.fn(), setBudget: vi.fn() }),
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/lib/api-fetch", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api-fetch")>()),
  apiFetch: h.fetch,
}))

afterEach(() => {
  cleanup()
  h.fetch.mockReset()
})

const noMoney = (text: string | null | undefined) => {
  expect(text ?? "").not.toMatch(/\$|NaN|none reported/)
}

describe("isCostShown", () => {
  it("is true only for a real number", () => {
    expect(isCostShown(0)).toBe(true)
    expect(isCostShown(1.5)).toBe(true)
    expect(isCostShown(undefined)).toBe(false)
    expect(isCostShown(null)).toBe(false)
    expect(isCostShown(Number.NaN)).toBe(false)
  })
})

describe("routine budget card with amounts withheld", () => {
  for (const [name, budget, want] of [
    ["over", { slug: "r", has_budget: true, month: "2026-10", over_budget: true }, "Over budget this month"],
    ["within", { slug: "r", has_budget: true, month: "2026-10" }, "Within budget this month"],
    ["none", { slug: "r", has_budget: false, month: "2026-10" }, "No budget set"],
  ] as const) {
    it(`says ${name} and offers no editor`, () => {
      h.budget = budget as RoutineBudget
      render(<RoutineBudgetCard workspaceId="ws" slug="r" />)
      const state = screen.getByTestId("routine-budget-state")
      expect(state).toHaveTextContent(want)
      expect(state).toHaveTextContent(/owners and admins/)
      noMoney(document.body.textContent)
      expect(screen.queryByRole("button", { name: /budget/i })).not.toBeInTheDocument()
    })
  }
})

describe("routine run peek with cost withheld", () => {
  it("leaves the Cost row out", () => {
    const run: PipelineRunRecord = {
      id: "run-1", pipeline_id: "p", pipeline_slug: "weekly", status: "completed", mode: "run",
      started_at: "2026-10-15T08:00:00Z", duration_ms: 1000, triggered_via: "schedule", pipeline_version: 1,
    } as PipelineRunRecord
    render(<RoutineRunPeek workspaceId="ws" runs={[run]} runId="run-1" onChange={() => {}} onClose={() => {}} onOpen={() => {}} />)
    const peek = screen.getByTestId("run-peek")
    expect(within(peek).queryByText("Cost")).not.toBeInTheDocument()
    noMoney(peek.textContent)
  })
})

describe("routines overview board with spend withheld", () => {
  it("drops the Spend tile instead of reading $0.00", () => {
    render(
      <RoutinesOverviewBoard
        routines={[]}
        activeRuns={[]}
        waitpoints={[]}
        schedules={[]}
        pendingStarts={[]}
        kpis={{ total: 3, successPct: 100 }}
        coveredSince={null}
        spend={{ month: "2026-10", routines: [{ slug: "weekly", has_budget: true, over_budget: true }] }}
        role="MEMBER"
        now={new Date("2026-10-17T08:00:00Z")}
        onDecide={vi.fn(async () => true)}
        onStop={vi.fn(async () => true)}
        onOpenRun={vi.fn()}
        onSelect={vi.fn()}
      />,
    )
    const items = within(screen.getByRole("list", { name: "Routine figures" })).getAllByRole("listitem")
    expect(items.map((li) => li.textContent).join(" ")).not.toMatch(/Spend/)
    noMoney(screen.getByRole("list", { name: "Routine figures" }).textContent)
  })
})

describe("routine health with spend withheld", () => {
  it("reports no monthly spend rather than $0", () => {
    const rows = routineHealthRows(
      [{ slug: "weekly", name: "Weekly" }] as never,
      [],
      { month: "2026-10", routines: [{ slug: "weekly", has_budget: true }] },
    )
    expect(rows[0].spent).toBeNull()
  })
})

describe("routine comparison with cost withheld", () => {
  it("accepts the recorded result and keeps cost unknown", () => {
    const job = { run_id: "r1", config: { version: { definition_hash: "h", version: 2 } } } as never
    const got = readComparisonResult(
      { id: "r1", workspace_id: "ws", definition_hash: "h", pipeline_version: 2, status: "completed", output: "ok", duration_ms: 5 },
      job,
      "ws",
    )
    expect(got.cost_usd).toBeNull()
  })
})

describe("chat run preflight with budget amounts withheld", () => {
  it("lists the budget state without dollar figures", async () => {
    const body: ChatRunPreflight = {
      scope: "configured_chat_agent", agent_id: "a", model: "m",
      payer: { state: "ready", category: "connected_provider_account", label: "Workspace payer" },
      budgets: [{ id: "b", scope: "crew", window: "month", mode: "hard", state: "ok" }],
      gate: { configured_mode: "sync", predicted_mode: "none", rule_matches: false, blocking: false, reason: "" },
      estimate_usd: null, estimate_state: "unknown", constraints: [],
    }
    h.fetch.mockResolvedValue(new Response(JSON.stringify(body)))
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
    render(<QueryClientProvider client={client}><RunPreflight chatId="c" workspaceId="ws" userId="u" enabled /></QueryClientProvider>)
    const item = await screen.findByText(/budget per month/)
    expect(item).toHaveTextContent("crew · budget per month · hard · ok")
    noMoney(item.textContent)
  })
})
