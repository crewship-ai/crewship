export interface ActivityItem {
  id: string
  type: "assignment" | "peer_conversation" | "escalation"
  status: string
  summary: string
  detail: string | null
  from_name: string
  from_slug: string
  to_name: string | null
  to_slug: string | null
  crew_name: string
  crew_slug: string
  crew_color: string | null
  created_at: string
}
