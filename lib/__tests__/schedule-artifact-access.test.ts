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

  // #5411: changing a schedule is the OWNER/ADMIN tier, so a MANAGER creator
  // cannot restore it by re-saving; the text must not promise them that.
  it.each(["creator_access_revoked", "creator_role", "agent_created", "creator_not_in_good_standing"])(
    "says only an OWNER or ADMIN restores %s, by adopting it",
    (reason) => {
      const s = scheduleArtifactAccess({ artifact_access: "stopped", stopped_reason: reason })
      expect(s!.reason).toMatch(/OWNER or ADMIN/)
      expect(s!.reason).toMatch(/becomes its creator/i)
      expect(s!.reason).toMatch(/MANAGER cannot change it/i)
      expect(s!.reason).not.toMatch(/who may edit schedules/i)
    },
  )

  it("says a routine an agent authored never reaches artifacts, and what restores it", () => {
    const s = scheduleArtifactAccess({ artifact_access: "stopped", stopped_reason: "routine_agent_authored" })
    expect(s!.reason).toMatch(/routine an agent authored/i)
    expect(s!.reason).toMatch(/routine a person authored/i)
    expect(s!.reason).not.toMatch(/its time, inputs/i)
  })

  it("still says it stopped for a reason it does not know", () => {
    const s = scheduleArtifactAccess({ artifact_access: "stopped", stopped_reason: "something_new" })
    expect(s?.label).toMatch(/stopped/i)
  })
})
