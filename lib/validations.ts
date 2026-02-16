import { z } from "zod"

export const createOrgSchema = z.object({
  name: z.string().min(2).max(100),
  slug: z.string().min(2).max(50).regex(/^[a-z0-9-]+$/, "Slug must be lowercase alphanumeric with hyphens"),
})

export const updateOrgSchema = createOrgSchema.partial()

export const createTeamSchema = z.object({
  name: z.string().min(2).max(100),
  slug: z.string().min(2).max(50).regex(/^[a-z0-9-]+$/, "Slug must be lowercase alphanumeric with hyphens"),
  description: z.string().max(500).optional(),
  color: z.string().regex(/^#[0-9a-fA-F]{6}$/).optional(),
  icon: z.string().max(10).optional(),
})

export const updateTeamSchema = createTeamSchema.partial()

export const createAgentSchema = z.object({
  name: z.string().min(2).max(100),
  slug: z.string().min(2).max(50).regex(/^[a-z0-9-]+$/, "Slug must be lowercase alphanumeric with hyphens"),
  team_id: z.string().uuid(),
  description: z.string().max(1000).optional(),
  role_title: z.string().max(100).optional(),
  agent_role: z.enum(["WORKER", "LEADER", "DIRECTOR"]).default("WORKER"),
  cli_adapter: z.enum(["CLAUDE_CODE", "OPENCODE", "CODEX_CLI", "GEMINI_CLI"]).default("CLAUDE_CODE"),
  llm_provider: z.enum(["OPENAI", "ANTHROPIC", "GOOGLE", "OLLAMA"]).optional(),
  llm_model: z.string().max(100).optional(),
  system_prompt: z.string().max(10000).optional(),
  temperature: z.number().min(0).max(2).default(0.7),
  max_tokens: z.number().int().positive().optional(),
  timeout_seconds: z.number().int().min(30).max(7200).default(1800),
  tool_profile: z.enum(["MINIMAL", "CODING", "MESSAGING", "FULL"]).default("CODING"),
})

export const updateAgentSchema = createAgentSchema.partial()

export const createCredentialSchema = z.object({
  name: z.string().min(1).max(100),
  description: z.string().max(500).optional(),
  value: z.string().min(1),
  scope: z.enum(["ORGANIZATION", "TEAM"]).default("ORGANIZATION"),
  team_id: z.string().uuid().optional(),
})

export const inviteMemberSchema = z.object({
  email: z.string().email(),
  role: z.enum(["ADMIN", "MANAGER", "MEMBER", "VIEWER"]).default("MEMBER"),
})
