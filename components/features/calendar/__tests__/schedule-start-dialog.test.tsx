import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { ScheduleStartDialog, weeklyCronFor } from "../schedule-start-dialog"
import type { Pipeline } from "@/hooks/use-pipelines"

const { fetcher } = vi.hoisted(() => ({ fetcher: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetcher }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const routines = [{ slug: "recipe", name: "Daily recipe", status: "active" }] as Pipeline[]
const recipe = { definition: { inputs: [{ name: "count", label: "Item count", type: "number", required: true }] } }
const posts = () => fetcher.mock.calls.filter(([, o]) => o?.method === "POST")

beforeEach(() => fetcher.mockReset())

describe("schedule a start · one time", () => {
  it("saves the slot's local instant and typed inputs into the pending queue", async () => {
    fetcher.mockImplementation(async (_url: string, options?: RequestInit) => ({ ok: true, json: async () => (options?.method === "POST" ? { pending_id: "pending-1" } : recipe) }))
    const saved = vi.fn()
    const date = new Date(2030, 8, 10, 14, 0)
    render(<ScheduleStartDialog workspaceId="ws" routines={routines} date={date} canRepeat={false} onClose={vi.fn()} onScheduled={saved} />)
    // One eligible routine is chosen for you.
    expect(screen.getByLabelText("Routine")).toHaveValue("recipe")
    expect(screen.getByLabelText("Start time")).toHaveValue("14:00")
    fireEvent.change(await screen.findByLabelText(/Item count/), { target: { value: "7" } })
    fireEvent.click(screen.getByRole("button", { name: "Schedule" }))
    await waitFor(() => expect(saved).toHaveBeenCalledOnce())
    const [url, options] = posts()[0]
    expect(url).toBe("/api/v1/workspaces/ws/pipelines/recipe/run")
    expect(JSON.parse(options.body)).toEqual({ fire_at: date.toISOString(), inputs: { count: 7 } })
  })

  it("rejects a past time without submitting", async () => {
    fetcher.mockResolvedValue({ ok: true, json: async () => ({ definition: { inputs: [] } }) })
    render(<ScheduleStartDialog workspaceId="ws" routines={routines} date={new Date(2020, 0, 1, 9)} canRepeat={false} onClose={vi.fn()} onScheduled={vi.fn()} />)
    fireEvent.click(await screen.findByRole("button", { name: "Schedule" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("future")
    expect(posts()).toHaveLength(0)
  })

  it("keeps a refused start open and does not report it as saved", async () => {
    fetcher.mockImplementation(async (_url: string, options?: RequestInit) => ({ ok: options?.method !== "POST", json: async () => (options?.method === "POST" ? { error: "Missing required credential" } : { definition: { inputs: [] } }) }))
    const saved = vi.fn()
    render(<ScheduleStartDialog workspaceId="ws" routines={routines} date={new Date(2030, 0, 1, 9)} canRepeat={false} onClose={vi.fn()} onScheduled={saved} />)
    fireEvent.click(await screen.findByRole("button", { name: "Schedule" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("Missing required credential")
    expect(saved).not.toHaveBeenCalled()
    expect(screen.getByRole("dialog")).toBeInTheDocument()
  })
})

describe("schedule a start · repeating", () => {
  it("Repeating disabled with reason without routine.create", async () => {
    fetcher.mockResolvedValue({ ok: true, json: async () => ({ definition: { inputs: [] } }) })
    render(<ScheduleStartDialog workspaceId="ws" routines={routines} date={new Date(2030, 0, 4, 9)} canRepeat={false} onClose={vi.fn()} onScheduled={vi.fn()} />)
    await screen.findByRole("button", { name: "Schedule" })
    fireEvent.click(screen.getByRole("radio", { name: "Repeating" }))
    expect(screen.getByTestId("repeat-blocked")).toHaveTextContent("Managers and above")
    expect(screen.queryByRole("button", { name: "Add schedule" })).toBeNull()
    expect(fetcher.mock.calls.some(([u]) => String(u).includes("/pipeline-schedules"))).toBe(false)
  })

  it("previews the next three starts on the server and adds the schedule", async () => {
    fetcher.mockImplementation(async (url: string, options?: RequestInit) => ({
      ok: true,
      json: async () =>
        String(url).includes("/preview")
          ? { occurrences: ["2030-01-04T13:00:00Z", "2030-01-11T13:00:00Z", "2030-01-18T13:00:00Z"] }
          : options?.method === "POST"
            ? { id: "sched-1" }
            : { definition: { inputs: [] } },
    }))
    const saved = vi.fn()
    const date = new Date(2030, 0, 4, 14) // a Friday
    render(<ScheduleStartDialog workspaceId="ws" routines={routines} date={date} canRepeat onClose={vi.fn()} onScheduled={saved} />)
    await screen.findByRole("button", { name: "Schedule" })
    fireEvent.click(screen.getByRole("radio", { name: "Repeating" }))
    expect(await screen.findByRole("list", { name: "Next 3 starts" }, { timeout: 2000 })).toBeInTheDocument()
    const preview = fetcher.mock.calls.find(([u]) => String(u).includes("/preview"))![0] as string
    const params = new URL(preview, "https://x.test").searchParams
    expect(params.get("cron_expr")).toBe("0 14 * * 5")
    expect(params.get("count")).toBe("3")
    fireEvent.click(screen.getByRole("button", { name: "Add schedule" }))
    await waitFor(() => expect(saved).toHaveBeenCalledOnce())
    const [url, options] = posts()[0]
    expect(url).toBe("/api/v1/workspaces/ws/pipeline-schedules")
    expect(JSON.parse(options.body)).toMatchObject({ target_pipeline_slug: "recipe", cron_expr: "0 14 * * 5", enabled: true, inputs: {} })
  })

  it("suggests the clicked slot's weekday and time", () => {
    expect(weeklyCronFor(new Date(2030, 0, 6, 8, 30))).toBe("30 8 * * 0")
  })
})
