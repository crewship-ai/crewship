import { describe, it, expect, vi, beforeEach } from "vitest"

const h = vi.hoisted(() => ({ api: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.api(...a) }))

import {
  coverageCounts, coverageRows, manualRunBody, runsCapNote, scopeLabel, RUNS_LIMIT,
  type ScopeWorkspace, type WorkspaceCoverage,
} from "../backups-model"
import { restore } from "../use-backup-recovery"
import { inspectBundle, runNow } from "../use-backup-runs"

const W = (id: string, name: string, crews = 1): ScopeWorkspace => ({ id, name, slug: id, crews })
const C = (id: string, status: WorkspaceCoverage["status"], last: string | null, plan: string | null = "Nightly"): WorkspaceCoverage =>
  ({ workspace_id: id, name: id, last_backup_at: last, status, plan, proof: 0 })
const body = (call: unknown[]) => JSON.parse((call[1] as { body: string }).body)

describe("coverage: least protected first", () => {
  const workspaces = [W("a", "A"), W("b", "B"), W("c", "C"), W("d", "D"), W("e", "E", 0)]
  const cov = [C("a", "ok", "2026-11-19T03:00:00Z"), C("b", "bad", null, null), C("c", "warn", "2026-11-10T03:00:00Z"), C("d", "bad", null)]

  it("orders never backed up, stale, covered, nothing to back up", () => {
    expect(coverageRows(cov, workspaces).map((r) => [r.id, r.state])).toEqual([
      ["b", "never"], ["d", "never"], ["c", "stale"], ["a", "covered"], ["e", "none"],
    ])
  })

  it("counts each state once; a workspace the server left out has nothing to back up", () => {
    expect(coverageCounts(coverageRows(cov, workspaces))).toEqual({ covered: 1, stale: 1, never: 2, none: 1, total: 5 })
  })

  it("without the server's answer yet, no workspace is judged", () => {
    expect(coverageRows(undefined, workspaces)).toEqual([])
  })
})

describe("the scope in words", () => {
  const all = [W("a", "Alpha"), W("b", "Beta"), W("c", "Gamma")]
  it("says whole instance, one name, a count or all", () => {
    expect(scopeLabel("instance", new Set(), all)).toBe("Whole instance")
    expect(scopeLabel("workspaces", new Set(["a"]), all)).toBe("Alpha")
    expect(scopeLabel("workspaces", new Set(["a", "b"]), all)).toBe("2 workspaces")
    expect(scopeLabel("workspaces", new Set(["a", "b", "c"]), all)).toBe("All 3 workspaces")
  })
})

describe("the newest 500 runs", () => {
  it("asks for the most the server gives and says so only when that many came back", () => {
    expect(RUNS_LIMIT).toBe(500)
    expect(runsCapNote(499)).toBeNull()
    expect(runsCapNote(500)).toBe("the newest 500 runs")
  })
})

describe("Back up now request: recipients XOR passphrase", () => {
  const nightly = { id: "p1", scope: "instance" as const, preset: "complete" as const, workspace_ids: [], contents: [], env_mode: "complete" as const, recipient_ids: ["r1"] }
  const form = { scope: "instance" as const, plan: null, workspaceIds: [] as string[], contents: "plan" as const, env: "plan" as const, encrypt: "plan" as const, passphrase: "", note: "" }

  it("as the plan, for the plan's own targets: the plan runs and only the note and keys travel", () => {
    expect(manualRunBody({ ...form, plan: nightly })).toEqual({ scope: "instance", plan_id: "p1" })
  })
  it("a passphrase replaces the plan's keys and a note is trimmed; recipients never travel with it", () => {
    const b = manualRunBody({ ...form, scope: "workspaces", workspaceIds: ["a", "b"], contents: "full", env: "complete", encrypt: "passphrase", passphrase: "s3cret", note: " before the upgrade " })
    expect(b).toEqual({ scope: "workspaces", workspace_ids: ["a", "b"], preset: "workspace", env_mode: "complete", passphrase: "s3cret", note: "before the upgrade" })
    expect(b).not.toHaveProperty("recipient_ids")
  })
  it("a whole-instance run is the complete preset and never partial", () => {
    expect(manualRunBody({ ...form, contents: "full", env: "files" })).toEqual({ scope: "instance", preset: "complete", env_mode: "files" })
  })
  it("only some kinds sends the custom preset and its categories", () => {
    expect(manualRunBody({ ...form, scope: "workspaces", workspaceIds: ["a"], contents: "some", categories: ["memory"] }))
      .toEqual({ scope: "workspaces", workspace_ids: ["a"], preset: "custom", contents: ["memory"] })
  })
  it("a plan that would not back up what the dialog says is copied, not run: the server takes targets from the plan", () => {
    const ws = { ...nightly, id: "p2", scope: "workspaces" as const, preset: "workspace" as const, env_mode: "files" as const, workspace_ids: [] }
    expect(manualRunBody({ ...form, scope: "workspaces", workspaceIds: ["a"], plan: ws }))
      .toEqual({ scope: "workspaces", workspace_ids: ["a"], preset: "workspace", env_mode: "files", recipient_ids: ["r1"] })
  })
  it("an instance plan and a workspace scope copy its keys too", () => {
    expect(manualRunBody({ ...form, scope: "workspaces", workspaceIds: ["a"], plan: nightly }))
      .toEqual({ scope: "workspaces", workspace_ids: ["a"], preset: "workspace", env_mode: "complete", recipient_ids: ["r1"] })
  })
})

describe("runNow and the manifest", () => {
  beforeEach(() => { h.api.mockReset(); h.api.mockResolvedValue(new Response("{}", { status: 200 })) })
  it("posts the body as given", async () => {
    await runNow({ scope: "instance", note: "x" })
    expect(h.api.mock.calls[0][0]).toBe("/api/v1/admin/instance/backups/run")
    expect(body(h.api.mock.calls[0])).toEqual({ scope: "instance", note: "x" })
  })
  it("reads the manifest of one bundle by path", async () => {
    await inspectBundle("/b/x y.cship")
    expect(h.api.mock.calls[0][0]).toBe("/api/v1/admin/instance/backups/bundles/inspect?path=%2Fb%2Fx%20y.cship")
  })
})

describe("restore: the two switches the server accepts", () => {
  beforeEach(() => { h.api.mockReset(); h.api.mockResolvedValue(new Response(JSON.stringify({ result: "ok" }), { status: 200 })) })
  it("sends recover_services on any online restore when it is on", async () => {
    await restore({ path: "/b/x", target: "new_workspace", as_workspace: "Copy", dry_run: false, recover_services: true }, "ws-1")
    expect(body(h.api.mock.calls[0]).recover_services).toBe(true)
    expect(body(h.api.mock.calls[0]).discard_workbooks).toBeUndefined()
  })
  it("sends discard_workbooks only together with replace", async () => {
    await restore({ path: "/b/x", target: "replace", dry_run: false, discard_workbooks: true }, "ws-1")
    expect(body(h.api.mock.calls[0])).toMatchObject({ replace: true, discard_workbooks: true })
    h.api.mockClear()
    await restore({ path: "/b/x", target: "new_workspace", as_workspace: "Copy", dry_run: false, discard_workbooks: true }, "ws-1")
    expect(body(h.api.mock.calls[0]).discard_workbooks).toBeUndefined()
  })
})
