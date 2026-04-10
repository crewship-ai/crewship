/** Lightweight crew representation used in lists and dropdowns. */
export interface CrewSummary {
  id: string
  name: string
  slug: string
  color: string | null
  icon: string | null
  _count?: { agents: number }
}

/** Lightweight agent representation used in lists, with optional crew context. */
export interface AgentSummary {
  id: string
  name: string
  slug: string
  crew_id: string | null
  avatar_seed: string | null
  avatar_style: string | null
  role_title: string | null
  agent_role: string | null
  // Backend returns crew without id — only name, slug, color, avatar_style
  crew: { name: string; slug: string; color: string | null; avatar_style?: string | null } | null
}

/** A connection between two crews allowing inter-crew agent communication. */
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
