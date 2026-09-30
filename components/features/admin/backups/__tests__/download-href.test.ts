import { describe, expect, it, vi } from "vitest"

// Backup history's Download: an instance bundle belongs to no workspace, so
// the per-workspace route answers 404 for it. It streams from the instance
// route, which resolves any catalogued bundle.

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-a", loading: false }) }))

import { downloadHref } from "../use-backup-runs"

const instanceRoute = "/api/v1/admin/instance/backups/bundles/download?path=%2Fb%2Finstance%201.tar.zst"

describe("downloadHref", () => {
  it.each([
    ["an instance bundle uses the instance route, even with a workspace on screen", "ws-a", "instance", instanceRoute],
    ["a run's bundle with no workspace on screen uses the instance route", null, "workspaces", instanceRoute],
  ] as const)("%s", (_, ws, scope, want) => {
    expect(downloadHref("/b/instance 1.tar.zst", ws, scope)).toBe(want)
  })
  it("a workspace bundle keeps the per-workspace route", () => {
    const href = downloadHref("/b/w.tar.zst", "ws-b", "workspaces")
    expect(href).toContain("/api/v1/admin/backups/download")
    expect(href).toContain("ws-b")
  })
  it("a legacy row (no scope) with no workspace has no link", () => {
    expect(downloadHref("/b/w.tar.zst", null)).toBeNull()
  })
})
