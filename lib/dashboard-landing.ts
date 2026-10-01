/**
 * Where the dashboard (/) sends a signed-in person, or null to stay.
 *
 * An instance administrator who belongs to no workspace goes to Admin: they
 * run the instance, and People & workspaces is where they join or create a
 * workspace. Anyone who has not finished onboarding goes to the wizard.
 * "wait" while the workspace or the admin status is still loading.
 */
export function dashboardLanding(s: {
  workspaceId: string | null
  workspaceLoading: boolean
  instanceAdmin: boolean | null
  onboardingCompleted: boolean | null
}): "/admin" | "/onboarding" | "wait" | null {
  if (s.workspaceLoading || s.instanceAdmin === null) return "wait"
  if (!s.workspaceId && s.instanceAdmin) return "/admin"
  if (s.onboardingCompleted === null) return "wait"
  return s.onboardingCompleted ? null : "/onboarding"
}
