import { describe, it, expect, vi, beforeEach } from "vitest"

const h = vi.hoisted(() => ({ api: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.api(...a) }))

import { restore, restoreChecks } from "../use-backup-recovery"

const body = (call: unknown[]) => JSON.parse((call[1] as { body: string }).body)

// "The crew where it was" restores a crew archive under its own names. The
// checks and the restore must ask the server the same question: no new name,
// and a target the shared rule accepts (in_place), never "crew", which needs one.
describe("the crew where it was", () => {
  beforeEach(() => {
    h.api.mockReset()
    h.api.mockResolvedValue(new Response(JSON.stringify({ result: "ok" }), { status: 200 }))
  })

  it("asks the checks for in_place, with no new name", async () => {
    await restoreChecks({ path: "/b/ops.cbk", target: "crew_in_place" })
    const sent = body(h.api.mock.calls[0])
    expect(sent.target).toBe("in_place")
    expect(sent.as_crew).toBeUndefined()
  })

  it("restores with no rename and no replace", async () => {
    await restore({ path: "/b/ops.cbk", target: "crew_in_place", dry_run: true }, "ws-dess")
    const sent = body(h.api.mock.calls[0])
    expect(sent.as_crew).toBeUndefined()
    expect(sent.as_workspace).toBeUndefined()
    expect(sent.replace).toBeUndefined()
  })
})
