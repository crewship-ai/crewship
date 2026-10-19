import { cleanup, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { JournalLookupValue } from "@/hooks/use-journal-lookup"

const mock = vi.hoisted(() => ({ fetch: vi.fn<(url: string) => Promise<Response>>() }))

vi.mock("@/lib/api-fetch", () => ({ apiFetch: (url: string) => mock.fetch(url) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: () => {} }))

const lookup: JournalLookupValue = {
  crews: new Map([
    ["c1", { id: "c1", name: "Research", slug: "research", icon: null, color: "teal" }],
    ["c2", { id: "c2", name: "Code review", slug: "code", icon: null, color: "blue" }],
  ]),
  agents: new Map(),
  missions: new Map(),
  loading: false,
  refresh: vi.fn(),
}
vi.mock("@/hooks/use-journal-lookup", () => ({ useJournalLookup: () => lookup }))

import { ActivitySpendView } from "../activity-spend"

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })

const DAY = {
  window: "24h",
  total_cost_usd: 1.65,
  by_agent: [
    { date: "2026-10-17", crew_id: "c1", agent_id: "a1", cost_usd: 1.0, call_count: 400 },
    { date: "2026-10-17", crew_id: "c2", agent_id: "a2", cost_usd: 0.65, call_count: 12 },
  ],
  by_routine: [],
  top_routines: [],
  top_runs: [
    { kind: "run", id: "run_139", label: "CRW-139 Rotate certs", cost_usd: 0.64 },
    { kind: "run", id: "run_140", label: "Nightly triage", cost_usd: 0.31 },
  ],
  truncated: false,
}
const MONTH = {
  rows: [
    { crew_id: "c1", cost_usd: 9.8, call_count: 900, input_tokens: 1, output_tokens: 1 },
    { crew_id: "c2", cost_usd: 18.9, call_count: 1200, input_tokens: 1, output_tokens: 1 },
    { crew_id: "", cost_usd: 4, call_count: 20, input_tokens: 1, output_tokens: 1 },
  ],
}

function route(day: unknown = DAY, month: unknown = MONTH) {
  mock.fetch.mockImplementation(async (url: string) => {
    if (url.startsWith("/api/v1/journal/spend")) return day instanceof Response ? day : json(day)
    if (url.startsWith("/api/v1/paymaster/spend/by-crew")) return month instanceof Response ? month : json(month)
    return json({}, 404)
  })
}

const monthName = new Date().toLocaleDateString("en-GB", { month: "long" })
const kpi = (label: string) => screen.getByText(label, { selector: "small" }).closest<HTMLElement>("[data-kpi]")!

beforeEach(() => {
  mock.fetch.mockReset()
  route()
})
afterEach(() => cleanup())

describe("ActivitySpendView (J04, v67 spendView)", () => {
  it("names the page with one h1 and says what it shows", async () => {
    render(<ActivitySpendView workspaceId="ws" />)
    expect(screen.getByRole("heading", { level: 1, name: "Spend" })).toBeVisible()
    expect(screen.getByText("What the work cost, by crew and by piece of work")).toBeVisible()
  })

  it("draws the KPI row: spend 24 h, spend this month, model calls 24 h, most expensive", async () => {
    render(<ActivitySpendView workspaceId="ws" />)
    await waitFor(() => expect(kpi("Spend · 24 h")).toHaveTextContent("$1.65"))
    expect(kpi(`Spend · ${monthName}`)).toHaveTextContent("$32.70")
    expect(kpi("Model calls · 24 h")).toHaveTextContent("412")
    expect(kpi("Most expensive")).toHaveTextContent("$0.64")
    expect(kpi("Most expensive")).toHaveTextContent("CRW-139 Rotate certs")
    // No workspace budget exists yet: nothing pretends there is one.
    expect(screen.queryByText(/budget/i)).toBeNull()
  })

  it("asks for the last 24 hours and for this month by crew, whatever else is chosen", async () => {
    render(<ActivitySpendView workspaceId="ws" />)
    await waitFor(() => expect(mock.fetch).toHaveBeenCalledTimes(2))
    const urls = mock.fetch.mock.calls.map((c) => c[0])
    expect(urls.find((u) => u.startsWith("/api/v1/journal/spend"))).toContain("window=24h")
    const start = new Date()
    start.setDate(1)
    start.setHours(0, 0, 0, 0)
    expect(urls.find((u) => u.startsWith("/api/v1/paymaster/spend/by-crew"))).toContain(`since=${encodeURIComponent(start.toISOString())}`)
    // The window is fixed (v67): no window picker of its own.
    expect(screen.queryByRole("combobox")).toBeNull()
  })

  it("shows spend by crew as bars, most expensive first, with unattributed spend named", async () => {
    render(<ActivitySpendView workspaceId="ws" />)
    const card = (await screen.findByRole("heading", { name: `By crew · ${monthName}` })).closest("section")!
    const rows = await within(card).findAllByRole("listitem")
    expect(rows.map((r) => r.textContent)).toEqual(["Code review$18.90", "Research$9.80", "No crew$4.00"])
    const bar = (r: HTMLElement) => r.querySelector<HTMLElement>("[data-bar]")!.style.width
    // Share of the month: 18.90 of 32.70.
    expect(bar(rows[0])).toBe("58%")
    expect(bar(rows[1])).toBe("30%")
  })

  it("lists the most expensive work of the last 24 hours, each opening in Activity", async () => {
    render(<ActivitySpendView workspaceId="ws" />)
    const card = (await screen.findByRole("heading", { name: "Most expensive work · 24 h" })).closest("section")!
    const link = await within(card).findByRole("link", { name: /CRW-139 Rotate certs/ })
    expect(link).toHaveAttribute("href", "/activity?run=run_139")
    expect(link).toHaveTextContent("$0.64")
    expect(within(card).getAllByRole("link")).toHaveLength(2)
  })

  it("ends with the admins-only estimate note", () => {
    render(<ActivitySpendView workspaceId="ws" />)
    expect(screen.getByText("Admins only. On subscription plans the cost is an estimate, not money spent.")).toBeVisible()
  })

  it("one failed source keeps the other part on screen", async () => {
    route(DAY, json({ error: "boom" }, 503))
    render(<ActivitySpendView workspaceId="ws" />)
    await waitFor(() => expect(kpi("Spend · 24 h")).toHaveTextContent("$1.65"))
    expect(await screen.findByText("Could not load spend by crew")).toBeVisible()
    expect(screen.getByRole("link", { name: /CRW-139 Rotate certs/ })).toBeVisible()
  })

  // #3590: below ADMIN the server answers 403. That is a quiet note, not an
  // error card or a toast.
  it("a 403 reads as one quiet note, not as failed loads", async () => {
    route(json({ error: "Spend requires OWNER or ADMIN" }, 403), json({ error: "Spend requires OWNER or ADMIN" }, 403))
    render(<ActivitySpendView workspaceId="ws" />)
    expect(await screen.findByText("Spend is unavailable: only workspace owners and admins can see costs.")).toBeVisible()
    expect(screen.queryByText("Could not load the last 24 hours")).toBeNull()
    expect(screen.queryByText("Could not load spend by crew")).toBeNull()
    expect(screen.queryByRole("alert")).toBeNull()
  })
})
