export interface CrewSummary {
  id: string
  name: string
  slug: string
  color: string | null
  icon: string | null
  _count?: { agents: number }
}

export interface AgentSummary {
  id: string
  name: string
  slug: string
  crew_id: string | null
  crew: { id: string; name: string; slug: string; color: string | null } | null
}

export interface CrewConnection {
  id: string
  from_crew_id: string
  from_crew_name: string
  from_crew_slug: string
  to_crew_id: string
  to_crew_name: string
  to_crew_slug: string
  direction: "bidirectional" | "unidirectional"
  status: string
  created_at: string
}
