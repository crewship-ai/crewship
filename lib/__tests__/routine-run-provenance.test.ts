import { describe, expect, it } from "vitest"
import { declaredCredentialTypes, readableOriginSource, routineRunOrigin } from "../routine-run-provenance"

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

describe("readableOriginSource", () => {
  // "Scheduled start · psched_cmuh2qiqm0003e1088340" put a database id in the
  // sentence; a name stays, an id does not.
  it.each([
    ["psched_cmuh2qiqm0003e1088340", undefined],
    ["cmuh2qiqm0003e1088340", undefined],
    ["run_cmulpp3pt0001c39add54", undefined],
    ["0f8fad5b-d9cb-469f-a165-70867728950e", undefined],
    ["Triage", "Triage"],
    ["Nightly CI — triage", "Nightly CI — triage"],
    ["github-push", "github-push"],
    [undefined, undefined],
  ])("%s → %s", (source, expected) => {
    expect(readableOriginSource(source)).toBe(expected)
  })
})
