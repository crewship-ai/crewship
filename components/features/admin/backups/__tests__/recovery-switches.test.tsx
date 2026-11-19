import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

// Restore › Checks: the two switches the server accepts on an online restore
// (restoreRequest in internal/api/backup.go). "Also bring back service data"
// (recover_services) goes with any online restore; "Discard open workbooks"
// (discard_workbooks) only with Replace, which is not offered for a partial
// archive. An instance archive is never restored online, so it has neither.

const h = vi.hoisted(() => ({ api: vi.fn(), runs: [] as unknown[], toast: { success: vi.fn(), error: vi.fn(), message: vi.fn(), info: vi.fn() } }))
vi.mock("sonner", () => ({ toast: h.toast }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.api(...a) }))
vi.mock("../use-backup-runs", async (orig) => ({
  ...(await orig<typeof import("../use-backup-runs")>()),
  useBackupRuns: () => ({ data: h.runs, status: "ready", source: "runs", reload: vi.fn() }),
}))
vi.mock("../use-backup-settings", () => ({
  useVaultKeys: () => ({ data: null }),
  useBackupSettings: () => ({ status: "unavailable", data: null }),
  useDestinations: () => ({ status: "ready", data: [] }),
  saveBackupSettings: vi.fn(),
}))
vi.mock("next/link", () => ({ default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }))

import { RestoreWizard } from "../backups-recovery"
import type { SectionCtx } from "../backups-scope"
import type { BackupRun } from "../backups-model"

const json = (b: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status }))
const run = (over: Partial<BackupRun>): BackupRun => ({
  id: "r1", plan_id: null, plan_name: null, trigger: "manual", note: null, scope: "workspaces", workspace_id: "ws-b", workspace_name: "B",
  kind: "full", status: "done", phases: [], bundle_path: "/b.tar.zst", size_bytes: 1, incomplete: [], started_at: "2026-09-30T00:00:00Z",
  ended_at: null, proof_level: 0, pinned: false, recipients: [], format_version: 3, restorable: null, ...over,
})
const ctx = (over: Partial<SectionCtx> = {}): SectionCtx => ({
  scope: "workspaces", selected: new Set(["ws-b"]), workspaces: [{ id: "ws-b", name: "B", slug: "b" }], currentWorkspaceId: "ws-a", demo: false, go: vi.fn(),
  focusRun: null, focusPath: "/b.tar.zst", backUpNow: vi.fn(), newPlanSignal: 0, ...over,
})
const bodies = (part: string) => h.api.mock.calls.filter(([u]) => String(u).includes(part)).map(([, i]) => JSON.parse(String((i as RequestInit).body ?? "{}")))

async function toChecks(choice?: string) {
  h.api.mockImplementation((url: string, init?: RequestInit) => {
    if (String(url).endsWith("/restore/checks")) return Promise.resolve(new Response("404", { status: 404 }))
    return json(JSON.parse(String(init?.body ?? "{}")).dry_run ? { result: "ok", dry_run: true } : { result: "ok" })
  })
  render(<RestoreWizard ctx={ctx()} />)
  if (choice) fireEvent.click(screen.getByRole("radio", { name: choice }))
  else fireEvent.change(screen.getByLabelText("New workspace name"), { target: { value: "B copy" } })
  fireEvent.click(screen.getByRole("button", { name: "Next" }))
  fireEvent.change(screen.getByLabelText("AGE identity"), { target: { value: "AGE-SECRET-KEY-1TEST" } })
  fireEvent.click(screen.getByRole("button", { name: "Run the checks" }))
  return screen.findByRole("button", { name: "Run the dry run" })
}

beforeEach(() => {
  h.api.mockReset()
  h.runs = [run({})]
})
afterEach(cleanup)

describe("Restore › Checks: the switches", () => {
  it("under a new name: service data only, off, and sent only when on, never to the checks", async () => {
    await toChecks()
    expect(screen.getByRole("switch", { name: "Also bring back service data" })).not.toBeChecked()
    expect(screen.queryByRole("switch", { name: "Discard open workbooks" })).toBeNull()
    fireEvent.click(screen.getByRole("switch", { name: "Also bring back service data" }))
    fireEvent.click(screen.getByRole("button", { name: "Run the dry run" }))
    await waitFor(() => expect(bodies("/admin/backups/restore").length).toBe(1))
    expect(bodies("/admin/backups/restore")[0]).toMatchObject({ dry_run: true, recover_services: true, as_workspace: "B copy" })
    expect(bodies("/admin/backups/restore")[0].discard_workbooks).toBeUndefined()
    expect(bodies("/restore/checks")[0].recover_services).toBeUndefined()
  })

  it("Replace offers Discard open workbooks, and sends it with replace", async () => {
    await toChecks("Replace a workspace")
    fireEvent.click(screen.getByRole("switch", { name: "Discard open workbooks" }))
    fireEvent.click(screen.getByRole("button", { name: "Run the dry run" }))
    await waitFor(() => expect(bodies("/admin/backups/restore").length).toBe(1))
    expect(bodies("/admin/backups/restore")[0]).toMatchObject({ replace: true, discard_workbooks: true })
    expect(bodies("/admin/backups/restore")[0].recover_services).toBeUndefined()
  })

  it("Replace without the switch sends no discard_workbooks: the server refuses a target with workbooks", async () => {
    await toChecks("Replace a workspace")
    fireEvent.click(screen.getByRole("button", { name: "Run the dry run" }))
    await waitFor(() => expect(bodies("/admin/backups/restore").length).toBe(1))
    expect(bodies("/admin/backups/restore")[0].discard_workbooks).toBeUndefined()
  })

  it("a partial archive is never replaced, so there is nothing to discard", () => {
    h.runs = [run({ kind: "custom" })]
    render(<RestoreWizard ctx={ctx()} />)
    expect(screen.queryByRole("radio", { name: "Replace a workspace" })).toBeNull()
    expect(screen.getByText(/Replace is not offered/)).toBeInTheDocument()
  })

  it("an instance archive is never restored online: command line only, no restore request, no switches", async () => {
    h.runs = [run({ scope: "instance", workspace_id: null, workspace_name: null })]
    h.api.mockImplementation(() => Promise.resolve(new Response("404", { status: 404 })))
    render(<RestoreWizard ctx={ctx({ scope: "instance" })} />)
    fireEvent.click(screen.getByRole("button", { name: "Next" }))
    fireEvent.change(screen.getByLabelText("AGE identity"), { target: { value: "AGE-SECRET-KEY-1TEST" } })
    fireEvent.click(screen.getByRole("button", { name: "Run the checks" }))
    expect(await screen.findByText(/Restore from the command line/)).toBeInTheDocument()
    expect(screen.queryByRole("switch")).toBeNull()
    expect(screen.queryByRole("button", { name: "Run the dry run" })).toBeNull()
    expect(h.api.mock.calls.some(([u]) => String(u).includes("/admin/backups/restore"))).toBe(false)
    expect(within(document.body).getByText(/crewship recover --bundle/)).toBeInTheDocument()
  })
})
