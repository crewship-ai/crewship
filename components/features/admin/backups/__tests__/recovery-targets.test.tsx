import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

// Recovery after the opponent review (#2990): where a restore may go depends
// on the archive itself — the server refuses a replace from a partial archive
// and --as-crew on anything but a crew archive — so the wizard offers only
// what will work. The result is the server's verdict, and a restore under a new
// name ends with what is left to finish, not with "Restore finished".

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

import { BackupsRecovery, RestoreWizard } from "../backups-recovery"
import type { SectionCtx } from "../backups-console"
import type { BackupRun } from "../backups-model"

const json = (b: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status }))
const run = (over: Partial<BackupRun>): BackupRun => ({
  id: "r1", plan_id: null, plan_name: null, trigger: "manual", note: null, scope: "workspaces", workspace_id: "ws-b", workspace_name: "B",
  kind: "full", status: "done", phases: [], bundle_path: "/b.tar.zst", size_bytes: 1, incomplete: [], started_at: "2026-09-30T00:00:00Z",
  ended_at: null, proof_level: 0, pinned: false, recipients: [], format_version: 3, restorable: null, ...over,
})
const ctx = (over: Partial<SectionCtx> = {}): SectionCtx => ({
  scope: "workspaces", selected: new Set(["ws-b"]), workspaces: [], currentWorkspaceId: "ws-a", demo: false, go: vi.fn(),
  focusRun: null, focusPath: "/b.tar.zst", backUpNow: vi.fn(), newPlanSignal: 0, ...over,
})
const targets = () => screen.getAllByRole("radio").map((r) => r.getAttribute("aria-label") ?? r.textContent)
const urls = () => h.api.mock.calls.map(([u]) => String(u))

beforeEach(() => {
  h.api.mockReset()
  h.api.mockImplementation(() => Promise.resolve(new Response("404 page not found", { status: 404 })))
})
afterEach(cleanup)

describe("targets follow the archive", () => {
  it("a workspace archive goes under a new name or replaces its workspace", () => {
    h.runs = [run({})]
    render(<RestoreWizard ctx={ctx()} />)
    expect(targets()).toEqual(["Into a new workspace", "Replace a workspace"])
  })

  it("a partial archive only goes under a new name, and says why", () => {
    h.runs = [run({ kind: "custom" })]
    render(<RestoreWizard ctx={ctx()} />)
    expect(targets()).toEqual(["Into a new workspace"])
    expect(screen.getByText(/Replace is not offered/)).toBeInTheDocument()
  })

  it("a crew archive goes back as that crew, never as a workspace", () => {
    h.runs = [run({ legacy: true, workspace_name: "B · crew" })]
    render(<RestoreWizard ctx={ctx()} />)
    expect(targets()).toEqual(["Crew under a new name", "The crew where it was"])
  })

  it("an instance archive restores offline or as an isolated drill", () => {
    h.runs = [run({ scope: "instance", workspace_id: null, workspace_name: null })]
    render(<RestoreWizard ctx={ctx({ scope: "instance" })} />)
    expect(targets()).toEqual(["Empty server", "Isolated instance"])
  })
})

describe("the result is the server's", () => {
  async function restoreUnderNewName(body: Record<string, unknown>) {
    h.runs = [run({})]
    h.api.mockImplementation((url: string, init?: RequestInit) => {
      if (url.endsWith("/restore/checks")) return Promise.resolve(new Response("404 page not found", { status: 404 }))
      const b = JSON.parse(String(init?.body ?? "{}"))
      if (b.files_only) return json({ result: "ok" })
      return json(b.dry_run ? { result: "ok", dry_run: true } : body)
    })
    render(<RestoreWizard ctx={ctx()} />)
    fireEvent.change(screen.getByLabelText("New workspace name"), { target: { value: "B restored" } })
    fireEvent.click(screen.getByRole("button", { name: "Next" }))
    fireEvent.change(screen.getByLabelText("AGE identity"), { target: { value: "AGE-SECRET-KEY-1TEST" } })
    fireEvent.click(screen.getByRole("button", { name: "Run the checks" }))
    fireEvent.click(await screen.findByRole("button", { name: "Run the dry run" }))
    fireEvent.click(await screen.findByRole("button", { name: "Restore…" }))
    const dialog = await screen.findByTestId("confirm-dialog")
    // A workspace restore holds nothing; only the offline instance one does.
    expect(dialog).not.toHaveTextContent(/stay held/)
    fireEvent.click(within(dialog).getByRole("button", { name: "Restore" }))
  }

  it("a partial restore under a new name says what is left to finish", async () => {
    await restoreUnderNewName({ result: "partial", attachments_missing: 2, docker_phase_skipped: true, restored_workspace_id: "ws-new" })
    expect(await screen.findByText("Data restored · environments still to finish")).toBeInTheDocument()
    expect(screen.getByText("partial")).toBeInTheDocument()
    expect(screen.getByText("2 attachment files missing")).toBeInTheDocument()
    expect(screen.getByText("Start each crew once")).toBeInTheDocument()
  })

  it("Bring back crew files sends files_only for the restored workspace", async () => {
    await restoreUnderNewName({ result: "ok", docker_phase_skipped: true, restored_workspace_id: "ws-new" })
    fireEvent.click(await screen.findByRole("button", { name: "Bring back crew files" }))
    await waitFor(() => expect(urls()).toContain("/api/v1/admin/backups/restore?workspace_id=ws-new"))
    const call = h.api.mock.calls.find(([u, i]) => String(u).includes("workspace_id=ws-new") && JSON.parse(String((i as RequestInit).body)).files_only)
    expect(JSON.parse(String((call![1] as RequestInit).body))).toMatchObject({ path: "/b.tar.zst", files_only: true, identity: "AGE-SECRET-KEY-1TEST" })
  })

  it("a clean restore with nothing left reads as finished", async () => {
    await restoreUnderNewName({ result: "ok", restored_workspace_id: "ws-new" })
    expect(await screen.findByText("Restore finished")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Bring back crew files" })).toBeNull()
  })
})

describe("on the nested page", () => {
  it("the side panel picks the tab; the page draws no tabs of its own", async () => {
    h.api.mockImplementation((url: string) => String(url).includes("/restores") ? json([]) : Promise.resolve(new Response("{}", { status: 404 })))
    render(<BackupsRecovery ctx={ctx({ inDrill: true, recoveryView: "history" })} />)
    expect(screen.queryByRole("group", { name: "Recovery" })).toBeNull()
    expect(await screen.findByText("No restore yet.")).toBeInTheDocument()
  })
})
