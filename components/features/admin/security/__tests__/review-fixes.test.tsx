// The independent review of 2026-09-30 (R3, R4, R5): what a bulk edit may
// carry across a change of selection, which response may land after the
// selection moved on, and what the overview may say when it could not check.
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, waitFor, renderHook, act, within } from "@testing-library/react"

const h = vi.hoisted(() => ({ api: vi.fn(), toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.api(...a) }))
vi.mock("sonner", () => ({ toast: h.toast }))
vi.mock("@/components/features/admin/keeper-health-card", () => ({ KeeperHealthCard: () => null }))

import { BulkGovernanceForm } from "../bulk-governance"
import { useInstanceKeeper, type InstanceGovRow } from "../use-instance-keeper"
import { SecurityOverview } from "../security-overview"

const row = (id: string, extra: Partial<InstanceGovRow> = {}) => ({
  workspace_id: id, workspace_name: id.toUpperCase(), workspace_slug: id, configured: true, enabled: false,
  security_contact_user_id: "", deny_notify_min_risk: 7, watch_spec: "", watch_presets: [], require_second_approver: false,
  gov_model_provider: "", gov_model_id: "", gov_model_credential_id: "", auto_lease_seconds: 0, behavior_sample_every: 5, ...extra,
}) as InstanceGovRow
const res = (data: unknown, status = 200) => ({ ok: status < 400, status, json: async () => data })
const preview = (ids: string[], id = "p-1") => ({
  applied: false, changed: ids.length, defaults_updated: false, preview_id: id,
  workspaces: ids.map((w) => ({ workspace_id: w, workspace_name: w.toUpperCase(), workspace_slug: w, changes: [{ field: "enabled", before: false, after: true }] })),
})
const body = (i: number) => JSON.parse(h.api.mock.calls[i][1].body)

beforeEach(() => { cleanup(); h.api.mockReset(); Object.values(h.toast).forEach((f) => f.mockReset()) })

describe("R3 · a bulk draft belongs to the workspaces it was made for", () => {
  const props = { section: "watchdog" as const, all: false, onSaved: vi.fn() }

  it("drops the draft, and says so, when the selection becomes other workspaces of the same number", () => {
    const r = render(<BulkGovernanceForm {...props} rows={[row("a"), row("b")]} />)
    fireEvent.click(screen.getByRole("radio", { name: "On" }))
    r.rerender(<BulkGovernanceForm {...props} rows={[row("c"), row("d")]} />)
    expect(screen.getByRole("button", { name: "Overwrite 2 workspaces…" })).toBeDisabled()
    expect(h.toast.info).toHaveBeenCalled()
  })

  it("confirms exactly the previewed request, with the preview's id", async () => {
    h.api.mockImplementation(async (_u: string, init: RequestInit) => {
      const b = JSON.parse(String(init.body))
      return res(b.dry_run ? preview(b.workspaces) : { ...preview(b.workspaces), applied: true })
    })
    render(<BulkGovernanceForm {...props} rows={[row("a"), row("b")]} />)
    fireEvent.click(screen.getByRole("radio", { name: "On" }))
    fireEvent.click(screen.getByRole("button", { name: "Overwrite 2 workspaces…" }))
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: /^Overwrite 2/ }))
    await waitFor(() => expect(h.api).toHaveBeenCalledTimes(2))
    expect(body(1)).toEqual({ workspaces: ["a", "b"], dry_run: false, expect_preview: "p-1", set: { enabled: true } })
  })

  it("closes the preview without saving when the selection moves while it is open", async () => {
    h.api.mockImplementation(async (_u: string, init: RequestInit) => res(preview(JSON.parse(String(init.body)).workspaces)))
    const r = render(<BulkGovernanceForm {...props} rows={[row("a"), row("b")]} />)
    fireEvent.click(screen.getByRole("radio", { name: "On" }))
    fireEvent.click(screen.getByRole("button", { name: "Overwrite 2 workspaces…" }))
    await screen.findByRole("alertdialog")
    r.rerender(<BulkGovernanceForm {...props} rows={[row("c"), row("d")]} />)
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
    expect(h.api).toHaveBeenCalledTimes(1)
  })

  it("ignores a preview that arrives after the selection changed", async () => {
    let finish: (v: unknown) => void = () => {}
    h.api.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
    const r = render(<BulkGovernanceForm {...props} rows={[row("a"), row("b")]} />)
    fireEvent.click(screen.getByRole("radio", { name: "On" }))
    fireEvent.click(screen.getByRole("button", { name: "Overwrite 2 workspaces…" }))
    r.rerender(<BulkGovernanceForm {...props} rows={[row("c"), row("d")]} />)
    await act(async () => finish(res(preview(["a", "b"]))))
    expect(screen.queryByRole("alertdialog")).toBeNull()
  })

  it("says so when the server refuses a save that no longer matches its preview", async () => {
    h.api.mockImplementation(async (_u: string, init: RequestInit) => {
      const b = JSON.parse(String(init.body))
      return b.dry_run ? res(preview(b.workspaces)) : res({ error: "the settings or the workspaces changed since the preview; review the changes again" }, 409)
    })
    render(<BulkGovernanceForm {...props} rows={[row("a"), row("b")]} />)
    fireEvent.click(screen.getByRole("radio", { name: "On" }))
    fireEvent.click(screen.getByRole("button", { name: "Overwrite 2 workspaces…" }))
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: /^Overwrite 2/ }))
    expect(await screen.findByRole("status")).toHaveTextContent("changed since the preview")
    expect(props.onSaved).not.toHaveBeenCalled()
  })

  it("names the field in each change of the preview", async () => {
    h.api.mockImplementation(async (_u: string, init: RequestInit) => res(preview(JSON.parse(String(init.body)).workspaces)))
    render(<BulkGovernanceForm {...props} rows={[row("a"), row("b")]} />)
    fireEvent.click(screen.getByRole("radio", { name: "On" }))
    fireEvent.click(screen.getByRole("button", { name: "Overwrite 2 workspaces…" }))
    const table = within(await screen.findByRole("alertdialog")).getByRole("table")
    expect(within(table).getAllByText("Watchdog: Off → On")).toHaveLength(2)
  })
})

describe("R4 · a late answer for an earlier selection never replaces the current one", () => {
  it("keeps the newer selection's activity", async () => {
    let finishOld: (v: unknown) => void = () => {}
    const old = new Promise((resolve) => { finishOld = resolve })
    h.api.mockImplementation((url: string) => {
      if (url.includes("/requests?")) return url.includes("workspace=old") ? old : Promise.resolve(res({ items: [{ id: "new" }], total: 1, counts: {}, by_workspace: [], by_type: {} }))
      return Promise.resolve(res(url.endsWith("/health") ? { workspaces: [] } : { workspaces: [], defaults: {} }))
    })
    const r = renderHook(({ selection }) => useInstanceKeeper(selection), { initialProps: { selection: ["old"] as string[] | null } })
    r.rerender({ selection: ["new"] })
    await waitFor(() => expect(r.result.current.requests?.items[0].id).toBe("new"))
    await act(async () => finishOld(res({ items: [{ id: "old" }], total: 1, counts: {}, by_workspace: [], by_type: {} })))
    expect(r.result.current.requests?.items[0].id).toBe("new")
  })
})

describe("R5 · green only after a check that ran", () => {
  const base = { entries: [], workspaceId: "a", onOpenActivity: () => {} }

  it("does not say the posture is fine when it could not be read", () => {
    render(<SecurityOverview {...base} status={null} posture={null} postureError="HTTP 500" health={[]} />)
    expect(screen.queryByText(/stands out/)).toBeNull()
    const card = screen.getByRole("region", { name: "Needs attention" })
    expect(card).toHaveTextContent("could not be checked")
    expect(card).toHaveTextContent("HTTP 500")
  })

  it("says the judge's health could not be read instead of showing no workspace", () => {
    render(<SecurityOverview {...base} status={null} posture={null} postureError={null} health={[]} healthError="HTTP 502" selectedCount={2} />)
    const card = screen.getByRole("region", { name: "Judge health" })
    expect(card).toHaveTextContent("could not be read")
    expect(card).not.toHaveTextContent("No workspace ticked")
  })

  it("shows why recent activity is missing instead of 'Nothing yet'", () => {
    render(<SecurityOverview {...base} status={null} posture={null} postureError={null} health={[]} activityError="HTTP 500" />)
    const card = screen.getByRole("region", { name: "Recent activity" })
    expect(card).toHaveTextContent("HTTP 500")
    expect(card).not.toHaveTextContent("Nothing yet")
  })
})

describe("follow-up · the same page loads once, and a preview belongs to one moment", () => {
  it("does not append the same page twice when Load more is clicked twice", async () => {
    let finish: (v: unknown) => void = () => {}
    const second = new Promise((resolve) => { finish = resolve })
    const page = (ids: string[]) => res({ items: ids.map((id) => ({ id })), total: 3, counts: {}, by_workspace: [], by_type: {} })
    h.api.mockImplementation((url: string) => {
      if (url.includes("/requests?")) return url.includes("offset=1") ? second : Promise.resolve(page(["first"]))
      return Promise.resolve(res(url.endsWith("/health") ? { workspaces: [] } : { workspaces: [], defaults: {} }))
    })
    const r = renderHook(() => useInstanceKeeper(["a"]))
    await waitFor(() => expect(r.result.current.requests?.items.length).toBe(1))
    let p1: Promise<void> = Promise.resolve(), p2: Promise<void> = Promise.resolve()
    act(() => { p1 = r.result.current.loadMore(); p2 = r.result.current.loadMore() })
    await act(async () => { finish(page(["second"])); await Promise.all([p1, p2]) })
    expect(r.result.current.requests?.items.map((i) => i.id)).toEqual(["first", "second"])
  })

  it("drops a preview asked for before the selection went away and came back", async () => {
    let finish: (v: unknown) => void = () => {}
    h.api.mockImplementation(() => new Promise((resolve) => { finish = resolve }))
    const props = { section: "watchdog" as const, all: false, onSaved: vi.fn() }
    const r = render(<BulkGovernanceForm {...props} rows={[row("a"), row("b")]} />)
    fireEvent.click(screen.getByRole("radio", { name: "On" }))
    fireEvent.click(screen.getByRole("button", { name: "Overwrite 2 workspaces…" }))
    r.rerender(<BulkGovernanceForm {...props} rows={[row("c"), row("d")]} />)
    r.rerender(<BulkGovernanceForm {...props} rows={[row("a"), row("b")]} />)
    await act(async () => finish(res(preview(["a", "b"], "stale"))))
    expect(screen.queryByRole("alertdialog")).toBeNull()
  })
})
