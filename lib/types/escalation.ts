export interface Escalation {
  id: string
  from_name: string
  from_slug: string
  reason: string
  context: string | null
  peer_conversation_id: string | null
  status: "PENDING" | "RESOLVED"
  resolution: string | null
  resolved_at: string | null
  created_at: string
}
