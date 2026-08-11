/**
 * The rail's emptiness, asserted where the decision is.
 *
 * Five different nothings reach this column and they want five different
 * actions from the reader. Which sentence a state produces is the decision
 * worth testing; mounting a sidebar and grepping for text would assert React.
 *
 * The bug this pins: emptiness used to be measured on the CHAIN LIST behind the
 * lens rather than on the lens itself, so an Issues lens holding no
 * issue-touching chain fell past the empty branch entirely and rendered a
 * section header reading "ISSUES TOUCHED 0" with nothing under it — a bare zero
 * over a blank column, which is the exact wall of zeros this rail was rebuilt
 * to delete.
 */

import { describe, expect, it } from "vitest"

import { emptyLensCopy, type EmptyLensFacts } from "../activity-sidebar"

const facts = (over: Partial<EmptyLensFacts> = {}): EmptyLensFacts => ({
  lens: "workflows",
  bareRuns: 0,
  loadedChainCount: 10,
  narrowedAway: false,
  scopedAway: false,
  chainsHaveUnrecorded: false,
  ...over,
})

describe("emptyLensCopy", () => {
  it("tells an empty workspace apart from a narrowed one", () => {
    // Opposite actions: one is "widen the question", the other is "nothing has
    // ever run here". One sentence for both sends a reader hunting for a broken
    // page when the answer was "clear the search".
    expect(emptyLensCopy(facts({ loadedChainCount: 0 }))).toMatch(/No workflows yet/i)
    expect(emptyLensCopy(facts({ narrowedAway: true }))).toMatch(/search/i)
  })

  it("names the pre-chain-recording era rather than claiming nothing ran", () => {
    expect(emptyLensCopy(facts({ loadedChainCount: 0, chainsHaveUnrecorded: true }))).toMatch(
      /before chain recording/i,
    )
  })

  it("blames the status segment when the status segment is what emptied it", () => {
    const copy = emptyLensCopy(facts({ scopedAway: true }))
    expect(copy).toMatch(/status/i)
    expect(copy).not.toMatch(/search/i)
  })

  it("prefers the search explanation when both narrowings could apply", () => {
    // Clearing the search is the action that restores the most, and a reader
    // told about the status segment first will clear that and still see nothing.
    expect(emptyLensCopy(facts({ narrowedAway: true, scopedAway: true }))).toMatch(/search/i)
  })

  it("gives every lens its own sentence when the window is not empty", () => {
    // The window HAS chains; the lens simply holds none of them. That is an
    // answer about the work, not about the filters, and each lens's answer
    // names where its rows actually live.
    expect(emptyLensCopy(facts({ lens: "issues" }))).toMatch(/Issues with no activity/i)
    expect(emptyLensCopy(facts({ lens: "agents" }))).toMatch(/no chain to belong to/i)
    expect(emptyLensCopy(facts({ lens: "routines" }))).toMatch(/Routines page/i)
  })

  it("sends the reader to Routines when runs happened but composed nothing", () => {
    const copy = emptyLensCopy(facts({ lens: "workflows", bareRuns: 3 }))
    expect(copy).toContain("3 runs")
    expect(copy).toMatch(/under Routines/i)
  })

  it("does not invent a count it does not have", () => {
    expect(emptyLensCopy(facts({ lens: "workflows", bareRuns: 0 }))).not.toMatch(/\d/)
  })

  it("never returns an empty string for any reachable state", () => {
    const lenses: EmptyLensFacts["lens"][] = ["workflows", "issues", "agents", "routines"]
    for (const lens of lenses) {
      for (const over of [
        {},
        { loadedChainCount: 0 },
        { loadedChainCount: 0, chainsHaveUnrecorded: true },
        { narrowedAway: true },
        { scopedAway: true },
        { bareRuns: 1 },
      ]) {
        expect(emptyLensCopy(facts({ lens, ...over })).length).toBeGreaterThan(0)
      }
    }
  })
})

// ---------------------------------------------------------------------------
// And the measurement itself, which is the half emptyLensCopy cannot see.
//
// The copy above is only reached when the rail decides the lens IS empty, and
// that decision was made on the chain list behind the lens rather than on the
// lens. So a window full of chains none of which touched an issue fell straight
// past the empty branch and rendered a section header reading "ISSUES TOUCHED 0"
// with nothing under it and no sentence saying why — the bare zero over a blank
// column that this rail was rebuilt to delete.
// ---------------------------------------------------------------------------

import { render, screen } from "@testing-library/react"
import { vi } from "vitest"

import type { ChainSummary } from "@/hooks/use-chains"
import { ActivitySidebar, EMPTY_FACETS, type ActivitySidebarProps } from "../activity-sidebar"

const chainRow = (over: Partial<ChainSummary> = {}): ChainSummary => ({
  origin: "run_a",
  started_by_kind: "schedule",
  started_by: "nightly",
  routine_slug: "triage",
  runs: 2,
  max_chain_depth: 1,
  failed_runs: 0,
  failed: false,
  first_activity: "2026-08-10T10:00:00.000Z",
  last_activity: "2026-08-10T10:00:01.000Z",
  duration_ms: 1000,
  issue_count: 0,
  agent_count: 0,
  ...over,
})

function mount(over: Partial<ActivitySidebarProps> = {}) {
  const chains = over.chains ?? [chainRow()]
  return render(
    <ActivitySidebar
      search=""
      onSearchChange={vi.fn()}
      facets={EMPTY_FACETS}
      onChange={vi.fn()}
      crews={[]}
      agents={[]}
      issues={[]}
      routines={[]}
      crewCounts={{}}
      issueCounts={{}}
      routineCounts={{}}
      focus={null}
      onFocus={vi.fn()}
      chains={chains}
      chainsBeforeStatus={chains}
      loadedChainCount={chains.length}
      chainsHaveMore={false}
      chainsHaveUnrecorded={false}
      routineBySlug={new Map()}
      selectedChain={null}
      onSelectChain={vi.fn()}
      lens="workflows"
      onLens={vi.fn()}
      onOpenEntity={vi.fn()}
      onToggleCollapse={vi.fn()}
      {...over}
    />,
  )
}

describe("ActivitySidebar — an empty lens says so", () => {
  it("explains an empty Issues lens instead of heading a blank column with 0", () => {
    // The window HAS a chain; it simply touched no issue.
    mount({ lens: "issues" })
    expect(screen.getByText(/Issues with no activity/i)).toBeTruthy()
    expect(screen.queryByText(/Issues touched/i)).toBeNull()
  })

  it("explains an empty Agents lens the same way", () => {
    mount({ lens: "agents" })
    expect(screen.getByText(/no chain to belong to/i)).toBeTruthy()
    expect(screen.queryByText(/Agents at work/i)).toBeNull()
  })

  it("still lists a lens that has rows", () => {
    // The guard must not be "always empty": a mutation that hid every list
    // would pass the two tests above and fail this one.
    mount({ lens: "routines" })
    expect(screen.getByText(/Routines that ran/i)).toBeTruthy()
    expect(screen.queryByText(/Routines page/i)).toBeNull()
  })

  it("names the window when the index returned only part of it", () => {
    mount({ chainsHaveMore: true })
    expect(screen.getByText(/Every count on this page describes these/i)).toBeTruthy()
  })

  it("says nothing about the window when it holds everything", () => {
    mount({ chainsHaveMore: false })
    expect(screen.queryByText(/Every count on this page describes these/i)).toBeNull()
  })
})
