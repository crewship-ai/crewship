// The one-time starts card is where an accepted deferred start stays
// accountable: planned and waiting starts on top, and the ones that already
// left the queue below with what became of them.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, within } from "@testing-library/react"
import type { PendingStart } from "@/lib/routine-pending-starts"
import { RoutineOnceSchedule } from "../routine-once-schedule"

const h = vi.hoisted(() => ({ starts: [] as PendingStart[], role: "MANAGER" }))
vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: h.role, capabilities: [] }) }))
vi.mock("@/hooks/use-pending-starts", () => ({ usePendingStarts: () => ({ starts: h.starts, loading: false, error: null, refresh: vi.fn() }) }))

const soon = (minutes: number) => new Date(Date.now() + minutes * 60_000).toISOString()

beforeEach(() => {
  h.role = "MANAGER"
  h.starts = []
})
afterEach(cleanup)

describe("<RoutineOnceSchedule>", () => {
  it("keeps a start that waits for capacity in the planned list and says why", () => {
    h.starts = [
      { id: "wait", pipeline_slug: "invoice", fire_at: soon(-2), status: "pending", dispatch_attempts: 2, next_attempt_at: soon(1), can_cancel: true },
      { id: "plan", pipeline_slug: "invoice", fire_at: soon(90), status: "pending", dispatch_attempts: 0, can_cancel: true },
      { id: "elsewhere", pipeline_slug: "other", fire_at: soon(30), status: "pending" },
    ]
    render(<RoutineOnceSchedule workspaceId="ws" slug="invoice" />)
    expect(screen.getByText("2 planned")).toBeInTheDocument()
    expect(screen.getByText("Waiting for capacity")).toBeInTheDocument()
    expect(screen.getByTestId("pending-state-wait")).toHaveTextContent("tried 2 times")
    expect(screen.getByText("Planned")).toBeInTheDocument()
    expect(screen.getAllByRole("button", { name: "Remove" })).toHaveLength(2)
  })

  it("lists starts that left the queue with their outcome, and links the run once it exists", () => {
    h.starts = [
      { id: "failed", pipeline_slug: "invoice", fire_at: soon(-60), status: "failed", last_error: "The accepted recipe version is no longer available." },
      { id: "linked", pipeline_slug: "invoice", fire_at: soon(-120), status: "fired", run_id: "run_7" },
      { id: "running", pipeline_slug: "invoice", fire_at: soon(-1), status: "fired", run_id: "" },
    ]
    render(<RoutineOnceSchedule workspaceId="ws" slug="invoice" />)
    const recent = screen.getByRole("group", { name: "Recent accepted starts" })
    const failed = within(recent).getByTestId("settled-start-failed")
    expect(failed).toHaveTextContent("Did not run")
    expect(failed).toHaveTextContent("The accepted recipe version is no longer available.")
    expect(within(screen.getByTestId("settled-start-linked")).getByRole("link", { name: "Open run" })).toHaveAttribute("href", "/routines?slug=invoice&run=run_7")
    // A fired start without its run link may still be executing.
    const running = screen.getByTestId("settled-start-running")
    expect(running).toHaveTextContent("Started")
    expect(running).not.toHaveTextContent("Did not run")
    expect(within(running).queryByRole("link")).toBeNull()
  })

  it("offers removal only where the server allows it", () => {
    h.starts = [{ id: "plan", pipeline_slug: "invoice", fire_at: soon(90), status: "pending", can_cancel: false }]
    render(<RoutineOnceSchedule workspaceId="ws" slug="invoice" />)
    expect(screen.getByRole("button", { name: "Remove" })).toBeDisabled()
  })
})
