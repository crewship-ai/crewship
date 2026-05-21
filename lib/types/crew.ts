/** A user who belongs to a crew, with basic profile information.
 *
 *  Per-crew role override (Patch M1) is optional — when omitted the
 *  user inherits their workspace role inside this crew. When set, it
 *  can only ELEVATE the workspace role, never demote (the effective
 *  role helper in backend takes the max of the two ranks).
 */
export interface CrewMember {
  id: string
  user_id: string
  created_at: string
  /** Per-crew role override. null / undefined = inherit workspace role. */
  role?: CrewMemberRole | null
  user: {
    id: string
    email: string
    full_name: string | null
    avatar_url: string | null
  }
}

/** Closed set of workspace + per-crew role values. Mirrors the
 *  roleRank ordering in internal/api/helpers.go. */
export type CrewMemberRole = "OWNER" | "ADMIN" | "MANAGER" | "MEMBER" | "VIEWER"
