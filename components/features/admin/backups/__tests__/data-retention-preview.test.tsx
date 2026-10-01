import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

// A retention preview is bound to what it previewed (review B3): the targets
// and the changes are a snapshot, any change of selection or draft drops it
// (A→B→A and late answers included), the confirmation sends exactly the
// snapshot with the server's preview_id, and a 409 writes nothing.

const h = vi.hoisted(() => ({
  put: vi.fn(),
  putDefaults: vi.fn(),
  toast: { success: vi.fn(), error: vi.fn(), message: vi.fn(), info: vi.fn() },
}))
vi.mock("sonner", () => ({ toast: h.toast }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("../use-data-retention", () => ({
  putRetention: (...a: unknown[]) => h.put(...a),
  putRetentionDefaults: (...a: unknown[]) => h.putDefaults(...a),
  useRetentionDefaults: () => ({ status: "ready", data: { defaults: { inbox_days: null }, configured: [] }, reload: vi.fn() }),
}))
import { RetentionBody } from "../data-retention"
import type { SectionCtx } from "../backups-console"

const rows = [{ key: "inbox_days", label: "Inbox items", days: null, foreverAllowed: true }]
const ctx = (...ids: string[]): SectionCtx => ({
  scope: "workspaces", selected: new Set(ids), workspaces: ["a", "b", "c"].map((id) => ({ id, slug: id, name: id })),
  currentWorkspaceId: "a", demo: false, go: vi.fn(), focusRun: null, focusPath: null, backUpNow: vi.fn(), newPlanSignal: 0,
})
const previewOf = (id: string, previewId = `p-${id}`) => ({
  ok: true, data: { dry_run: true, preview_id: previewId, changes: [{ workspace_id: id, workspace_name: id, key: "inbox_days", from: null, to: 30, rows_affected: 5 }] },
})
const applies = () => h.put.mock.calls.filter((c) => c[2] === false)

beforeEach(() => {
  h.put.mockReset()
  h.putDefaults.mockReset()
  for (const f of Object.values(h.toast)) f.mockReset()
  h.put.mockImplementation((ids: string[] | null) => Promise.resolve(previewOf(ids?.[0] ?? "all")))
})
afterEach(cleanup)

function pickIn(group: string, label: string) {
  fireEvent.click(within(screen.getByRole("group", { name: group })).getByRole("button", { name: label }))
}
const pickInbox = (label: string) => pickIn("Keep Inbox items for", label)

// Ported from the reviewer's repro (review-retention-scope.test.tsx): a
// preview for A confirmed after switching to B must never write B.
it("a retention preview for A never applies its draft to B", async () => {
  const r = render(<RetentionBody rows={rows} ctx={ctx("a")} />)
  pickInbox("30 d")
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await screen.findByRole("alertdialog")
  r.rerender(<RetentionBody rows={rows} ctx={ctx("b")} />)
  const confirm = screen.queryByRole("button", { name: "Apply" })
  if (confirm) fireEvent.click(confirm)
  await act(async () => { await Promise.resolve() })
  for (const call of applies()) expect(call[0]).toEqual(["a"])
  expect(applies().some((c) => (c[0] as string[] | null)?.includes("b"))).toBe(false)
  expect(screen.queryByRole("alertdialog")).toBeNull()
})

it("confirms exactly the snapshot it previewed, with the server's preview_id", async () => {
  render(<RetentionBody rows={rows} ctx={ctx("a")} />)
  pickInbox("30 d")
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await screen.findByRole("alertdialog")
  h.put.mockResolvedValueOnce({ ok: true, data: { dry_run: false, changes: [] } })
  fireEvent.click(screen.getByRole("button", { name: "Apply" }))
  await waitFor(() => expect(applies()).toHaveLength(1))
  expect(applies()[0]).toEqual([["a"], [{ key: "inbox_days", days: 30 }], false, "p-a"])
})

it("a preview asked for A, answered after A→B→A, is dropped", async () => {
  let resolve: (v: unknown) => void = () => {}
  h.put.mockImplementationOnce(() => new Promise((r) => { resolve = r }))
  const r = render(<RetentionBody rows={rows} ctx={ctx("a")} />)
  pickInbox("30 d")
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  r.rerender(<RetentionBody rows={rows} ctx={ctx("b")} />)
  r.rerender(<RetentionBody rows={rows} ctx={ctx("a")} />)
  await act(async () => { resolve(previewOf("a")) })
  expect(screen.queryByRole("alertdialog")).toBeNull()
})

it("a change of the draft drops a pending preview", async () => {
  let resolve: (v: unknown) => void = () => {}
  h.put.mockImplementationOnce(() => new Promise((r) => { resolve = r }))
  render(<RetentionBody rows={rows} ctx={ctx("a")} />)
  pickInbox("30 d")
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  pickInbox("90 d")
  await act(async () => { resolve(previewOf("a")) })
  expect(screen.queryByRole("alertdialog")).toBeNull()
})

it("every workspace ticked previews and confirms as null, not the ids on screen", async () => {
  render(<RetentionBody rows={rows} ctx={ctx("a", "b", "c")} />)
  pickInbox("30 d")
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await screen.findByRole("alertdialog")
  fireEvent.click(screen.getByRole("button", { name: "Apply" }))
  await waitFor(() => expect(applies()).toHaveLength(1))
  expect(applies()[0][0]).toBeNull()
  expect(applies()[0][3]).toBe("p-all")
})

it("a 409 says something changed and writes nothing", async () => {
  render(<RetentionBody rows={rows} ctx={ctx("a")} />)
  pickInbox("30 d")
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await screen.findByRole("alertdialog")
  h.put.mockResolvedValueOnce({ ok: false, unavailable: false, status: 409, error: "the windows or the workspaces changed since the preview" })
  fireEvent.click(screen.getByRole("button", { name: "Apply" }))
  expect(await screen.findByText("Something changed since the preview — review again")).toBeInTheDocument()
  expect(h.toast.success).not.toHaveBeenCalled()
  await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
})

it("defaults confirm the snapshot they previewed with expect_preview", async () => {
  h.putDefaults.mockResolvedValueOnce({ ok: true, data: { applied: false, dry_run: true, preview_id: "d1", changes: [{ key: "inbox_days", from: null, to: 30 }] } })
  render(<RetentionBody rows={rows} ctx={ctx("a")} />)
  pickIn("New workspaces keep Inbox items for", "30 d")
  fireEvent.click(screen.getByRole("button", { name: "Save defaults for new workspaces…" }))
  await screen.findByRole("alertdialog")
  h.putDefaults.mockResolvedValueOnce({ ok: false, unavailable: false, status: 409, error: "the defaults changed since the preview" })
  fireEvent.click(screen.getByRole("button", { name: "Save defaults for new workspaces" }))
  expect(await screen.findByText("Something changed since the preview — review again")).toBeInTheDocument()
  expect(h.putDefaults.mock.calls[1]).toEqual([{ inbox_days: 30 }, false, "d1"])
  expect(h.toast.success).not.toHaveBeenCalled()
})

