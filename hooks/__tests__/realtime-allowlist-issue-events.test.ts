import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { describe, expect, it } from "vitest"

import { VALID_REALTIME_TYPES } from "@/hooks/use-realtime"

/**
 * A6 (docs/prd/PRD-ISSUES-AND-ROUTINES-2026.md §17, F32/#2125): `issue.created`,
 * `issue.deleted` and `issue.started` were emitted server-side and silently
 * dropped by `VALID_REALTIME_TYPES` in hooks/use-realtime.tsx — handleMessage()
 * drops anything not listed there, with no error and no log, so the surface
 * that was supposed to update just... didn't, until a reload.
 *
 * This test does not hardcode those three names. It reads the Go source of
 * the handlers that emit issue lifecycle events and extracts the literal
 * event-type string each `broadcastIssueEvent(wsID, "...", ...)` call
 * passes, then checks that every one is registered in the allowlist. A
 * future call site added to one of these files that broadcasts a new,
 * unregistered type fails this test — that is the durable half of the fix.
 * Registering the three known offenders (this PR) is the one-time half.
 *
 * Scope: the handlers cited by the PRD and by #2125 —
 * issue_handler_create.go:320, issue_handler_update.go:451,
 * issue_handler_workflow.go:305 — plus the sibling issue-event files in the
 * same package that use the same `broadcastIssueEvent` wrapper, so a rename
 * or a moved call site doesn't quietly fall out of coverage.
 * `internal/api` broadcasts roughly three dozen *other* event types outside
 * the issue.* namespace (project.*, milestone.*, page.*, feature_flag.*,
 * integration.*, recurring_issue.*, triage_rule.*, ...) that are equally
 * unregistered today — out of scope for A6; see the A6 PR body for the
 * count and #2125 for the tracking issue.
 */

const ISSUE_HANDLER_FILES = [
  "internal/api/issue_handler_create.go",
  "internal/api/issue_handler_update.go",
  "internal/api/issue_handler_workflow.go",
  "internal/api/issue_handler_comments.go",
  "internal/api/issue_handler_relations.go",
]

const BROADCAST_CALL = /broadcastIssueEvent\([^,]+,\s*"([a-zA-Z0-9_.]+)"/g

function emittedTypes(): string[] {
  const types = new Set<string>()
  for (const rel of ISSUE_HANDLER_FILES) {
    const src = readFileSync(resolve(process.cwd(), rel), "utf8")
    for (const m of src.matchAll(BROADCAST_CALL)) types.add(m[1])
  }
  return [...types]
}

describe("realtime allowlist — issue event emitters (guard against #2125 regressing)", () => {
  const emitted = emittedTypes()

  it("found real emitters — an empty result means the regex or file list went stale, not that the bug is fixed", () => {
    expect(emitted.length).toBeGreaterThan(0)
    expect(emitted).toEqual(
      expect.arrayContaining(["issue.created", "issue.deleted", "issue.started", "issue.updated"]),
    )
  })

  it.each(emittedTypes().map((t) => [t] as const))(
    "%s (broadcastIssueEvent) is registered in VALID_REALTIME_TYPES",
    (type) => {
      expect(VALID_REALTIME_TYPES.has(type)).toBe(true)
    },
  )
})
