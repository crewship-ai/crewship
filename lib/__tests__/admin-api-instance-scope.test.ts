// Part C: an instance-wide admin card is used by an instance admin who may
// belong to no workspace. INSTANCE_SCOPE says "no workspace, on purpose" —
// different from null, which still means "not known yet" and sends nothing.
import { describe, it, expect, vi, beforeEach } from "vitest"

const h = vi.hoisted(() => ({ apiFetch: vi.fn(async () => ({ ok: true })) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...(a as [])) }))

import { adminFetch, INSTANCE_SCOPE } from "@/lib/admin-api"
import { withWs } from "@/lib/admin-workspace-query"

beforeEach(() => h.apiFetch.mockClear())

describe("adminFetch", () => {
  it("sends an instance-wide request without a workspace for INSTANCE_SCOPE", async () => {
    await adminFetch("/api/v1/admin/keeper/config", INSTANCE_SCOPE)
    expect(h.apiFetch).toHaveBeenCalledWith("/api/v1/admin/keeper/config", undefined)
  })

  it("still refuses while the workspace is not known", async () => {
    await expect(adminFetch("/api/v1/admin/keeper/config", null)).rejects.toThrow()
    expect(h.apiFetch).not.toHaveBeenCalled()
  })

  it("withWs leaves the parameter off for INSTANCE_SCOPE", () => {
    expect(withWs("/api/v1/admin/health", INSTANCE_SCOPE)).toBe("/api/v1/admin/health")
  })
})
