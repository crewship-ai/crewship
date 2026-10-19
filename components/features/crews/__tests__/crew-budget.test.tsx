import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"

vi.mock("@/lib/api-fetch", async (orig) => ({ ...(await orig<typeof import("@/lib/api-fetch")>()), apiFetch: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

import { apiFetch } from "@/lib/api-fetch"
import { CrewBudgetCard } from "../crew-budget"

type Budget = { crew_id: string; window: string; limit_usd: number | null; mode?: string; spent_usd: number; state: string }
const none: Budget = { crew_id: "c1", window: "month", limit_usd: null, spent_usd: 12.5, state: "none" }
const res = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body }) as unknown as Response

let server: Budget
let calls: { method: string; body?: string }[]
beforeEach(() => {
  server = { ...none }
  calls = []
  vi.mocked(apiFetch).mockImplementation(async (url, init) => {
    const method = (init as RequestInit | undefined)?.method ?? "GET"
    const body = (init as RequestInit | undefined)?.body as string | undefined
    calls.push({ method, body })
    if (!String(url).includes("/budget")) return res({}, 404)
    if (method === "PUT") {
      const b = JSON.parse(body!)
      server = { ...server, limit_usd: b.limit_usd, mode: b.mode ?? "tiered", state: server.spent_usd >= b.limit_usd ? "exceeded" : server.spent_usd >= b.limit_usd * 0.8 ? "warn" : "ok" }
    }
    if (method === "DELETE") server = { ...none }
    return res(server)
  })
})
afterEach(cleanup)

describe("CrewBudgetCard", () => {
  it("shows the month's spend and 'No limit' before one is set", async () => {
    render(<CrewBudgetCard workspaceId="ws" crewId="c1" />)
    expect(await screen.findByText(/Spend this month \$12\.50/)).toBeInTheDocument()
    expect(screen.getByText("No limit")).toBeInTheDocument()
    expect(screen.getByText(/Soft warns at 80 %, hard stops new runs at 100 %/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Remove limit" })).toBeNull()
  })

  it("sets a monthly limit and says how close the crew is", async () => {
    render(<CrewBudgetCard workspaceId="ws" crewId="c1" />)
    await screen.findByText("No limit")
    fireEvent.change(screen.getByLabelText("Monthly limit in US dollars"), { target: { value: "15" } })
    fireEvent.click(screen.getByRole("button", { name: "Set limit" }))
    await screen.findByText("$15.00")
    expect(JSON.parse(calls.find((c) => c.method === "PUT")!.body!)).toEqual({ limit_usd: 15 })
    expect(screen.getByText(/83 % used/)).toBeInTheDocument()
    expect(screen.getByRole("status")).toHaveTextContent(/Warning/)
  })

  it("refuses a limit that is not a positive number without asking the server", async () => {
    render(<CrewBudgetCard workspaceId="ws" crewId="c1" />)
    await screen.findByText("No limit")
    for (const bad of ["", "0", "-3", "lots"]) {
      fireEvent.change(screen.getByLabelText("Monthly limit in US dollars"), { target: { value: bad } })
      fireEvent.click(screen.getByRole("button", { name: "Set limit" }))
    }
    expect(calls.filter((c) => c.method === "PUT")).toHaveLength(0)
    expect(screen.getByRole("alert")).toHaveTextContent(/greater than 0/)
  })

  it("removes the limit", async () => {
    server = { ...none, limit_usd: 20, mode: "tiered", state: "ok" }
    render(<CrewBudgetCard workspaceId="ws" crewId="c1" />)
    fireEvent.click(await screen.findByRole("button", { name: "Remove limit" }))
    await screen.findByText("No limit")
    expect(calls.some((c) => c.method === "DELETE")).toBe(true)
  })

  it("says so when the budget cannot be read, and offers nothing to save", async () => {
    vi.mocked(apiFetch).mockImplementation(async () => res({}, 500))
    render(<CrewBudgetCard workspaceId="ws" crewId="c1" />)
    expect(await screen.findByText(/could not be loaded/i)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Set limit" })).toBeNull()
  })

  it("keeps the person where they were when saving fails", async () => {
    render(<CrewBudgetCard workspaceId="ws" crewId="c1" />)
    await screen.findByText("No limit")
    vi.mocked(apiFetch).mockImplementationOnce(async () => res({ error: "Forbidden" }, 403))
    fireEvent.change(screen.getByLabelText("Monthly limit in US dollars"), { target: { value: "30" } })
    fireEvent.click(screen.getByRole("button", { name: "Set limit" }))
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(/could not be saved/i))
    expect(screen.getByLabelText("Monthly limit in US dollars")).toHaveValue("30")
  })
})
