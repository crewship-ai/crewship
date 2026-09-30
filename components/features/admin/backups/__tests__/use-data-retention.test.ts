import { describe, expect, it } from "vitest"

import { toRetentionChanges, toRetentionRows } from "../use-data-retention"

const api = {
  workspaces: [
    { workspace_id: "w1", workspace_name: "Dess", workspace_slug: "dess", windows: { inbox_days: null, routine_runs_days: 90 } },
    { workspace_id: "w2", workspace_name: "Coolify", workspace_slug: "coolify", windows: { inbox_days: 30, routine_runs_days: 90 } },
  ],
  keys: [
    { key: "routine_runs_days", label: "Routine runs", detail: "keeps the last 10", forever_allowed: false },
    { key: "inbox_days", label: "Inbox items", detail: null, forever_allowed: true },
  ],
  housekeeping: [{ key: "journal_compaction", label: "Journal compaction", value: "30 days", detail: "fixed" }],
}

describe("toRetentionRows", () => {
  it.each([
    ["shared value", "routine_runs_days", { days: 90, mixed: false, foreverAllowed: false }],
    ["differing values are mixed", "inbox_days", { days: null, mixed: true, foreverAllowed: true }],
  ])("%s", (_, key, want) => {
    const row = toRetentionRows(api).rows.find((r) => r.key === key)
    expect(row).toMatchObject(want)
  })

  it("keeps the server's order and appends housekeeping read-only", () => {
    const rows = toRetentionRows(api).rows
    expect(rows.map((r) => r.key)).toEqual(["routine_runs_days", "inbox_days", "journal_compaction"])
    expect(rows[2]).toMatchObject({ housekeeping: true, fixed: "30 days" })
  })
})

describe("toRetentionChanges", () => {
  it("flattens per-workspace changes with the next-sweep count", () => {
    const out = toRetentionChanges({
      dry_run: true,
      workspaces: [{ workspace_id: "w1", workspace_name: "Dess", changes: [{ key: "inbox_days", from: null, to: 90, rows_affected_next_sweep: 412 }] }],
    })
    expect(out).toEqual({ dry_run: true, changes: [{ workspace_id: "w1", workspace_name: "Dess", key: "inbox_days", from: null, to: 90, rows_affected: 412 }] })
  })
})
