/**
 * The shell hands ONE narrowed chain list to every surface — asserted over the
 * source, because that is the only place the claim lives.
 *
 * This is the read-side twin of the argument in route_read_scope_invariant_test.go:
 * a rule that is only ever enforced by a reviewer noticing is not enforced. The
 * defect it closes was exactly that shape — lens-overviews.tsx carried a comment
 * promising "All three read the SAME ChainSummary[] the rail reads, so no
 * dashboard can disagree with the list beside it", and they did not. The rail
 * narrowed its own private copy while the shell handed the raw array to the three
 * dashboards, so typing in the search box left the rail showing two rows next to
 * a dashboard reporting twenty. Nothing failed, because nothing was checking.
 *
 * A source scan rather than a mounted shell, deliberately. ActivityStreamView
 * pulls the journal list, the journal stream, pipelines, schedules and missions;
 * mounting it means five mocks whose upkeep would swamp the one-line fact being
 * asserted, and a passing mount would still not prove the NEXT `chains={...}`
 * added below it was wired right. The scan proves it for every call site,
 * including ones that do not exist yet.
 *
 * If a surface ever legitimately needs the unnarrowed list, add it to
 * ALLOWED_RAW with the reason — the point is not that the list stays empty, it
 * is that widening it is a deliberate act with a written justification instead
 * of a silent default.
 */

import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { describe, expect, it } from "vitest"

const SHELL = resolve(__dirname, "../activity-stream-view.tsx")
const source = readFileSync(SHELL, "utf8")

/**
 * Props that may take something other than the narrowed list, and why.
 *
 * `loadedChainCount` is the size of the window BEFORE narrowing: it exists to
 * tell "nothing matches your search" apart from "nothing has ever run here",
 * which is a question about the raw window by definition.
 */
const ALLOWED_RAW = new Set(["loadedChainCount", "chainsBeforeStatus"])

describe("the shell's chain wiring", () => {
  it("passes the narrowed list to every surface that renders chains", () => {
    const passes = [...source.matchAll(/\bchains=\{([^}]+)\}/g)].map((m) => m[1].trim())
    expect(passes.length).toBeGreaterThan(0)
    for (const expr of passes) {
      expect(
        expr,
        `chains={${expr}} — every list, dashboard and drill-down reads the narrowed ` +
          `list, or the rail and the column beside it describe different windows`,
      ).toBe("visibleChains")
    }
  })

  it("gives the status segments the set the search left, not the scoped one", () => {
    // The segments must survive their own selection: counting over the scoped
    // list would render "Failed 3 · Waiting 0 · Running 0" — what is left after
    // the pick rather than what there is to pick.
    expect(source).toMatch(/chainsBeforeStatus=\{narrowedChains\.searched\}/)
  })

  it("measures the raw window only where the raw window is the question", () => {
    const rawUses = [...source.matchAll(/\b(\w+)=\{chains\.length\}/g)].map((m) => m[1])
    for (const prop of rawUses) {
      expect(ALLOWED_RAW.has(prop), `${prop}={chains.length} is unexplained`).toBe(true)
    }
  })

  it("fetches the answers the waiting join needs, not the ask types alone", () => {
    // #2036. `entriesInScope(_, "waiting")` retires a row by finding its
    // ANSWER in the window, and the answers are filed under Security —
    // `sourceEntryTypes("human")` excludes every one of them server-side. The
    // list read "Waiting on you: 5" beside a card reading 1, one click apart.
    //
    // Asserted over the source for the same reason as the chain wiring above:
    // the fact lives in one branch of one useMemo, and mounting the shell to
    // reach it costs five mocks. `waitingEntryTypes` itself is covered by
    // behaviour in lib/__tests__/activity-stream.test.ts.
    const branch = /facets\.scope === "waiting"[\s\S]{0,400}?\n\s*: /.exec(source)?.[0] ?? ""
    expect(branch, 'the params memo no longer has a "waiting" branch').not.toBe("")
    expect(branch).toContain("waitingEntryTypes()")
    expect(branch).not.toMatch(/\?\s*sourceEntryTypes\("human"\)/)
  })

  it("hands the drill-downs an id, never a display label, as the key they match on", () => {
    // `stop.label` is `identifier || title || id`, so on a workspace without
    // issue identifiers it is the TITLE — nothing matched, and the deep link
    // pointed at a URL-encoded sentence.
    expect(source).toMatch(/issueId=\{openIssue\.id\}/)
    expect(source).not.toMatch(/issueId=\{openIssue\.label\}/)
  })
})
