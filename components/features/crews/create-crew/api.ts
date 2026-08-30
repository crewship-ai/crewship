// Server response shapes for /api/v1/crew-templates and /api/v1/crew-ai-suggest.
// Kept separate so types.ts has zero side-imports beyond this declaration file.
//
// agent_role is AgentRole ("AGENT" | "LEAD") — the set POST /api/v1/agents
// accepts. It used to include the retired "COORDINATOR", which let the wizard
// render a lineup it could not create (#2197). A third value belongs here only
// once the create endpoint accepts it.
//
// Do not expect the compiler to catch a widening. Every renderer of a role
// deliberately declares `agent_role: string` on its own props, because the
// value crosses a network boundary where a union is a claim rather than a
// guarantee — so a third member added to AgentRole type-checks tree-wide
// (verified: `tsc --noEmit`, zero errors). The tripwire is a test —
// "the shared AgentRole union is exactly what the create endpoint accepts" in
// components/features/crews/__tests__/agent-role-badges.test.tsx — and the
// runtime fallback in lib/agent-role.ts is what makes the widening harmless
// until someone reads it.
//
// The import below is type-only and erased at build, so this file still
// contributes no runtime dependency.

import type { AgentRole } from "@/lib/agent-personas"

export interface CrewTemplateAgent {
  name: string
  slug: string
  role_title: string
  agent_role: AgentRole
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
  agent_role: AgentRole
  system_prompt: string
}

export interface AISuggestResponse {
  crew_name: string
  crew_slug: string
  description: string
  agents: AISuggestedAgent[]
}
