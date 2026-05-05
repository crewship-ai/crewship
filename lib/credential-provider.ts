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

// detectFromValue inspects the secret value and returns a suggested
// (provider, name) pair. Used by the paste-first flow: user pastes
// `sk-ant-...` and we pre-populate the form with provider=ANTHROPIC
// and name=ANTHROPIC_API_KEY without them having to type it. Mirrors
// how Doppler / 1Password recognise common token shapes.
//
// Returns nulls when the shape is unfamiliar — caller falls back to
// the generic "type a name" flow.
export function detectFromValue(
  value: string,
): { provider: CredentialProvider; suggestedName: string } | null {
  const v = (value ?? "").trim()
  if (v.length < 8) return null

  // Anthropic — both regular API keys and Claude Code OAuth tokens.
  if (v.startsWith("sk-ant-oat")) return { provider: "ANTHROPIC", suggestedName: "CLAUDE_CODE_OAUTH_TOKEN" }
  if (v.startsWith("sk-ant-")) return { provider: "ANTHROPIC", suggestedName: "ANTHROPIC_API_KEY" }

  // OpenAI — both project keys (sk-proj-) and legacy (sk-).
  if (v.startsWith("sk-proj-")) return { provider: "OPENAI", suggestedName: "OPENAI_API_KEY" }
  if (/^sk-[A-Za-z0-9_-]{20,}$/.test(v)) return { provider: "OPENAI", suggestedName: "OPENAI_API_KEY" }

  // GitHub PATs (classic + fine-grained) and OAuth.
  if (v.startsWith("ghp_") || v.startsWith("gho_") || v.startsWith("ghs_") || v.startsWith("github_pat_")) {
    return { provider: "GITHUB", suggestedName: "GH_TOKEN" }
  }

  // GitLab PAT.
  if (v.startsWith("glpat-")) return { provider: "GITLAB", suggestedName: "GITLAB_TOKEN" }

  // Google API keys.
  if (v.startsWith("AIza") && v.length >= 35) return { provider: "GOOGLE", suggestedName: "GOOGLE_API_KEY" }

  // AWS access key ID.
  if (/^(AKIA|ASIA)[A-Z0-9]{16}$/.test(v)) return { provider: "AWS", suggestedName: "AWS_ACCESS_KEY_ID" }

  // Vercel — `vercel_...` is non-standard; the official prefix is the
  // 24-char hex pattern, but tokens copied via CLI often carry no
  // prefix. We don't false-positive here.

  return null
}
