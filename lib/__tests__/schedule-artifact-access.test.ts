import { describe, it, expect } from "vitest"
import { scheduleArtifactAccess } from "@/lib/schedule-artifact-access"

// Owner decision for #5356: a schedule whose creator no longer vouches for it
// must say so, and why, instead of silently running without artifact access.

describe("scheduleArtifactAccess", () => {
  it("says nothing with artifact access off or while it is active", () => {
    expect(scheduleArtifactAccess({})).toBeNull()
    expect(scheduleArtifactAccess({ artifact_access: "active" })).toBeNull()
  })

  it.each([
    ["creator_access_revoked", /access was revoked/i],
    ["creator_suspended", /suspended/i],
    ["creator_not_member", /no longer a member/i],
    ["creator_role", /role/i],
    ["creator_restricted", /restricted/i],
    ["agent_created", /agent/i],
    ["creator_unknown", /before/i],
    ["creator_not_in_good_standing", /no longer in good standing/i],
  ])("explains %s and how to restore it", (reason, text) => {
    const s = scheduleArtifactAccess({ artifact_access: "stopped", stopped_reason: reason })
    expect(s).not.toBeNull()
    expect(s!.label).toMatch(/artifact access stopped/i)
    expect(s!.reason).toMatch(text)
    expect(s!.reason).toMatch(/change|save/i)
  })

  it("still says it stopped for a reason it does not know", () => {
    const s = scheduleArtifactAccess({ artifact_access: "stopped", stopped_reason: "something_new" })
    expect(s?.label).toMatch(/stopped/i)
  })
})
