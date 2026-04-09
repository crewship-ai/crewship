import { z } from "zod"

/** Zod schema for creating a workspace (name + slug). */
export const createWorkspaceSchema = z.object({
  name: z.string().min(2).max(100),
  slug: z.string().min(2).max(50).regex(/^[a-z0-9-]+$/, "Slug must be lowercase alphanumeric with hyphens"),
})

/** Zod schema for partially updating a workspace. */
export const updateWorkspaceSchema = createWorkspaceSchema.partial()

/** Zod schema for creating a crew with optional description, color, icon, and container resource limits. */
export const createCrewSchema = z.object({
  name: z.string().min(2).max(100),
  slug: z.string().min(2).max(50).regex(/^[a-z0-9-]+$/, "Slug must be lowercase alphanumeric with hyphens"),
  description: z.string().max(500).optional(),
  color: z.string().regex(/^#[0-9a-fA-F]{6}$/).optional(),
  icon: z.string().max(10).optional(),
  container_ttl_hours: z.number().int().min(1).max(720).nullable().optional(),
  container_memory_mb: z.number().int().min(512).max(32768).optional(),
  container_cpus: z.number().min(0.5).max(16).optional(),
})

/** Zod schema for partially updating a crew. */
export const updateCrewSchema = createCrewSchema.partial()

/** Zod schema for creating an agent with CLI adapter, LLM config, role, and resource settings. */
export const createAgentSchema = z.object({
  name: z.string().min(2).max(100),
  slug: z.string().min(2).max(50).regex(/^[a-z0-9-]+$/, "Slug must be lowercase alphanumeric with hyphens"),
  crew_id: z.string().min(1),
  description: z.string().max(1000).optional(),
  role_title: z.string().max(100).optional(),
  agent_role: z.enum(["AGENT", "LEAD", "COORDINATOR"]).default("AGENT"),
  lead_mode: z.enum(["active", "passive"]).default("active").optional(),
  cli_adapter: z.enum(["CLAUDE_CODE", "OPENCODE", "CODEX_CLI", "GEMINI_CLI"]).default("CLAUDE_CODE"),
  llm_provider: z.enum(["OPENAI", "ANTHROPIC", "GOOGLE", "OLLAMA"]).optional(),
  llm_model: z.string().max(100).optional(),
  system_prompt: z.string().max(10000).optional(),
  timeout_seconds: z.number().int().min(30).max(7200).default(1800),
  tool_profile: z.enum(["MINIMAL", "CODING", "MESSAGING", "FULL"]).default("CODING"),
})

/** Zod schema for partially updating an agent. */
export const updateAgentSchema = createAgentSchema.partial()

/** Allowed credential type discriminators. */
export const credentialTypeValues = ["AI_CLI_TOKEN", "API_KEY", "SECRET"] as const

/** Allowed credential provider discriminators. */
export const credentialProviderValues = ["ANTHROPIC", "OPENAI", "GOOGLE", "NONE"] as const

/** Allowed credential status values for lifecycle tracking. */
export const credentialStatusValues = ["ACTIVE", "EXPIRED", "RATE_LIMITED", "REVOKED", "ERROR"] as const

/**
 * Zod schema for creating a credential with scope validation.
 * Enforces that CREW scope requires crew_id, and non-SECRET types require a provider.
 */
export const createCredentialSchema = z.object({
  name: z.string().min(1).max(100),
  description: z.string().max(500).optional(),
  value: z.string().min(1),
  type: z.enum(credentialTypeValues).default("SECRET"),
  provider: z.enum(credentialProviderValues).default("NONE"),
  scope: z.enum(["WORKSPACE", "CREW"]).default("WORKSPACE"),
  crew_id: z.string().min(1).optional(),
  account_label: z.string().max(100).optional(),
  account_email: z.string().email().optional(),
  refresh_token: z.string().optional(),
  token_expires_at: z.string().datetime().optional(),
}).refine(
  (data) => {
    if (data.scope === "CREW" && !data.crew_id) return false
    if (data.scope === "WORKSPACE" && data.crew_id) return false
    return true
  },
  {
    message: "crew_id is required for CREW scope and must be absent for WORKSPACE scope",
    path: ["crew_id"],
  }
).refine(
  (data) => {
    if (data.type !== "SECRET" && data.provider === "NONE") return false
    return true
  },
  {
    message: "provider is required for AI_CLI_TOKEN and API_KEY types",
    path: ["provider"],
  }
)

/** Zod schema for inviting a member to a workspace by email with a role. */
export const inviteMemberSchema = z.object({
  email: z.string().email(),
  role: z.enum(["ADMIN", "MANAGER", "MEMBER", "VIEWER"]).default("MEMBER"),
})
