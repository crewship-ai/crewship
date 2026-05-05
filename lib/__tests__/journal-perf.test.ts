import { describe, it, expect } from "vitest"
import { annotateEntries, filterEntries } from "@/lib/journal-perf"
import type { JournalEntry } from "@/lib/types/journal"

function entry(overrides: Partial<JournalEntry> = {}): JournalEntry {
  return {
    id: `id-${Math.random().toString(36).slice(2, 8)}`,
    workspace_id: "ws_test",
    ts: "2026-05-05T12:00:00Z",
    entry_type: "exec.command",
    severity: "info",
    actor_type: "agent",
    summary: "viktor runs pnpm test",
    payload: {},
    refs: {},
    ...overrides,
  }
}

describe("annotateEntries", () => {
  it("attaches _tsMs once and is idempotent on re-call", () => {
    const list = [
      entry({ id: "a", ts: "2026-05-05T12:00:00Z" }),
      entry({ id: "b", ts: "2026-05-05T12:01:00Z" }),
    ]
    const a1 = annotateEntries(list)
    expect(a1[0]._tsMs).toBe(new Date("2026-05-05T12:00:00Z").getTime())
    expect(a1[1]._tsMs).toBe(new Date("2026-05-05T12:01:00Z").getTime())
    // Mutates in place — same reference back.
    expect(a1).toBe(list)
    // Re-running doesn't reparse; still a number, still correct.
    const a2 = annotateEntries(list)
    expect(a2[0]._tsMs).toBe(a1[0]._tsMs)
  })

  it("falls back to 0 for unparseable ts", () => {
    const list = [entry({ id: "x", ts: "not-a-date" })]
    const a = annotateEntries(list)
    expect(a[0]._tsMs).toBe(0)
  })
})

describe("filterEntries", () => {
  function ann(entries: JournalEntry[]) {
    return annotateEntries(entries)
  }

  it("returns sevCounts from search-filtered set, before severity filter", () => {
    const list = ann([
      entry({ id: "a", severity: "info", summary: "alpha" }),
      entry({ id: "b", severity: "warn", summary: "alpha" }),
      entry({ id: "c", severity: "info", summary: "beta" }),
    ])
    const out = filterEntries(list, { severity: "warn", matcher: null, muted: new Set(), bucket: null })
    // Severity filter narrows the bucketed array but counts reflect ALL passing the matcher (none here).
    expect(out.sevCounts.all).toBe(3)
    expect(out.sevCounts.info).toBe(2)
    expect(out.sevCounts.warn).toBe(1)
    expect(out.bucketed.length).toBe(1)
    expect(out.bucketed[0].id).toBe("b")
  })

  it("applies the matcher to filter both counts and entries", () => {
    const list = ann([
      entry({ id: "a", summary: "ALLOW read" }),
      entry({ id: "b", summary: "DENY write" }),
      entry({ id: "c", summary: "ALLOW exec" }),
    ])
    const matcher = (e: JournalEntry) => (e.summary || "").toLowerCase().includes("allow")
    const out = filterEntries(list, { severity: "all", matcher, muted: new Set(), bucket: null })
    expect(out.sevCounts.all).toBe(2)
    expect(out.bucketed.map((e) => e.id)).toEqual(["a", "c"])
  })

  it("mutes a group → entries excluded, but groupCounts reflect pre-mute counts", () => {
    const list = ann([
      entry({ id: "a", entry_type: "exec.command" }),
      entry({ id: "b", entry_type: "exec.output_chunk" }),
      entry({ id: "c", entry_type: "network.egress" }),
    ])
    const out = filterEntries(list, { severity: "all", matcher: null, muted: new Set(["exec"]), bucket: null })
    expect(out.groupCounts.exec).toBe(2)
    expect(out.groupCounts.network).toBe(1)
    expect(out.bucketed.map((e) => e.id)).toEqual(["c"])
    // `filtered` excludes muted too (used by histogram).
    expect(out.filtered.map((e) => e.id)).toEqual(["c"])
  })

  it("bucket narrowing applies only to bucketed; filtered keeps the wider context", () => {
    const list = ann([
      entry({ id: "a", ts: "2026-05-05T12:00:00Z" }),
      entry({ id: "b", ts: "2026-05-05T12:00:30Z" }),
      entry({ id: "c", ts: "2026-05-05T12:01:00Z" }),
    ])
    const fromMs = new Date("2026-05-05T12:00:25Z").getTime()
    const toMs = new Date("2026-05-05T12:00:35Z").getTime()
    const out = filterEntries(list, {
      severity: "all",
      matcher: null,
      muted: new Set(),
      bucket: { fromMs, toMs },
    })
    expect(out.filtered.length).toBe(3)
    expect(out.bucketed.map((e) => e.id)).toEqual(["b"])
  })

  it("does one pass — counts + filter slice are mutually consistent", () => {
    const list = ann([
      entry({ id: "1", severity: "info", entry_type: "exec.command" }),
      entry({ id: "2", severity: "warn", entry_type: "network.egress" }),
      entry({ id: "3", severity: "warn", entry_type: "exec.command" }),
      entry({ id: "4", severity: "error", entry_type: "keeper.decision" }),
    ])
    const out = filterEntries(list, { severity: "warn", matcher: null, muted: new Set(), bucket: null })
    // counts come from the search-filtered set (all entries).
    expect(out.sevCounts.all).toBe(4)
    expect(out.sevCounts.warn).toBe(2)
    // Group counts post-severity-filter (i.e., the warn rows only).
    expect(out.groupCounts.exec).toBe(1)
    expect(out.groupCounts.network).toBe(1)
    expect(out.groupCounts.keeper).toBe(0)
    // bucketed = only warn rows.
    expect(out.bucketed.map((e) => e.id).sort()).toEqual(["2", "3"])
  })
})
