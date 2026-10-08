import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, waitFor, within } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn(), toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), message: vi.fn() }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("sonner", () => ({ toast: h.toast }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))

import { SchedulesBody } from "../backups-schedules"
import { plansFixture } from "../__fixtures__/backups"
import { PageSaveBar, PageSaveProvider } from "@/components/ui/page-save-bar"
import type { SectionCtx } from "../backups-console"

// Admin › Backups › a plan, on the nested page: the side panel lists the
// plans, so the page opens the one it picked and carries its actions — on/off,
// Run now, Delete — beside the editor.
function ctxOf(over: Partial<SectionCtx> = {}): SectionCtx {
  return {
    scope: "instance", selected: new Set(["ws-dess", "ws-coolify"]), workspaces: [
      { id: "ws-dess", name: "Dess", slug: "dess" }, { id: "ws-coolify", name: "Coolify", slug: "coolify" },
    ],
    currentWorkspaceId: "ws-dess", demo: false, go: vi.fn(), focusRun: null, focusPath: null, backUpNow: vi.fn(), newPlanSignal: 0,
    inDrill: true, focusPlan: null, ...over,
  }
}

function show(ctx: SectionCtx, reload = vi.fn()) {
  const utils = render(
    <PageSaveProvider>
      <SchedulesBody plans={plansFixture()} ctx={ctx} reload={reload} now={new Date("2026-10-08T10:00:00Z")} />
      <PageSaveBar />
    </PageSaveProvider>,
  )
  return { ...utils, reload }
}

const calls = (method: string, part: string) =>
  h.apiFetch.mock.calls.filter(([u, init]) => String(u).includes(part) && (init as RequestInit | undefined)?.method === method)

beforeEach(() => {
  window.history.replaceState(null, "", "/admin/backups?section=schedules")
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue(new Response("{}", { status: 404 }))
  h.toast.mockReset()
  h.toast.error.mockReset()
  h.toast.success.mockReset()
  h.toast.message.mockReset()
})
afterEach(() => cleanup())

describe("Schedules on the nested page", () => {
  it("opens the plan the panel picked, with no plan list of its own", () => {
    show(ctxOf({ focusPlan: "plan-memory" }))
    expect(screen.getByLabelText("Plan name")).toHaveValue("Memory every 6 h")
    expect(screen.queryByRole("button", { name: /^Edit / })).toBeNull()
  })

  it("opens the first plan when the panel picked none", () => {
    show(ctxOf())
    expect(screen.getByLabelText("Plan name")).toHaveValue("Complete recovery")
  })

  it("a new plan is unsaved and has no on/off or Run now yet", () => {
    show(ctxOf({ focusPlan: "new" }))
    expect(screen.getByRole("region", { name: "Unsaved changes" })).toBeInTheDocument()
    expect(screen.queryByRole("switch", { name: "Enabled" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Run now" })).toBeNull()
  })

  it("a whole-instance plan is always a full backup: no choice of contents", () => {
    show(ctxOf())
    expect(screen.queryByRole("button", { name: "Only some kinds" })).toBeNull()
    expect(document.querySelector("[data-slot=contents-table]")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Workspaces" }))
    fireEvent.click(screen.getByRole("button", { name: "Only some kinds" }))
    expect(screen.getByText(/Not full protection: issues, inbox, projects and approvals are only in a full backup/)).toBeInTheDocument()
    expect(document.querySelector("[data-slot=contents-table]")).not.toBeNull()
  })

  it("Enabled saves at once, sending the saved plan with the switch flipped", async () => {
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) =>
      init?.method === "PUT" ? new Response(JSON.stringify({ ...plansFixture()[0], enabled: false }), { status: 200 }) : new Response("{}", { status: 404 }))
    const { reload } = show(ctxOf())
    fireEvent.change(screen.getByLabelText("Plan name"), { target: { value: "Edited, not saved" } })
    fireEvent.click(screen.getByRole("switch", { name: "Enabled" }))
    await waitFor(() => expect(calls("PUT", "/backups/plans/plan-complete")).toHaveLength(1))
    const body = JSON.parse(String((calls("PUT", "/backups/plans/plan-complete")[0][1] as RequestInit).body))
    expect(body.enabled).toBe(false)
    expect(body.name).toBe("Complete recovery")
    await waitFor(() => expect(reload).toHaveBeenCalled())
  })

  it("a refused on/off flips back and says so in the corner", async () => {
    h.apiFetch.mockImplementation(async (_u: string, init?: RequestInit) =>
      init?.method === "PUT" ? new Response(JSON.stringify({ error: "plan is locked" }), { status: 409 }) : new Response("{}", { status: 404 }))
    show(ctxOf())
    const sw = screen.getByRole("switch", { name: "Enabled" })
    fireEvent.click(sw)
    await waitFor(() => expect(h.toast.error).toHaveBeenCalledWith("Couldn’t save Backup plan", expect.anything()))
    expect(sw).toHaveAttribute("aria-checked", "true")
  })

  it("Run now starts this plan", async () => {
    h.apiFetch.mockImplementation(async (_u: string, init?: RequestInit) =>
      init?.method === "POST" ? new Response(JSON.stringify({ id: "r1", run_id: "r1", run_ids: ["r1"], status: "running" }), { status: 200 }) : new Response("{}", { status: 404 }))
    show(ctxOf())
    fireEvent.click(screen.getByRole("button", { name: "Run now" }))
    await waitFor(() => expect(calls("POST", "/backups/run")).toHaveLength(1))
    expect(JSON.parse(String((calls("POST", "/backups/run")[0][1] as RequestInit).body))).toMatchObject({ plan_id: "plan-complete" })
  })

  it("Delete asks first, says the backups stay, then deletes", async () => {
    h.apiFetch.mockImplementation(async (_u: string, init?: RequestInit) =>
      init?.method === "DELETE" ? new Response(null, { status: 204 }) : new Response("{}", { status: 404 }))
    const { reload } = show(ctxOf())
    fireEvent.click(screen.getByRole("button", { name: "Delete…" }))
    const dialog = await screen.findByRole("alertdialog")
    expect(dialog).toHaveTextContent(/Backups it made stay until they age out/)
    expect(calls("DELETE", "/backups/plans/plan-complete")).toHaveLength(0)
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete plan" }))
    await waitFor(() => expect(calls("DELETE", "/backups/plans/plan-complete")).toHaveLength(1))
    await waitFor(() => expect(reload).toHaveBeenCalled())
  })

  it("marks each calendar entry with an icon and a word", () => {
    show(ctxOf())
    for (const l of ["✓ done", "✕ failed", "↻ catch-up / skipped", "○ planned", "◇ environments"]) expect(screen.getByText(l)).toBeInTheDocument()
  })
})
