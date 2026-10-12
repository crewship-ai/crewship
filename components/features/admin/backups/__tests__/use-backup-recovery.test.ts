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

// The real POST /admin/backups/restore body (internal/api/backup.go
// backupRestoreResponse): the server classifies the restore itself and names
// what did not land. The report must carry both, never fall back to "ok".
describe("legacyReport reads the server's restore result", () => {
  it("keeps the server's partial verdict and says why", () => {
    const r = legacyReport({
      result: "partial",
      attachments_missing: 12,
      dropped_crew_filesystems: ["ops", "infra"],
      rows_inserted_shortfalls: [{ table: "mission_tasks", recorded: 10, actual: 7 }],
      payload_row_count_mismatches: [{ table: "chats", recorded: 5, actual: 4 }],
      incomplete: [{ kind: "memory_blob_missing", detail: "2 memory files absent", count: 2 }],
      security_level_clamps: [{ credential_id: "c1", name: "Prod DB", from: "9", to: 3 }],
    })
    expect(r.result).toBe("partial")
    expect(r.warnings).toEqual([
      "12 attachment files missing",
      "crew files not restored: ops, infra",
      "mission_tasks: 7 of 10 rows landed",
      "chats: archive holds 4 rows, its manifest says 5",
      "2 memory files absent",
      "credential Prod DB clamped to security level 3",
    ])
  })

  it("trusts a partial verdict even when no reason is listed", () => {
    expect(legacyReport({ result: "partial" }).result).toBe("partial")
  })

  it("names dropped columns instead of printing objects", () => {
    const r = legacyReport({ result: "partial", dropped_columns: [{ table: "agents", column: "legacy_mode", rows: 3 }] })
    expect(r.notes).toContain("column dropped: agents.legacy_mode (3 rows)")
    expect(r.notes.join(" ")).not.toContain("[object Object]")
  })

  it("says crew files still have to be brought back when the Docker phase was skipped", () => {
    const r = legacyReport({ result: "ok", docker_phase_skipped: true })
    expect(r.notes).toContain("Crew files are not in the containers yet: start each crew, then bring back its files")
  })

  it("a clean server result stays ok", () => {
    const r = legacyReport({ result: "ok", attachments_missing: 0, dropped_crew_filesystems: [], incomplete: [] })
    expect(r).toEqual({ result: "ok", summary: "Restore finished", warnings: [], notes: [] })
  })
})
