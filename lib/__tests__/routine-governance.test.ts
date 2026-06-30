import { describe, it, expect } from "vitest"
import {
  normalizeRoutineStatus,
  routineStatusBadge,
  roleAtLeast,
  canApproveRoutine,
  canKillRoutine,
  runDisabledReason,
} from "@/lib/routine-governance"
import { STATUS_BADGE_CLASSES, STATUS_DOT_CLASSES } from "@/lib/colors"

describe("normalizeRoutineStatus", () => {
  it("passes through proposed and disabled", () => {
    expect(normalizeRoutineStatus("proposed")).toBe("proposed")
    expect(normalizeRoutineStatus("disabled")).toBe("disabled")
  })
  it("treats active / unknown / absent as active", () => {
    expect(normalizeRoutineStatus("active")).toBe("active")
    expect(normalizeRoutineStatus("weird")).toBe("active")
    expect(normalizeRoutineStatus(undefined)).toBe("active")
    expect(normalizeRoutineStatus(null)).toBe("active")
  })
})

describe("routineStatusBadge", () => {
  it("maps a proposed routine to the shared AWAITING_APPROVAL palette", () => {
    const b = routineStatusBadge("proposed")
    expect(b).not.toBeNull()
    expect(b!.label).toBe("Awaiting approval")
    // Colors come straight from the shared palette (violet), not a
    // hand-rolled amber map — matches the approval pills everywhere else.
    expect(b!.className).toBe(STATUS_BADGE_CLASSES.AWAITING_APPROVAL)
    expect(b!.dot).toBe(STATUS_DOT_CLASSES.AWAITING_APPROVAL)
    expect(b!.className).toContain("violet")
  })
  it("maps a disabled routine to the shared muted (SKIPPED) palette", () => {
    const b = routineStatusBadge("disabled")
    expect(b).not.toBeNull()
    expect(b!.label).toBe("Disabled")
    expect(b!.className).toBe(STATUS_BADGE_CLASSES.SKIPPED)
    expect(b!.dot).toBe(STATUS_DOT_CLASSES.SKIPPED)
    expect(b!.className).toContain("text-muted-foreground")
  })
  it("returns null for active / unknown (no badge)", () => {
    expect(routineStatusBadge("active")).toBeNull()
    expect(routineStatusBadge(undefined)).toBeNull()
    expect(routineStatusBadge("weird")).toBeNull()
  })
})

describe("roleAtLeast", () => {
  it("respects the OWNER>ADMIN>MANAGER>MEMBER>VIEWER hierarchy", () => {
    expect(roleAtLeast("OWNER", "MANAGER")).toBe(true)
    expect(roleAtLeast("MANAGER", "MANAGER")).toBe(true)
    expect(roleAtLeast("MEMBER", "MANAGER")).toBe(false)
    expect(roleAtLeast("VIEWER", "ADMIN")).toBe(false)
  })
  it("treats unknown / null role as below everything", () => {
    expect(roleAtLeast(null, "VIEWER")).toBe(false)
    expect(roleAtLeast("nonsense", "VIEWER")).toBe(false)
  })
})

describe("canApproveRoutine (MANAGER+)", () => {
  it.each([
    ["OWNER", true],
    ["ADMIN", true],
    ["MANAGER", true],
    ["MEMBER", false],
    ["VIEWER", false],
    [null, false],
  ] as const)("%s -> %s", (role, expected) => {
    expect(canApproveRoutine(role)).toBe(expected)
  })
})

describe("canKillRoutine (OWNER/ADMIN)", () => {
  it.each([
    ["OWNER", true],
    ["ADMIN", true],
    ["MANAGER", false],
    ["MEMBER", false],
    ["VIEWER", false],
    [null, false],
  ] as const)("%s -> %s", (role, expected) => {
    expect(canKillRoutine(role)).toBe(expected)
  })
})

describe("runDisabledReason", () => {
  it("returns null for active (runnable)", () => {
    expect(runDisabledReason("active")).toBeNull()
    expect(runDisabledReason(undefined)).toBeNull()
  })
  it("explains why proposed/disabled routines cannot run", () => {
    expect(runDisabledReason("proposed")).toMatch(/awaiting approval/i)
    expect(runDisabledReason("disabled")).toMatch(/disabled/i)
  })
})
