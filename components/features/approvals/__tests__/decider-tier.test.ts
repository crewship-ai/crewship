import { describe, it, expect } from "vitest"

import { canRole } from "@/components/features/inbox/inbox-derive"
import { isAdminTier } from "@/lib/permissions/tiers"

// With /approvals folded into the Inbox, two checks decide what an approver
// sees there: isAdminTier gates loading the approvals queue (inbox-v2.tsx) and
// canRole(role, "manage") gates the Approve / Deny dock (inbox-v2-detail.tsx).
// Both mirror the server's roleManage on GET and POST /api/v1/approvals. If
// they ever disagree, someone sees a dock the server refuses, or a queue they
// cannot act on — so pin them to each other for every role.
describe("approval decider tier", () => {
  it.each(["OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER", null] as const)("%s: queue read and decide agree", (role) => {
    expect(canRole(role, "manage")).toBe(isAdminTier(role))
  })

  it("only OWNER and ADMIN decide", () => {
    expect(["OWNER", "ADMIN", "MANAGER", "MEMBER"].filter((r) => isAdminTier(r))).toEqual(["OWNER", "ADMIN"])
  })
})
