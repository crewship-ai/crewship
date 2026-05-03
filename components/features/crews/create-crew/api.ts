// Server response shapes for /api/v1/crew-templates and /api/v1/crew-ai-suggest.
// Kept separate so types.ts has zero side-imports beyond this declaration file.

export interface CrewTemplateAgent {
  name: string
  slug: string
  role_title: string
  agent_role: "AGENT" | "LEAD" | "COORDINATOR"
  cli_adapter: string
  llm_provider: string
  llm_model: string
  tool_profile: string
  system_prompt: string
  skills?: string[]
}

export interface CrewTemplate {
  id: string
  name: string
  slug: string
  description: string | null
  icon: string | null
  color: string | null
  category: string
  agents: CrewTemplateAgent[]
  is_builtin: boolean
  created_at: string
}

export interface AISuggestedAgent {
  name: string
  slug: string
  role_title: string
  agent_role: "AGENT" | "LEAD" | "COORDINATOR"
  system_prompt: string
}

export interface AISuggestResponse {
  crew_name: string
  crew_slug: string
  description: string
  agents: AISuggestedAgent[]
}
