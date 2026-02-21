export interface PeerConversation {
  id: string
  from_name: string
  from_slug: string
  to_name: string
  to_slug: string
  question: string
  response: string | null
  status: "RUNNING" | "COMPLETED" | "FAILED"
  duration_ms: number | null
  escalated: boolean
  created_at: string
  finished_at: string | null
}
