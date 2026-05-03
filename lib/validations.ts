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

/**
 * Zod schema for creating an agent with CLI adapter, LLM config, role, and resource settings.
 *
 * Role-based crew_id rules (match backend validation in internal/api/agents.go):
 *   - LEAD         → crew_id is REQUIRED
 *   - COORDINATOR  → crew_id MUST NOT be present (workspace-level agent)
 *   - AGENT        → crew_id is required by convention (workers live in a crew)
 */
export const createAgentSchema = z.object({
  name: z.string().min(2).max(100),
  slug: z.string().min(2).max(50).regex(/^[a-z0-9-]+$/, "Slug must be lowercase alphanumeric with hyphens"),
  crew_id: z.string().min(1).optional(),
  description: z.string().max(1000).optional(),
  role_title: z.string().max(100).optional(),
  // COORDINATOR role is deprecated (2026-04-16); see docs/guides/coordinator.mdx.
  // Kept in the enum for backward compat so existing agents still validate.
  agent_role: z.enum(["AGENT", "LEAD", "COORDINATOR"]).default("AGENT"),
  lead_mode: z.enum(["active", "passive"]).default("active").optional(),
  cli_adapter: z.enum(["CLAUDE_CODE", "OPENCODE", "CODEX_CLI", "GEMINI_CLI", "CURSOR_CLI", "FACTORY_DROID"]).default("CLAUDE_CODE"),
  llm_provider: z.enum(["OPENAI", "ANTHROPIC", "GOOGLE", "CURSOR", "FACTORY", "OLLAMA"]).optional(),
  llm_model: z.string().max(100).optional(),
  system_prompt: z.string().max(10000).optional(),
  timeout_seconds: z.number().int().min(30).max(7200).default(1800),
  tool_profile: z.enum(["MINIMAL", "CODING", "MESSAGING", "FULL"]).default("CODING"),
}).superRefine((data, ctx) => {
  if (data.agent_role === "LEAD" && !data.crew_id) {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ["crew_id"],
      message: "LEAD role requires crew_id",
    })
  }
  if (data.agent_role === "COORDINATOR" && data.crew_id) {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ["crew_id"],
      message: "COORDINATOR role must not have crew_id",
    })
  }
  if (data.agent_role === "AGENT" && !data.crew_id) {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ["crew_id"],
      message: "AGENT role requires crew_id",
    })
  }
})

/**
 * Zod schema for partially updating an agent.
 * Can't chain `.partial()` onto a schema with `.superRefine()`, so we redefine
 * the base shape as optional and keep the same role/crew_id conditional
 * validation (applied only when agent_role is being changed).
 */
export const updateAgentSchema = z.object({
  name: z.string().min(2).max(100).optional(),
  slug: z.string().min(2).max(50).regex(/^[a-z0-9-]+$/, "Slug must be lowercase alphanumeric with hyphens").optional(),
  crew_id: z.string().min(1).optional(),
  description: z.string().max(1000).optional(),
  role_title: z.string().max(100).optional(),
  agent_role: z.enum(["AGENT", "LEAD", "COORDINATOR"]).optional(),
  lead_mode: z.enum(["active", "passive"]).optional(),
  cli_adapter: z.enum(["CLAUDE_CODE", "OPENCODE", "CODEX_CLI", "GEMINI_CLI", "CURSOR_CLI", "FACTORY_DROID"]).optional(),
  llm_provider: z.enum(["OPENAI", "ANTHROPIC", "GOOGLE", "CURSOR", "FACTORY", "OLLAMA"]).optional(),
  llm_model: z.string().max(100).optional(),
  system_prompt: z.string().max(10000).optional(),
  timeout_seconds: z.number().int().min(30).max(7200).optional(),
  tool_profile: z.enum(["MINIMAL", "CODING", "MESSAGING", "FULL"]).optional(),
}).superRefine((data, ctx) => {
  // Only validate role ↔ crew_id when agent_role is being changed.
  // Partial updates that omit agent_role rely on backend re-validation.
  if (data.agent_role === "LEAD" && !data.crew_id) {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ["crew_id"],
      message: "LEAD role requires crew_id",
    })
  }
  if (data.agent_role === "COORDINATOR" && data.crew_id) {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ["crew_id"],
      message: "COORDINATOR role must not have crew_id",
    })
  }
})

/** Allowed credential type discriminators. */
export const credentialTypeValues = ["AI_CLI_TOKEN", "API_KEY", "SECRET"] as const

/** Allowed credential provider discriminators. Mirrors prisma/schema.prisma
 *  CredentialProvider enum + the multi-CLI wave additions (CURSOR, FACTORY).
 *  When OpenCode users need OpenRouter/xAI/Groq/DeepSeek keys, they add them
 *  as SECRET type with the env var name set manually. */
export const credentialProviderValues = ["ANTHROPIC", "OPENAI", "GOOGLE", "CURSOR", "FACTORY", "NONE"] as const

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
