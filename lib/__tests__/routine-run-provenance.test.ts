import { describe, expect, it } from "vitest"
import { declaredCredentialTypes, routineRunOrigin } from "../routine-run-provenance"

describe("historical routine provenance", () => {
  it("reads credential types only from the executed definition, including nested and hook steps", () => {
    const executed = {
      credentials_required: [{ type: "github", scope: "repo" }, { type: "bad secret value" }],
      steps: [{ type: "foreach", foreach: { steps: [
        { type: "http", http: { credential_ref: { type: "stripe" }, body: "SECRET_IN_BODY" } },
      ] }, hooks: { after: { http: { credential_ref: { type: "slack" } } } } }],
      hooks: { before_all: { http: { credential_ref: { type: "github" } } } },
    }
    const currentHead = { credentials_required: [{ type: "new_head_only" }] }
    expect(declaredCredentialTypes(executed)).toEqual(["github", "slack", "stripe"])
    expect(declaredCredentialTypes(executed)).not.toContain(declaredCredentialTypes(currentHead)[0])
    expect(declaredCredentialTypes(null)).toEqual([])
  })

  it("recognizes an automation stored as schedule without trusting arbitrary metadata", () => {
    expect(routineRunOrigin({ triggered_via: "schedule", metadata: { automation_name: "Triage" } })).toEqual({ label: "automation", source: "Triage", chainDepth: undefined })
    expect(routineRunOrigin({ triggered_via: "schedule", metadata: { automation_name: { secret: "x" } } }).label).toBe("schedule")
    expect(routineRunOrigin({})).toEqual({ label: "unknown", source: undefined, chainDepth: undefined })
  })
})
