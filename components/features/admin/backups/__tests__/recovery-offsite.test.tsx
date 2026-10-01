import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

// Recovery step 1, "From off-site storage…": list what a destination holds,
// fetch a copy back (a server job the page follows), then continue the wizard
// with the fetched bundle. A copy already on this server is used as it is.

const h = vi.hoisted(() => ({ api: vi.fn(), toast: { success: vi.fn(), error: vi.fn(), message: vi.fn(), info: vi.fn() } }))
vi.mock("sonner", () => ({ toast: h.toast }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.api(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: null, loading: false }) }))
vi.mock("next/link", () => ({ default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }))
import { RestoreWizard } from "../backups-recovery"
import type { SectionCtx } from "../backups-console"

const ctx: SectionCtx = {
  scope: "instance", selected: new Set(), workspaces: [], currentWorkspaceId: null, demo: false, go: vi.fn(),
  focusRun: null, focusPath: null, backUpNow: vi.fn(), newPlanSignal: 0,
}
const KEY = "instance/crewship-instance-20260929T010000Z.tar.zst"
const LOCAL = "/var/lib/crewship/backups/crewship-instance-20260929T010000Z.tar.zst"
const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status }))
const job = (status: string, path: string | null = null) => ({ id: "f1", destination_id: "d1", key: KEY, status, path, size: 10, layers: 0, error: null, started_at: "2026-09-30T09:00:00Z", ended_at: null })
let statusCalls = 0

beforeEach(() => {
  h.api.mockReset()
  statusCalls = 0
  h.api.mockImplementation((url: string, init?: RequestInit) => {
    if (url.includes("/backups/runs")) return json({ data: [] })
    if (url.endsWith("/backups/destinations")) return json({ data: [{ id: "d1", name: "r2", kind: "s3", bucket: "b", prefix: "prod" }] })
    if (url.includes("/backups/copies?destination=d1")) return json({ destination_id: "d1", destination_name: "r2", copies: [
      { key: KEY, size: 538181632, modified: "2026-09-29T01:04:10Z", scope: "instance", workspace_id: null, local: false, local_path: null },
      { key: "instance/older.tar.zst", size: 1, modified: "2026-09-20T01:04:10Z", scope: "instance", workspace_id: null, local: true, local_path: "/var/lib/crewship/backups/older.tar.zst" },
    ] })
    if (url.endsWith("/backups/copies/fetch") && init?.method === "POST") return json(job("running"), 202)
    if (url.endsWith("/backups/copies/fetch/f1")) { statusCalls++; return json(statusCalls < 2 ? job("running") : job("done", LOCAL)) }
    if (url.endsWith("/restore/checks")) return Promise.resolve(new Response("404 page not found", { status: 404 }))
    return Promise.resolve(new Response("404 page not found", { status: 404 }))
  })
})
afterEach(cleanup)

it("fetches an off-site copy and continues the restore with it", async () => {
  render(<RestoreWizard ctx={ctx} />)
  fireEvent.click(await screen.findByRole("button", { name: "From off-site storage…" }))
  const row = (await screen.findByText(KEY)).closest("tr") as HTMLElement
  fireEvent.click(within(row).getByRole("button", { name: "Fetch" }))
  await waitFor(() => expect(h.api.mock.calls.some(([u, i]) => String(u).endsWith("/copies/fetch") && (i as RequestInit)?.method === "POST")).toBe(true))
  const post = h.api.mock.calls.find(([u, i]) => String(u).endsWith("/copies/fetch") && (i as RequestInit)?.method === "POST")
  expect(JSON.parse(String((post?.[1] as RequestInit).body))).toEqual({ destination_id: "d1", key: KEY })
  // The job is followed until it is done, then the wizard moves on.
  expect(await screen.findByRole("radiogroup", { name: "Restore into" }, { timeout: 5000 })).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Next" }))
  fireEvent.change(screen.getByLabelText("AGE identity"), { target: { value: "AGE-SECRET-KEY-1TEST" } })
  fireEvent.click(screen.getByRole("button", { name: "Run the checks" }))
  expect(await screen.findByText(`crewship recover --bundle ${LOCAL} --identity <key-file> --data-dir /var/lib/crewship`)).toBeInTheDocument()
})

it("a copy already on this server is used without fetching", async () => {
  render(<RestoreWizard ctx={ctx} />)
  fireEvent.click(await screen.findByRole("button", { name: "From off-site storage…" }))
  const row = (await screen.findByText("instance/older.tar.zst")).closest("tr") as HTMLElement
  fireEvent.click(within(row).getByRole("button", { name: "Use" }))
  expect(await screen.findByRole("radiogroup", { name: "Restore into" })).toBeInTheDocument()
  expect(h.api.mock.calls.some(([u]) => String(u).includes("/copies/fetch"))).toBe(false)
})

it("a failed fetch says why and stays on the list", async () => {
  h.api.mockImplementation((url: string, init?: RequestInit) => {
    if (url.includes("/backups/runs")) return json({ data: [] })
    if (url.endsWith("/backups/destinations")) return json({ data: [{ id: "d1", name: "r2", kind: "s3", bucket: "b", prefix: "" }] })
    if (url.includes("/backups/copies?destination=d1")) return json({ destination_id: "d1", destination_name: "r2", copies: [
      { key: KEY, size: 1, modified: "2026-09-29T01:04:10Z", scope: "instance", workspace_id: null, local: false, local_path: null }] })
    if (url.endsWith("/backups/copies/fetch") && init?.method === "POST") return json(job("running"), 202)
    if (url.endsWith("/backups/copies/fetch/f1")) return json({ ...job("failed"), error: "offsite: 1 environment layer(s) the bundle needs are missing at the destination" })
    return Promise.resolve(new Response("404 page not found", { status: 404 }))
  })
  render(<RestoreWizard ctx={ctx} />)
  fireEvent.click(await screen.findByRole("button", { name: "From off-site storage…" }))
  const row = (await screen.findByText(KEY)).closest("tr") as HTMLElement
  fireEvent.click(within(row).getByRole("button", { name: "Fetch" }))
  await waitFor(() => expect(h.toast.error).toHaveBeenCalledWith(expect.stringContaining("environment layer")), { timeout: 5000 })
  expect(screen.queryByRole("radiogroup", { name: "Restore into" })).toBeNull()
})
