import { describe, expect, it } from "vitest"

import { legacyReport } from "../use-backup-recovery"

describe("legacyReport and complete environments", () => {
  it("a restored environment keeps the report ok and says processes start fresh", () => {
    const r = legacyReport({ dry_run: true, environments: [{ crew: "ops", result: "restored", unsafe: [] }] })
    expect(r.result).toBe("ok")
    expect(r.notes).toContain("Processes start fresh; what was only in memory is not restored")
  })

  it("a rebuilt or skipped environment makes it partial, with the reason", () => {
    const r = legacyReport({
      environments: [
        { crew: "researcher", result: "rebuilt", reason: "built for linux/arm64 and this server is linux/amd64", unsafe: [] },
        { crew: "infra", result: "skipped", reason: "no Docker on this server", unsafe: ["privileged mode on infra"] },
      ],
    })
    expect(r.result).toBe("partial")
    expect(r.warnings).toEqual([
      "environment researcher rebuilt instead of restored: built for linux/arm64 and this server is linux/amd64",
      "environment infra skipped: no Docker on this server",
    ])
    expect(r.notes).toContain("Not carried over for safety: privileged mode on infra")
  })

  it("a report without environments is unchanged", () => {
    const r = legacyReport({ dry_run: false })
    expect(r).toEqual({ result: "ok", summary: "Restore finished", warnings: [], notes: [] })
  })
})
