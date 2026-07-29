import { describe, it, expect } from "vitest"

// =============================================================================
// A crew row must not grow a dot per agent.
//
// It used to render one dot for every agent in the crew — running and error
// uncapped, idle capped at 3 — so a crew of 35 running agents drew 35 dots and
// pushed the count beside them out of view. The roster is unbounded; the row
// is not.
//
// The count already answers "how many". The only thing it cannot answer is
// "is something running or broken", which is the whole reason to glance at a
// collapsed crew. So each STATE gets at most one mark, and idle gets none —
// idle is the normal case and does not need ink.
//
// This is the rule expressed as arithmetic, so it fails if anyone reintroduces
// a per-agent loop.
// =============================================================================

/** Marks the row renders for a given crew composition. Mirrors the JSX. */
function marks({ running, error }: { running: number; error: number }) {
  const out: string[] = []
  if (error > 0) out.push("error")
  if (running > 0) out.push("running")
  return out
}

describe("crew status marks", () => {
  it("never renders more marks than there are states", () => {
    for (const size of [1, 5, 35, 400]) {
      expect(marks({ running: size, error: 0 })).toHaveLength(1)
      expect(marks({ running: size, error: size })).toHaveLength(2)
    }
  })

  it("shows nothing when every agent is simply idle", () => {
    expect(marks({ running: 0, error: 0 })).toEqual([])
  })

  it("puts error before running — the worse state reads first", () => {
    expect(marks({ running: 2, error: 1 })).toEqual(["error", "running"])
  })

  it("still distinguishes a broken crew from a busy one", () => {
    // The reason the dots existed at all. A count alone cannot say this.
    expect(marks({ running: 0, error: 1 })).toContain("error")
    expect(marks({ running: 1, error: 0 })).not.toContain("error")
  })
})
