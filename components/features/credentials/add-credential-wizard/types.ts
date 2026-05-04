// Wizard state for the Crew-styled Add Credential flow.
// Matches the 4-step model from CONNECTIONS.md §4.4.

export type CredentialType = "AI_CLI_TOKEN" | "API_KEY" | "CLI_TOKEN" | "SECRET" | "OAUTH2"

export type CredentialProvider =
  | "ANTHROPIC" | "OPENAI" | "GOOGLE"
  | "GITHUB" | "GITLAB" | "VERCEL" | "AWS"
  | "CURSOR" | "FACTORY" | "CUSTOM_CLI" | "NONE"

export type WizardStep = 1 | 2 | 3 | 4

export type AuthMethod =
  | "setup-token"  // Anthropic: claude setup-token
  | "api-key"      // raw API key
  | "oauth"        // OAuth flow (provider-managed)
  | "pat"          // GitHub/GitLab/Vercel PAT
  | "github-app"   // GitHub App
  | "secret"       // generic secret

export interface WizardState {
  step: WizardStep
  // Step 1
  provider: CredentialProvider | null
  // Step 2
  authMethod: AuthMethod | null
  type: CredentialType
  // Step 3
  value: string
  testResult: { valid: boolean; error?: string } | null
  testing: boolean
  // Step 4
  name: string
  accountLabel: string
  description: string
  scope: "WORKSPACE" | "CREW"
  crewIds: string[]
  expiresAt: string  // YYYY-MM-DD or "" for no expiration override
  agentIds: string[] // pre-assign agents

  submitting: boolean
  error: string | null
}

export const INITIAL: WizardState = {
  step: 1,
  provider: null,
  authMethod: null,
  type: "API_KEY",
  value: "",
  testResult: null,
  testing: false,
  name: "",
  accountLabel: "",
  description: "",
  scope: "WORKSPACE",
  crewIds: [],
  expiresAt: "",
  agentIds: [],
  submitting: false,
  error: null,
}

export const PROVIDER_TILES: { id: CredentialProvider; label: string; defaultMethod: AuthMethod; defaultType: CredentialType }[] = [
  { id: "ANTHROPIC", label: "Anthropic", defaultMethod: "setup-token", defaultType: "AI_CLI_TOKEN" },
  { id: "OPENAI", label: "OpenAI", defaultMethod: "api-key", defaultType: "API_KEY" },
  { id: "GOOGLE", label: "Google", defaultMethod: "api-key", defaultType: "API_KEY" },
  { id: "CURSOR", label: "Cursor", defaultMethod: "api-key", defaultType: "API_KEY" },
  { id: "FACTORY", label: "Factory", defaultMethod: "api-key", defaultType: "API_KEY" },
  { id: "GITHUB", label: "GitHub", defaultMethod: "pat", defaultType: "CLI_TOKEN" },
  { id: "GITLAB", label: "GitLab", defaultMethod: "pat", defaultType: "CLI_TOKEN" },
  { id: "VERCEL", label: "Vercel", defaultMethod: "pat", defaultType: "CLI_TOKEN" },
  { id: "AWS", label: "AWS", defaultMethod: "pat", defaultType: "CLI_TOKEN" },
  { id: "CUSTOM_CLI", label: "Custom CLI", defaultMethod: "pat", defaultType: "CLI_TOKEN" },
  { id: "NONE", label: "Generic Secret", defaultMethod: "secret", defaultType: "SECRET" },
]

// Auth methods available per provider. Empty list = whatever the
// provider tile's defaultMethod says (use defaults, don't show
// alternatives).
export const PROVIDER_AUTH_METHODS: Partial<Record<CredentialProvider, AuthMethod[]>> = {
  ANTHROPIC: ["setup-token", "api-key"],
  GITHUB: ["pat", "github-app"],
  GITLAB: ["pat"],
  VERCEL: ["pat"],
  AWS: ["pat"],
}

// Default env var name (becomes credentials.name) per provider+method.
export function defaultEnvVarName(provider: CredentialProvider, method: AuthMethod): string {
  if (method === "secret") return ""
  switch (provider) {
    case "ANTHROPIC": return "ANTHROPIC_API_KEY"
    case "OPENAI": return "OPENAI_API_KEY"
    case "GOOGLE": return "GOOGLE_API_KEY"
    case "CURSOR": return "CURSOR_API_KEY"
    case "FACTORY": return "FACTORY_API_KEY"
    case "GITHUB": return "GH_TOKEN"
    case "GITLAB": return "GITLAB_TOKEN"
    case "VERCEL": return "VERCEL_TOKEN"
    case "AWS": return "AWS_ACCESS_KEY_ID"
    default: return ""
  }
}
