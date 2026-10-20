import { useState } from "react"
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { CalendarPeek, comesFrom, type CalendarPermissions } from "../calendar-peek"
import type { CalendarItem } from "@/lib/calendar/model"
import { ROUTINES, at, entry, items } from "./fixtures"

const { fetcher, push, toast } = vi.hoisted(() => ({
  fetcher: vi.fn(),
  push: vi.fn(),
  toast: { success: vi.fn(), error: vi.fn() },
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetcher }))
vi.mock("@/lib/toast", () => ({ toast }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("next/link", () => ({ default: ({ children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => <a {...props}>{children}</a> }))

const list = items([
  entry({ id: "plan", at: at(2030, 10, 15, 8), slug: "scan", timezone: "UTC" }),
  entry({ id: "once", kind: "pending", at: at(2030, 10, 16, 14), slug: "scan" }),
  entry({ id: "r1", kind: "run", status: "waiting", at: at(2030, 10, 14, 9), slug: "triage" }),
  entry({ id: "r2", kind: "run", status: "failed", at: at(2030, 10, 14, 2), slug: "triage" }),
])
const byId = (id: string) => list.find((i) => i.id === id)!

function Harness({ start, permissions, onChanged = vi.fn() }: { start: string; permissions: CalendarPermissions; onChanged?: () => void }) {
  const [id, setId] = useState<string | null>(start)
  return (
    <CalendarPeek
      item={list.find((i) => i.id === id) ?? null}
      items={list}
      workspaceId="ws"
      routines={ROUTINES}
      permissions={permissions}
      onSelect={setId}
      onChanged={onChanged}
    />
  )
}
const MANAGER = { run: true, author: true }
const MEMBER = { run: false, author: false }

beforeEach(() => {
  fetcher.mockReset()
  push.mockReset()
  vi.clearAllMocks()
})

describe("calendar-peek", () => {
  it("pending start offers Cancel with ConfirmDialog and cancels through the pending queue", async () => {
    fetcher.mockResolvedValue({ ok: true, json: async () => ({}) })
    const onChanged = vi.fn()
    render(<Harness start="pending:once" permissions={MANAGER} onChanged={onChanged} />)
    const peek = screen.getByRole("dialog")
    expect(within(peek).getByRole("link", { name: "Open routine" })).toHaveAttribute("href", "/routines?slug=scan&view=plan")
    fireEvent.click(within(peek).getByRole("button", { name: "Cancel start" }))
    const confirm = await screen.findByRole("alertdialog")
    expect(confirm).toHaveTextContent("Cancel this start?")
    fireEvent.click(within(confirm).getByRole("button", { name: "Cancel start" }))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect(fetcher).toHaveBeenCalledWith("/api/v1/workspaces/ws/pipelines/pending/once/cancel", { method: "POST" })
    expect(toast.success).toHaveBeenCalledWith("Start cancelled · the calendar follows")
  })

  it("hides Cancel start and Run now from someone who may not use them", () => {
    const { unmount } = render(<Harness start="pending:once" permissions={MEMBER} />)
    expect(screen.queryByRole("button", { name: "Cancel start" })).toBeNull()
    unmount()
    render(<Harness start="planned:plan" permissions={MEMBER} />)
    expect(screen.queryByRole("button", { name: "Run now" })).toBeNull()
  })

  it("a waiting run says Decide to a manager and Open run to everyone else", () => {
    const { unmount } = render(<Harness start="run:r1" permissions={MANAGER} />)
    expect(screen.getByRole("link", { name: "Decide" })).toHaveAttribute("href", "/routines?slug=triage&run=r1")
    unmount()
    render(<Harness start="run:r1" permissions={MEMBER} />)
    expect(screen.getByRole("link", { name: "Open run" })).toBeInTheDocument()
  })

  it("a failed run is At risk; J / K walk the visible items", () => {
    render(<Harness start="run:r2" permissions={MEMBER} />)
    expect(screen.getByTestId("calendar-peek")).toHaveTextContent("At risk")
    fireEvent.click(screen.getByRole("button", { name: "Next (J)" }))
    expect(screen.getByTestId("calendar-peek")).not.toHaveTextContent("At risk")
    expect(screen.getByTestId("calendar-peek")).toHaveTextContent("Waiting")
    fireEvent.click(screen.getByRole("button", { name: "Previous (K)" }))
    expect(screen.getByTestId("calendar-peek")).toHaveTextContent("At risk")
  })

  it("Run now loads the routine's inputs, starts it and opens the run", async () => {
    fetcher.mockImplementation(async (_url: string, options?: RequestInit) => ({
      ok: true,
      json: async () => (options?.method === "POST" ? { run_id: "run-9" } : { definition: { inputs: [] } }),
    }))
    render(<Harness start="planned:plan" permissions={MANAGER} />)
    fireEvent.click(screen.getByRole("button", { name: "Run now" }))
    const dialog = await screen.findByRole("dialog", { name: /Run Weekly scan/ })
    fireEvent.click(within(dialog).getByRole("button", { name: "Run now" }))
    await waitFor(() => expect(push).toHaveBeenCalledWith("/routines?slug=scan&run=run-9"))
    const post = fetcher.mock.calls.find(([, o]) => o?.method === "POST")!
    expect(post[0]).toBe("/api/v1/workspaces/ws/pipelines/scan/run")
    expect(JSON.parse(post[1].body)).toEqual({ inputs: {} })
  })

  it("comes-from text never shows column names", () => {
    for (const i of list as CalendarItem[]) expect(comesFrom(i)).not.toMatch(/[a-z]+_[a-z]+|\.[a-z_]+/)
    expect(comesFrom(byId("planned:plan"))).toBe("Repeating schedule of Weekly scan · UTC")
    expect(comesFrom(byId("pending:once"))).toBe("One-time start of Weekly scan")
  })
})
