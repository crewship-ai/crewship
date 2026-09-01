import { describe, expect, it, vi } from "vitest"
import { render, screen } from "@testing-library/react"

import { LogsTypeChips } from "../logs-type-chips"
import { ENTRY_TYPES_BY_GROUP } from "@/lib/journal-groups"
import { GROUP_LABEL, GROUP_ORDER, type EntryGroup } from "@/lib/journal-style"
import { annotateEntries, filterEntries } from "@/lib/journal-perf"
import type { JournalEntry } from "@/lib/types/journal"

/**
 * The chip row is downstream of `filterEntries`, so a chip test that
 * hand-builds its `counts` prop cannot see the bug this file exists for:
 * `GROUP_KEYS` seeded `groupCounts` with 15 of the 18 groups, so
 * `groupCounts[grp]++` was `undefined++` → NaN for the rest, and
 * `NaN > 0` is false. Every assertion here therefore feeds the chip row
 * the counts the real pipeline produces.
 */

function entry(entryType: string, i: number): JournalEntry {
  return {
    id: `j${i}`,
    workspace_id: "ws1",
    ts: "2026-08-31T12:00:00Z",
    entry_type: entryType,
    severity: "info",
    actor_type: "agent",
    summary: entryType,
  }
}

/** One representative entry type per group; "other" needs an unmapped one. */
function sampleTypeFor(g: EntryGroup): string {
  if (g === "other") return "invented.later.by.the.backend"
  const types = ENTRY_TYPES_BY_GROUP[g]
  expect(types.length, `group "${g}" has no entry types`).toBeGreaterThan(0)
  return types[0]
}

function countsForOneEntryPerGroup() {
  const entries = annotateEntries(GROUP_ORDER.map((g, i) => entry(sampleTypeFor(g), i)))
  return filterEntries(entries, {
    severity: "all",
    matcher: null,
    muted: new Set<EntryGroup>(),
    bucket: null,
  }).groupCounts
}

describe("filterEntries → group counts", () => {
  it("counts every group in GROUP_ORDER as a number, never NaN", () => {
    const counts = countsForOneEntryPerGroup()
    for (const g of GROUP_ORDER) {
      expect(Number.isFinite(counts[g]), `groupCounts["${g}"] is ${counts[g]}`).toBe(true)
      expect(counts[g], `groupCounts["${g}"]`).toBe(1)
    }
  })
})

describe("LogsTypeChips", () => {
  it("renders a chip for every group that has entries", () => {
    const counts = countsForOneEntryPerGroup()
    render(
      <LogsTypeChips
        counts={counts}
        muted={new Set<EntryGroup>()}
        onToggle={vi.fn()}
        onResetAll={vi.fn()}
      />,
    )

    const missing = GROUP_ORDER.filter(
      (g) => screen.queryByLabelText(`Mute ${GROUP_LABEL[g]} (1)`) === null,
    )
    expect(missing, "groups whose chip never renders").toEqual([])
  })

  it("keeps chip order in GROUP_ORDER", () => {
    const counts = countsForOneEntryPerGroup()
    render(
      <LogsTypeChips
        counts={counts}
        muted={new Set<EntryGroup>()}
        onToggle={vi.fn()}
        onResetAll={vi.fn()}
      />,
    )
    const rendered = screen.getAllByRole("button").map((b) => b.textContent?.replace(/1$/, ""))
    expect(rendered).toEqual(GROUP_ORDER.map((g) => GROUP_LABEL[g]))
  })

  it("hides a group with no entries rather than showing a zero chip", () => {
    const counts = Object.fromEntries(GROUP_ORDER.map((g) => [g, 0])) as Record<EntryGroup, number>
    counts.exec = 3
    render(
      <LogsTypeChips
        counts={counts}
        muted={new Set<EntryGroup>()}
        onToggle={vi.fn()}
        onResetAll={vi.fn()}
      />,
    )
    expect(screen.getAllByRole("button")).toHaveLength(1)
    expect(screen.getByLabelText("Mute exec (3)")).toBeTruthy()
  })
})
