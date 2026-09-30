// Part C: an instance administrator who belongs to no workspace lands in
// Admin — where the People page offers to join or create one — instead of the
// first-run wizard that makes them a workspace of their own.
import { describe, it, expect } from "vitest"
import { dashboardLanding } from "@/lib/dashboard-landing"

describe("dashboardLanding", () => {
  it.each([
    [{ workspaceId: null, workspaceLoading: false, instanceAdmin: true, onboardingCompleted: false }, "/admin"],
    [{ workspaceId: null, workspaceLoading: false, instanceAdmin: true, onboardingCompleted: true }, "/admin"],
    [{ workspaceId: null, workspaceLoading: false, instanceAdmin: false, onboardingCompleted: false }, "/onboarding"],
    [{ workspaceId: "ws", workspaceLoading: false, instanceAdmin: true, onboardingCompleted: false }, "/onboarding"],
    [{ workspaceId: "ws", workspaceLoading: false, instanceAdmin: false, onboardingCompleted: true }, null],
    [{ workspaceId: null, workspaceLoading: true, instanceAdmin: true, onboardingCompleted: false }, "wait"],
    [{ workspaceId: null, workspaceLoading: false, instanceAdmin: null, onboardingCompleted: false }, "wait"],
  ])("%o → %s", (input, want) => {
    expect(dashboardLanding(input)).toBe(want)
  })
})
