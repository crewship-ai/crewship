// Review R5: a failed read of the audit trail must not pass for a complete
// one. The histogram and the facet counts treat an incomplete read as a floor
// (truncated), never as "these are all the events there were".
import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"

const h = vi.hoisted(() => ({ api: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.api(...a) }))

import { useAuditActivity } from "../use-audit-activity"

const page = (n: number, total: number) => ({ ok: true, status: 200, json: async () => ({ data: [{ created_at: `2026-09-2${n}T10:00:00Z`, action: "x", entity_type: "y" }], pagination: { total_pages: total } }) })

beforeEach(() => h.api.mockReset())

describe("useAuditActivity", () => {
  it("marks a read that failed part-way as incomplete, with the reason", async () => {
    h.api.mockResolvedValueOnce(page(1, 3)).mockResolvedValueOnce({ ok: false, status: 500, json: async () => ({}) })
    const { result } = renderHook(() => useAuditActivity("ws", "workspace" as never, "2026-09-20", "2026-09-29"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.truncated).toBe(true)
    expect(result.current.error).toMatch(/500/)
  })

  it("marks a read that threw as incomplete", async () => {
    h.api.mockRejectedValueOnce(new Error("offline"))
    const { result } = renderHook(() => useAuditActivity("ws", "workspace" as never, "2026-09-20", "2026-09-29"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.truncated).toBe(true)
    expect(result.current.error).toBeTruthy()
  })
})
