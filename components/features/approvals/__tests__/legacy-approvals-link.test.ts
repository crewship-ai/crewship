import { describe, it, expect } from "vitest"

import { legacyApprovalsTarget, LEGACY_APPROVALS_DEFAULT } from "@/components/features/approvals/legacy-approvals-link"

// /approvals is no longer a page: approval requests are decided in the Inbox.
// The route stays forever as a redirect because its URL is baked into links
// that already left the building — notification emails and Slack "Review"
// buttons (internal/notifyroute/links.go) and `crewship open approvals`.
describe("legacyApprovalsTarget", () => {
  it.each([
    ["", LEGACY_APPROVALS_DEFAULT],
    ["?status=pending", "/inbox?attention=approvals"],
    ["?status=decided", "/inbox?view=history"],
    ["?status=all", "/inbox?view=history"],
    ["?agent_id=ag_1", "/inbox?attention=approvals&agent_id=ag_1"],
    ["?agent_id=ag_1&status=decided", "/inbox?view=history&agent_id=ag_1"],
    // Only the known parameters travel; anything else is dropped rather than
    // handed to an Inbox that may read it differently.
    ["?filter=x&agent_id=a%20b", "/inbox?attention=approvals&agent_id=a+b"],
    ["?agent_id=", "/inbox?attention=approvals"],
    ["?status=bogus", "/inbox?attention=approvals"],
  ])("%s → %s", (query, want) => {
    expect(legacyApprovalsTarget(new URLSearchParams(query))).toBe(want)
  })

  it("defaults to the decisions waiting in the Inbox", () => {
    expect(LEGACY_APPROVALS_DEFAULT).toBe("/inbox?attention=approvals")
  })

  it("accepts a missing query", () => {
    expect(legacyApprovalsTarget(null)).toBe("/inbox?attention=approvals")
  })
})
