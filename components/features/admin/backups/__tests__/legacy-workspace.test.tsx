import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react"

// The legacy /admin/backups* routes act on the workspace the request names,
// and an instance admin may name one they are not a member of. So the
// Backups UI names the workspace explicitly — the run's own, or the one the
// scope bar picks — never "wherever the admin happens to sit", and never the
// instance restore route the server does not have.

const h = vi.hoisted(() => ({ api: vi.fn(), toast: { success: vi.fn(), error: vi.fn(), message: vi.fn(), info: vi.fn() } }))
vi.mock("sonner", () => ({ toast: h.toast }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.api(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-a", loading: false }) }))

import { restore } from "../use-backup-recovery"
import { useBackupRuns, workspaceFor } from "../use-backup-runs"

const urls = () => h.api.mock.calls.map(([u]) => String(u))
const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status }))

beforeEach(() => { h.api.mockReset() })
afterEach(cleanup)

describe("workspaceFor", () => {
  it.each([
    ["the run's own workspace wins", "ws-c", ["ws-b"], "ws-c"],
    ["else the one workspace ticked", null, ["ws-b"], "ws-b"],
    ["else the workspace the admin sits in", null, ["ws-b", "ws-c"], "ws-a"],
  ])("%s", (_, run, selected, want) => {
    expect(workspaceFor({ selected: new Set(selected), currentWorkspaceId: "ws-a" }, run)).toBe(want)
  })
  it("is null when nothing names a workspace", () => {
    expect(workspaceFor({ selected: new Set(), currentWorkspaceId: null }, null)).toBeNull()
  })
})

describe("restore", () => {
  it("a workspace restore goes to the legacy route with the workspace named, never the instance route", async () => {
    h.api.mockImplementation(() => json({ dry_run: true }))
    const r = await restore({ path: "/b.tar.zst", target: "replace", dry_run: true }, "ws-b")
    expect(r.ok).toBe(true)
    expect(urls()).toEqual(["/api/v1/admin/backups/restore?workspace_id=ws-b"])
  })
  it.each(["empty_server", "isolated"] as const)("%s is never sent to the server", async (target) => {
    const r = await restore({ path: "/b.tar.zst", target, dry_run: true }, "ws-b")
    expect(r.ok).toBe(false)
    expect(h.api).not.toHaveBeenCalled()
  })
  it("says to pick a workspace when none is named", async () => {
    const r = await restore({ path: "/b.tar.zst", target: "replace", dry_run: true }, null)
    expect(r).toMatchObject({ ok: false })
    expect(h.api).not.toHaveBeenCalled()
  })
})

describe("useBackupRuns legacy fallback", () => {
  it("lists the bundles of every workspace ticked, not the one the admin sits in", async () => {
    h.api.mockImplementation((url: string) => {
      if (url.includes("/admin/instance/backups/runs")) return Promise.resolve(new Response("404 page not found", { status: 404 }))
      const ws = new URL(url, "http://x").searchParams.get("workspace_id")
      return json({ data: [{ path: `/${ws}.tar.zst`, file_name: `${ws}.tar.zst`, size_bytes: 1, scope: "workspace", encrypted: true }] })
    })
    const all = [{ id: "ws-a", name: "A", slug: "a" }, { id: "ws-b", name: "B", slug: "b" }, { id: "ws-c", name: "C", slug: "c" }]
    const { result } = renderHook(() => useBackupRuns("workspaces", new Set(["ws-b", "ws-c"]), all))
    await waitFor(() => expect(result.current.source).toBe("legacy"))
    await waitFor(() => expect(result.current.data?.length).toBe(2))
    expect(result.current.data?.map((r) => [r.workspace_id, r.bundle_path]).sort()).toEqual([["ws-b", "/ws-b.tar.zst"], ["ws-c", "/ws-c.tar.zst"]])
    expect(urls().some((u) => u.includes("workspace_id=ws-a"))).toBe(false)
  })
})

describe("the recovery wizard", () => {
  it("restores a workspace backup into the workspace the backup belongs to", async () => {
    vi.resetModules()
    vi.doMock("../use-backup-runs", async (orig) => ({
      ...(await orig<typeof import("../use-backup-runs")>()),
      useBackupRuns: () => ({ status: "ready", source: "runs", reload: vi.fn(), data: [{
        id: "r1", scope: "workspaces", workspace_id: "ws-b", workspace_name: "B", bundle_path: "/b.tar.zst", started_at: "2026-09-30T00:00:00Z",
        plan_id: null, plan_name: null, trigger: "manual", note: null, kind: "full", status: "done", phases: [], size_bytes: 1, incomplete: [],
        ended_at: null, proof_level: 0, pinned: false, recipients: [], format_version: 3, restorable: null,
      }] }),
    }))
    vi.doMock("../use-backup-settings", () => ({ useVaultKeys: () => ({ data: null }), useBackupSettings: () => ({ status: "unavailable", data: null }), saveBackupSettings: vi.fn() }))
    const { RestoreWizard } = await import("../backups-recovery")
    h.api.mockImplementation((url: string) => url.endsWith("/restore/checks") ? Promise.resolve(new Response("404 page not found", { status: 404 })) : json({ dry_run: true }))
    render(<RestoreWizard ctx={{ scope: "workspaces", selected: new Set(["ws-b", "ws-c"]), workspaces: [], currentWorkspaceId: "ws-a", demo: false, go: vi.fn(), focusRun: null, focusPath: "/b.tar.zst", backUpNow: vi.fn(), newPlanSignal: 0 }} />)
    fireEvent.click(screen.getByRole("radio", { name: /Replace a workspace/ }))
    fireEvent.click(screen.getByRole("button", { name: "Next", exact: true }))
    fireEvent.change(screen.getByLabelText("AGE identity"), { target: { value: "AGE-SECRET-KEY-1TEST" } })
    fireEvent.click(screen.getByRole("button", { name: "Run the checks" }))
    fireEvent.click(await screen.findByRole("button", { name: "Run the dry run" }))
    await waitFor(() => expect(urls()).toContain("/api/v1/admin/backups/restore?workspace_id=ws-b"))
    expect(urls().some((u) => /\/admin\/instance\/backups\/restore(\?|$)/.test(u))).toBe(false)
  })
})
