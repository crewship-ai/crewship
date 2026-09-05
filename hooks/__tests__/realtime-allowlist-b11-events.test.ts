import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { describe, expect, it } from "vitest"

import { VALID_REALTIME_TYPES } from "@/hooks/use-realtime"

/**
 * B11 (PRD-ISSUES-AND-ROUTINES-2026.md §14.2/§17, F32/F43, #2368): the
 * remaining new issue event types — `issue.session.state`,
 * `issue.checkpoint.written` and `run.outcome` — went from "not emitted at
 * all" to "emitted AND allowlisted" in this PR
 * (internal/api/issue_session_realtime.go). Same shape as
 * realtime-allowlist-issue-events.test.ts (A6's guard for
 * `broadcastIssueEvent`), generalised to the `broadcastWorkspaceEvent`
 * call convention these three use instead — none of them go through
 * `IssueHandler`, so they can't reuse that wrapper.
 *
 * A future call site added to this file that broadcasts a new,
 * unregistered type fails this test.
 */

const SOURCE_FILE = "internal/api/issue_session_realtime.go"
const BROADCAST_CALL = /broadcastWorkspaceEvent\([^,]+,\s*[^,]+,\s*"([a-zA-Z0-9_.]+)"/g

function emittedTypes(): string[] {
  const src = readFileSync(resolve(process.cwd(), SOURCE_FILE), "utf8")
  const types = new Set<string>()
  for (const m of src.matchAll(BROADCAST_CALL)) types.add(m[1])
  return [...types]
}

describe("realtime allowlist — B11 session/outcome event emitters (#2368)", () => {
  const emitted = emittedTypes()

  it("found real emitters — an empty result means the regex or file path went stale, not that nothing broadcasts", () => {
    expect(emitted.length).toBeGreaterThan(0)
    expect(emitted).toEqual(
      expect.arrayContaining(["issue.session.state", "issue.checkpoint.written", "run.outcome"]),
    )
  })

  it.each(emittedTypes().map((t) => [t] as const))(
    "%s (broadcastWorkspaceEvent) is registered in VALID_REALTIME_TYPES",
    (type) => {
      expect(VALID_REALTIME_TYPES.has(type)).toBe(true)
    },
  )
})
