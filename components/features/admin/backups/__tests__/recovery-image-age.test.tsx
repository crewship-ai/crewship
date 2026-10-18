import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"

// ADM-08: the restore checks add "crews on an older image", read per crew from
// GET /crews/{id}/image-status (the restore checks themselves do not compare
// image age). It is the state of this server now, and says so; where the
// server cannot answer, the row says that instead of "current".

const h = vi.hoisted(() => ({ api: vi.fn(), runs: [] as unknown[] }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), message: vi.fn(), info: vi.fn() } }))
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
import { checksFixture } from "../__fixtures__/backups"
import type { SectionCtx } from "../backups-scope"
import type { BackupRun } from "../backups-model"

const json = (b: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status }))
const run = (): BackupRun => ({
  id: "r1", plan_id: null, plan_name: null, trigger: "manual", note: null, scope: "workspaces", workspace_id: "ws-b", workspace_name: "B",
  kind: "full", status: "done", phases: [], bundle_path: "/b.tar.zst", size_bytes: 1, incomplete: [], started_at: "2026-09-30T00:00:00Z",
  ended_at: null, proof_level: 0, pinned: false, recipients: [], format_version: 3, restorable: null,
})
const ctx = (): SectionCtx => ({
  scope: "workspaces", selected: new Set(["ws-b"]), workspaces: [], currentWorkspaceId: "ws-a", demo: false, go: vi.fn(),
  focusRun: null, focusPath: "/b.tar.zst", backUpNow: vi.fn(), newPlanSignal: 0,
})

function api(status: (id: string) => Promise<Response>) {
  h.api.mockImplementation((url: string) => {
    if (url.endsWith("/restore/checks")) return json(checksFixture("workspaces"))
    if (url.includes("/api/v1/crews?")) return json([{ id: "c1", name: "Research" }, { id: "c2", name: "Ops" }, { id: "c3", name: "Code" }])
    const m = /\/crews\/(c\d)\/image-status/.exec(url)
    if (m) return status(m[1])
    return Promise.resolve(new Response("404", { status: 404 }))
  })
}
async function toChecks() {
  render(<RestoreWizard ctx={ctx()} />)
  fireEvent.change(screen.getByLabelText("New workspace name"), { target: { value: "B restored" } })
  fireEvent.click(screen.getByRole("button", { name: "Next" }))
  fireEvent.change(screen.getByLabelText("AGE identity"), { target: { value: "AGE-SECRET-KEY-1TEST" } })
  fireEvent.click(screen.getByRole("button", { name: "Run the checks" }))
}
const behind = (b: boolean) => json({ image: "crewship/crew:1", running: true, running_digest: "sha256:a", resolved_digest: "sha256:b", behind: b, reason: "" })

beforeEach(() => { h.runs = [run()]; h.api.mockReset() })
afterEach(cleanup)

describe("restore checks › older image", () => {
  it("counts the crews that run an older image, as of now", async () => {
    api((id) => behind(id !== "c3"))
    await toChecks()
    const row = await screen.findByTestId("image-age-check")
    await waitFor(() => expect(row).toHaveTextContent("2 of 3 crews run an older image"))
    expect(row).toHaveTextContent(/this server now/i)
    expect(h.api.mock.calls.map(([u]) => String(u))).toContain("/api/v1/crews/c1/image-status?workspace_id=ws-b")
  })

  it("says all are current only when every crew answered", async () => {
    api(() => behind(false))
    await toChecks()
    await waitFor(() => expect(screen.getByTestId("image-age-check")).toHaveTextContent("All 3 crews run the current image"))
  })

  it("says it cannot tell when the server cannot report digests, never 'current'", async () => {
    api((id) => (id === "c1" ? behind(false) : Promise.resolve(new Response("", { status: 503 }))))
    await toChecks()
    await waitFor(() => expect(screen.getByTestId("image-age-check")).toHaveTextContent("Could not read the image of 2 of 3 crews"))
    expect(screen.getByTestId("image-age-check")).not.toHaveTextContent(/current image/)
  })

  it("is absent when the checks are unavailable", async () => {
    h.api.mockImplementation(() => Promise.resolve(new Response("404", { status: 404 })))
    await toChecks()
    await screen.findByText(/checks before a restore/i)
    expect(screen.queryByTestId("image-age-check")).toBeNull()
  })
})
