/**
 * Invariants of the Activity shell that live in one line of its source each,
 * asserted over the source because mounting the shell to reach them costs a
 * mock per data source and still would not prove the next call site right.
 *
 * Behaviour (rows, counts, the Peek, keys) is covered by mounted tests in
 * activity-stream-view.test.tsx and by lib/__tests__/activity-feed-merge.test.ts.
 */

import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { describe, expect, it } from "vitest"

const SHELL = resolve(__dirname, "../activity-stream-view.tsx")
const source = readFileSync(SHELL, "utf8")

describe("the shell's wiring", () => {
  it("counts the views over the same rows the list draws", () => {
    // The panel's numbers and the table beside it must describe one window:
    // the same work, the same changes, the same (debounced) filters.
    expect(source).toMatch(/allWork\.filter\(\(w\) => workPasses\(w, effective, filterCtx\)\)/)
    expect(source).toMatch(/allChanges\.filter\(\(c\) => changePasses\(c, effective, filterCtx\)\)/)
    expect(source).toMatch(/viewCounts\(allWork, allChanges, effective, filterCtx,/)
  })

  it("asks the journal for no more than the API serves", () => {
    const page = Number(/const JOURNAL_PAGE = ([\d_]+)/.exec(source)?.[1]?.replace(/_/g, ""))
    expect(page).toBeGreaterThan(0)
    expect(page, "the journal API rejects limit > 500").toBeLessThanOrEqual(500)
    const chains = Number(/const CHAIN_LIMIT = ([\d_]+)/.exec(source)?.[1]?.replace(/_/g, ""))
    expect(chains, "GET /api/v1/chains caps limit at 200").toBeLessThanOrEqual(200)
  })

  it("opens one stream, and it keeps both kinds of row live", () => {
    expect(source.match(/useJournalStream\(/g)).toHaveLength(1)
    // A change is prepended; a run event re-reads the chain index.
    expect(source).toMatch(/prependLive\(e\)/)
    expect(source).toMatch(/movesWork\(e\.entry_type\)[\s\S]{0,200}refreshChainsRef\.current\(\)/)
  })
})
