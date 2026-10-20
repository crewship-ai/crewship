import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, cleanup, waitFor } from "@testing-library/react"

// Owner, real phone 19. 10.: denying an approval raised a red "Routine
// overdue-invoice-reminders couldn't finish / wait step "approve" (approval)
// denied". A deliberate deny is not an error: a neutral confirmation that
// names the routine by its name.

const handlers: Record<string, (e: { payload: Record<string, unknown> }) => void> = {}
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: (type: string, cb: (e: { payload: Record<string, unknown> }) => void) => { handlers[type] = cb },
}))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-current" }) }))
vi.mock("next/navigation", () => ({ usePathname: () => "/inbox", useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }))
const sonner = vi.hoisted(() => ({ error: vi.fn(), warning: vi.fn(), success: vi.fn(), info: vi.fn() }))
vi.mock("sonner", () => ({ toast: sonner }))
const api = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api.fetch(...a) }))

import { RealtimeToasts } from "../realtime-toasts"

const fire = (type: string, payload: Record<string, unknown>) => handlers[type]({ payload })

beforeEach(() => {
  for (const f of Object.values(sonner)) f.mockReset()
  api.fetch.mockReset()
  api.fetch.mockResolvedValue({ ok: false, json: async () => ({}) })
})
afterEach(() => { cleanup() })

describe("a denied approval", () => {
  it("is a neutral confirmation, not an error", () => {
    render(<RealtimeToasts />)
    fire("pipeline.run.failed", {
      pipeline_slug: "overdue-invoice-reminders",
      status: "FAILED",
      failed_at_step: "approve",
      error_message: 'wait step "approve" (approval) denied',
    })
    expect(sonner.error).not.toHaveBeenCalled()
    expect(sonner.info).toHaveBeenCalledWith("Denied · Overdue invoice reminders stopped", expect.objectContaining({ id: "routine-denied:overdue-invoice-reminders" }))
  })

  it("names the routine by its name once the server answers", async () => {
    api.fetch.mockResolvedValue({ ok: true, json: async () => ({ name: "Overdue invoice reminders (EU)" }) })
    render(<RealtimeToasts />)
    fire("pipeline.run.failed", { pipeline_slug: "overdue-invoice-reminders", error_message: 'wait step "approve" (approval) denied' })
    await waitFor(() => expect(sonner.info).toHaveBeenLastCalledWith("Denied · Overdue invoice reminders (EU) stopped", expect.objectContaining({ id: "routine-denied:overdue-invoice-reminders" })))
    expect(api.fetch).toHaveBeenCalledWith("/api/v1/workspaces/ws-current/pipelines/overdue-invoice-reminders")
  })
})

describe("a routine that really failed", () => {
  it("names the routine, not its slug", () => {
    render(<RealtimeToasts />)
    fire("pipeline.run.failed", { pipeline_slug: "weekly-scan", error_message: "exit 1" })
    expect(sonner.error).toHaveBeenCalledWith("Routine Weekly scan couldn’t finish", expect.objectContaining({ id: "routine-failed:weekly-scan" }))
  })
})
