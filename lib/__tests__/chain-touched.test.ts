import { describe, expect, it } from "vitest"

import { chainTouched } from "@/lib/chain-touched"
import type { ChainSummary } from "@/hooks/use-chains"

const chain = (over: Partial<ChainSummary>): ChainSummary =>
  ({
    origin: "o",
    started_by_kind: "user",
    started_by: "Demo User",
    runs: 1,
    max_chain_depth: 0,
    failed_runs: 0,
    failed: false,
    first_activity: "2026-08-10T12:00:00Z",
    last_activity: "2026-08-10T12:00:05Z",
    duration_ms: 5000,
    issue_count: 0,
    agent_count: 0,
    ...over,
  }) as ChainSummary

describe("chainTouched — what a workflow row says it did", () => {
  // The row exists to tell two runs of ONE routine apart. The routine name
  // cannot do that: it is the same on both. What differs is what each run
  // reached — which issue, which agent — so that is what the second line says.
  it("names the issue by its identifier, which is what a human recognises", () => {
    const s = chainTouched(chain({ issue_count: 1, issues: [{ id: "m1", identifier: "ENG-7" }] }))
    expect(s).toBe("ENG-7")
  })

  it("names agents by slug", () => {
    const s = chainTouched(chain({ agent_count: 1, agents: [{ id: "a1", slug: "riley", assignments: 1 }] }))
    expect(s).toBe("riley")
  })

  it("says how many an agent took when it took more than one", () => {
    const s = chainTouched(chain({ agent_count: 1, agents: [{ id: "a1", slug: "riley", assignments: 3 }] }))
    expect(s).toBe("riley ×3")
  })

  it("puts issues before agents — the noun a person went looking for", () => {
    const s = chainTouched(
      chain({
        issue_count: 1,
        issues: [{ id: "m1", identifier: "ENG-7" }],
        agent_count: 1,
        agents: [{ id: "a1", slug: "riley", assignments: 1 }],
      }),
    )
    expect(s).toBe("ENG-7 · riley")
  })

  it("reports the FULL count when the server capped the list", () => {
    // issue_count is uncapped on purpose. Rendering only the returned five
    // would make a chain that touched forty issues read as one that touched
    // five — a cut list that does not say it was cut.
    const s = chainTouched(
      chain({
        issue_count: 40,
        issues: [
          { id: "1", identifier: "ENG-1" },
          { id: "2", identifier: "ENG-2" },
        ],
      }),
    )
    expect(s).toBe("ENG-1, ENG-2 +38")
  })

  it("falls back to the id when an issue has no identifier yet", () => {
    // A freshly created issue can reach the index before its identifier is
    // readable. Rendering "undefined" is worse than rendering the id.
    expect(chainTouched(chain({ issue_count: 1, issues: [{ id: "cmsn123" }] }))).toBe("cmsn123")
  })

  it("returns empty when the chain touched nothing, so the row shows no separator", () => {
    expect(chainTouched(chain({}))).toBe("")
  })

  it("survives a count with no list — the server capped to zero or omitted it", () => {
    // A count without refs is reachable: the arrays are omitempty. Saying "+3"
    // alone is still true and still useful; crashing or printing nothing is not.
    expect(chainTouched(chain({ issue_count: 3 }))).toBe("3 issues")
  })
})
