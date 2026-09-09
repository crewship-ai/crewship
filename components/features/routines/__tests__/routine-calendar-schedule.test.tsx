import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { RoutineCalendarSchedule } from "../routine-calendar-schedule"
import type { Pipeline } from "@/hooks/use-pipelines"
const { fetcher } = vi.hoisted(() => ({ fetcher: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetcher }))
vi.mock("sonner", () => ({ toast: { success: vi.fn() } }))
const routines = [{ slug: "recipe", name: "Daily recipe", status: "active" }] as Pipeline[]
beforeEach(() => fetcher.mockReset())
describe("calendar scheduling", () => {
  it("saves the selected local instant and typed inputs into the durable pending queue", async () => {
    fetcher.mockImplementation(async (_url: string, options?: RequestInit) => ({ ok: true, json: async () => options?.method === "POST" ? { pending_id: "pending-1" } : { definition: { inputs: [{ name: "count", label: "Item count", type: "number", required: true }] } } }))
    const saved = vi.fn(), date = new Date(2030, 8, 10, 9, 30)
    render(<RoutineCalendarSchedule workspaceId="ws" routines={routines} date={date} onClose={vi.fn()} onScheduled={saved} />)
    fireEvent.change(screen.getByLabelText("Routine"), { target: { value: "recipe" } })
    fireEvent.change(await screen.findByLabelText(/Item count/), { target: { value: "7" } })
    fireEvent.click(screen.getByRole("button", { name: "Schedule routine" }))
    await waitFor(() => expect(saved).toHaveBeenCalledOnce())
    const call = fetcher.mock.calls.find(([, options]) => options?.method === "POST")!
    expect(JSON.parse(call[1].body)).toEqual({ fire_at: date.toISOString(), inputs: { count: 7 } })
  })
  it("rejects past times without submitting", async () => {
    fetcher.mockResolvedValue({ ok: true, json: async () => ({ definition: { inputs: [] } }) })
    render(<RoutineCalendarSchedule workspaceId="ws" routines={routines} date={new Date(2020, 0, 1, 9)} onClose={vi.fn()} onScheduled={vi.fn()} />)
    fireEvent.change(screen.getByLabelText("Routine"), { target: { value: "recipe" } })
    fireEvent.click(await screen.findByRole("button", { name: "Schedule routine" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("future")
    expect(fetcher.mock.calls.some(([, options]) => options?.method === "POST")).toBe(false)
  })
  it("keeps a refused schedule open and does not report it as saved", async () => {
    fetcher.mockImplementation(async (_url: string, options?: RequestInit) => ({ ok: options?.method !== "POST", json: async () => options?.method === "POST" ? { error: "Missing required credential" } : { definition: { inputs: [] } } }))
    const saved = vi.fn()
    render(<RoutineCalendarSchedule workspaceId="ws" routines={routines} date={new Date(2030, 0, 1, 9)} onClose={vi.fn()} onScheduled={saved} />)
    fireEvent.change(screen.getByLabelText("Routine"), { target: { value: "recipe" } })
    fireEvent.click(await screen.findByRole("button", { name: "Schedule routine" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("Missing required credential")
    expect(saved).not.toHaveBeenCalled()
    expect(screen.getByRole("dialog")).toBeInTheDocument()
  })

})
