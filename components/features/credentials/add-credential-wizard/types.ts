// Wizard state for the Crew-styled Add Credential flow.
// Matches the 4-step model from CONNECTIONS.md §4.4.

// CredentialType mirrors the backend's closed enum in
// internal/api/credentials_types.go. Vault types (USERPASS, SSH_KEY,
// CERTIFICATE, GENERIC_SECRET) ship with PR feat/credentials-vault-ui;
// keep these in sync with the Go validator or the wizard will start
// emitting 400s.
export type CredentialType =
  | "AI_CLI_TOKEN"
  | "API_KEY"
  | "CLI_TOKEN"
  | "SECRET"
  | "OAUTH2"
  | "USERPASS"
  | "SSH_KEY"
  | "CERTIFICATE"
  | "GENERIC_SECRET"

// CredentialProvider is also the brand-registry lookup key. The
// VAULT_* IDs are wizard-only buckets for the four runtime-vault
// types — backend stores the literal string, but they're semantically
// generic (no upstream provider). Mapped to dedicated tiles + icons in
// PROVIDER_TILES below and BRAND_REGISTRY in lib/credential-providers.
export type CredentialProvider =
  | "ANTHROPIC" | "OPENAI" | "GOOGLE"
  | "GITHUB" | "GITLAB" | "VERCEL" | "AWS"
  | "CURSOR" | "FACTORY" | "CUSTOM_CLI" | "NONE"
  | "VAULT_USERPASS" | "VAULT_SSH_KEY" | "VAULT_CERTIFICATE" | "VAULT_GENERIC"

export type WizardStep = 1 | 2 | 3 | 4

export type AuthMethod =
  | "setup-token"  // Anthropic: claude setup-token
  | "api-key"      // raw API key
  | "oauth"        // OAuth flow (provider-managed)
  | "pat"          // GitHub/GitLab/Vercel PAT
  | "github-app"   // GitHub App
  | "secret"       // generic secret
  | "userpass"     // username + password (Bitwarden-style)
  | "ssh-key"      // PEM-encoded private key, file-mounted in container
  | "certificate"  // PEM-encoded cert chain, file-mounted in container

export interface WizardState {
  step: WizardStep
  // Step 1
  provider: CredentialProvider | null
  // Step 2
  authMethod: AuthMethod | null
  type: CredentialType
  // Step 3
  value: string
  // username is the cleartext identifier half of a USERPASS credential.
  // Empty for every other type. Kept separate from `value` so the API
  // can store it in its own column (it's an identifier, not a secret).
  username: string
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
  username: "",
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
  // Vault-type tiles — provider buckets for credentials with no
  // upstream service (just raw secrets the agent consumes in-container).
  // Order matters for the picker grid: put the most common ones first.
  { id: "VAULT_USERPASS", label: "Username + Password", defaultMethod: "userpass", defaultType: "USERPASS" },
  { id: "VAULT_SSH_KEY", label: "SSH Key", defaultMethod: "ssh-key", defaultType: "SSH_KEY" },
  { id: "VAULT_CERTIFICATE", label: "TLS Certificate", defaultMethod: "certificate", defaultType: "CERTIFICATE" },
  { id: "VAULT_GENERIC", label: "Generic Secret", defaultMethod: "secret", defaultType: "GENERIC_SECRET" },
  // Kept for backwards compat — older code paths still set provider="NONE"
  // for opaque secrets. The new VAULT_GENERIC tile above is the wizard
  // entry point; this row is only here so existing rows render with a
  // sensible label/icon.
  { id: "NONE", label: "Legacy Secret", defaultMethod: "secret", defaultType: "SECRET" },
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
  // Vault types: the wizard prompts the user for a meaningful name
  // (it becomes the env var prefix the agent sees, e.g. GMAIL_USERNAME
  // / GMAIL_PASSWORD for a USERPASS named "GMAIL"). No sensible default.
  if (method === "userpass" || method === "ssh-key" || method === "certificate") return ""
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
