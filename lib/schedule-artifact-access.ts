// Schedule artifact access — whether a schedule's fires reach their agents'
// standing artifacts now (#5339, owner decision for #5356). The server
// computes artifact_access / stopped_reason with the same standing check every
// fire runs; this only turns them into a pill and a sentence. A
// person-standing reason reaches the browser only for the schedule's creator
// and workspace admins; everyone else gets creator_not_in_good_standing.

export interface ScheduleArtifactAccessInput {
  artifact_access?: string
  stopped_reason?: string
}

export interface ScheduleArtifactAccess {
  label: string
  reason: string
}

const restore = "A change to what it runs (its time, inputs or routine) by someone in good standing who may edit schedules restores it."

const reasons: Record<string, string> = {
  creator_access_revoked: "Its creator's access was revoked (sessions revoked, suspended or password recovered) after they saved it.",
  creator_suspended: "Its creator is suspended.",
  creator_not_member: "Its creator is no longer a member of this workspace (or left and rejoined).",
  creator_role: "Its creator's role can no longer create schedules.",
  creator_restricted: "Its creator is now a restricted member.",
  agent_created: "An agent created or last changed it, and an agent cannot vouch for a schedule.",
  creator_unknown: "It was saved before its creator was recorded.",
  creator_not_in_good_standing: "Its creator is no longer in good standing.",
}

/** null while artifact access is active, or when the server reports none (flag off). */
export function scheduleArtifactAccess(s: ScheduleArtifactAccessInput): ScheduleArtifactAccess | null {
  if (s.artifact_access !== "stopped") return null
  const why = (s.stopped_reason && reasons[s.stopped_reason]) || "Its creator no longer vouches for it."
  return { label: "artifact access stopped", reason: `Runs start, but reach no artifacts. ${why} ${restore}` }
}
