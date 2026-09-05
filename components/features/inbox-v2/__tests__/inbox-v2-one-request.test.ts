import { describe, expect, it } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"

// B10 (#2364, PRD-ISSUES-AND-ROUTINES-2026 §12): "/inbox-v2 makes one
// request" is the accept line for the list this page renders. Before this
// change, InboxV2 called `useInbox(workspaceId, "active", ...)` AND
// `useInbox(workspaceId, "resolved", ...)` — two separate GET
// /api/v1/inbox round-trips for the same workspace on every load, on top
// of the /api/v1/approvals poll and the paginated /api/v1/missions walk
// F28 named. This is a source-shape guard (the same technique
// internal/notify's TestRoutineUpdateSubkindMatchesProducer uses on the Go
// side) rather than a full network-mock render: InboxV2's dependency graph
// (useWorkspace, useApprovals, useRealtimeStatusSafe, useInboxLookup, the
// missions useQuery) is large enough that mounting the whole component in
// a unit test would mock more than it proves. What this DOES prove: nobody
// re-introduces the second `useInbox` call the merge fixed, and the
// derived `active`/`resolved` views come from ONE query. A mounted /
// Playwright-level proof of the full page's request count is a natural
// follow-up, not delivered here.
describe("InboxV2 list fetch", () => {
  it("calls useInbox exactly once (state=all), not once per state filter", () => {
    const src = readFileSync(
      join(__dirname, "..", "inbox-v2.tsx"),
      "utf8",
    )
    const calls = src.match(/useInbox\(/g) ?? []
    expect(calls).toHaveLength(1)
    if (!/useInbox\(\s*workspaceId,\s*"all"/.test(src)) {
      throw new Error(
        'InboxV2 must call useInbox(workspaceId, "all", ...) once and derive active/resolved ' +
          "client-side from that one result, not fetch each state filter separately.",
      )
    }
  })
})
