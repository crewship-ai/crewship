/**
 * Where the dashboard (/) sends a signed-in person, or null to stay.
 *
 * An instance administrator who belongs to no workspace goes to Admin: they
 * run the instance, and People & workspaces is where they join or create a
 * workspace. Anyone who has not finished onboarding goes to the wizard,
 * except someone whose role in the current workspace is not OWNER: they were
 * invited into a workspace another person set up, so there is nothing for
 * them to set up there (#4876).
 * "wait" while the workspace or the admin status is still loading.
 */
export function dashboardLanding(s: {
  workspaceId: string | null
  workspaceLoading: boolean
  instanceAdmin: boolean | null
  onboardingCompleted: boolean | null
  /** Caller's role in the current workspace; null when unknown. */
  role?: string | null
}): "/admin" | "/onboarding" | "wait" | null {
  if (s.workspaceLoading || s.instanceAdmin === null) return "wait"
  if (!s.workspaceId && s.instanceAdmin) return "/admin"
  if (s.workspaceId && s.role && s.role !== "OWNER") return null
  if (s.onboardingCompleted === null) return "wait"
  return s.onboardingCompleted ? null : "/onboarding"
}
