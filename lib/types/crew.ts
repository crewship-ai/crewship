/** A user who belongs to a crew, with basic profile information. */
export interface CrewMember {
  id: string
  user_id: string
  created_at: string
  user: {
    id: string
    email: string
    full_name: string | null
    avatar_url: string | null
  }
}
