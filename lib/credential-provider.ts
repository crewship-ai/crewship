// Provider auto-detection from credential names. Provider is purely
// metadata for icon + grouping in the FE — Crewship is a generic
// secret store, not a curated integrations product, so we can never
// enumerate every service the user might add. We instead match
// well-known prefixes and fall back to NONE (generic 🔑) for anything
// else, the same heuristic Doppler / Vercel use.
//
// Adding a new entry here is purely additive: the credential keeps
// working with provider=NONE before the prefix is registered. New
// prefixes should be UPPER_SNAKE and end in "_" so we never match
// inside a word.

export type CredentialProvider =
  | "ANTHROPIC" | "OPENAI" | "GOOGLE"
  | "GITHUB" | "GITLAB" | "VERCEL" | "AWS"
  | "CURSOR" | "FACTORY" | "CUSTOM_CLI" | "NONE"

const PREFIX_RULES: Array<{ prefix: string; provider: CredentialProvider }> = [
  { prefix: "ANTHROPIC_", provider: "ANTHROPIC" },
  { prefix: "CLAUDE_", provider: "ANTHROPIC" },
  { prefix: "OPENAI_", provider: "OPENAI" },
  { prefix: "OAI_", provider: "OPENAI" },
  { prefix: "GOOGLE_", provider: "GOOGLE" },
  { prefix: "GEMINI_", provider: "GOOGLE" },
  { prefix: "GH_", provider: "GITHUB" },
  { prefix: "GITHUB_", provider: "GITHUB" },
  { prefix: "GL_", provider: "GITLAB" },
  { prefix: "GITLAB_", provider: "GITLAB" },
  { prefix: "VERCEL_", provider: "VERCEL" },
  { prefix: "AWS_", provider: "AWS" },
  { prefix: "CURSOR_", provider: "CURSOR" },
  { prefix: "FACTORY_", provider: "FACTORY" },
]

// detectProvider takes an env-var-style name and returns the most
// likely provider. Case-insensitive; an empty/short name returns NONE.
export function detectProvider(name: string): CredentialProvider {
  const upper = (name ?? "").trim().toUpperCase()
  if (upper.length === 0) return "NONE"
  for (const rule of PREFIX_RULES) {
    if (upper.startsWith(rule.prefix)) return rule.provider
  }
  return "NONE"
}

// detectType infers the credential type from the name suffix. Used to
// pre-fill the type in the simple Add flow without making the user
// pick from a dropdown for the common case.
export function detectType(
  name: string,
): "AI_CLI_TOKEN" | "API_KEY" | "CLI_TOKEN" | "SECRET" | "OAUTH2" {
  const upper = (name ?? "").trim().toUpperCase()
  if (upper.includes("OAUTH")) return "OAUTH2"
  if (upper.endsWith("_API_KEY") || upper.includes("_API_KEY_")) return "API_KEY"
  if (upper.endsWith("_TOKEN") || upper.includes("_TOKEN_")) return "CLI_TOKEN"
  return "SECRET"
}
