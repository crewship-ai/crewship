// Thin wrappers over the brand registry. Kept around so existing
// callers (`detectProvider`, `detectFromValue`, `detectType`) don't
// need to touch the registry directly. New code should prefer the
// registry-based helpers in `lib/credential-providers/registry`.
//
// The registry is the single source of truth for which providers
// exist, what their brand colour is, what icon they render with, and
// which name keywords or value prefixes auto-match them.

import {
  BRAND_REGISTRY,
  detectBrandFromName,
  detectBrandFromValue,
  type BrandEntry,
} from "@/lib/credential-providers/registry"

// `provider` in the DB is now an open string (any registry key). We
// keep this alias exported so older imports continue to compile, but
// its enum-shaped form is gone — values like "STRIPE" / "NOTION" /
// "LINEAR" are equally valid.
export type CredentialProvider = string

export function detectProvider(name: string): string {
  const hit = detectBrandFromName(name)
  return hit ? hit.key : "NONE"
}

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
// and name=ANTHROPIC_API_KEY without them having to type it.
//
// Falls back to null when the shape is unfamiliar — caller keeps the
// generic "type a name" flow.
export function detectFromValue(
  value: string,
): { provider: string; suggestedName: string } | null {
  const hit = detectBrandFromValue(value)
  if (!hit) return null
  return {
    provider: hit.key,
    suggestedName: defaultEnvVarName(hit),
  }
}

// defaultEnvVarName returns the conventional ENV var name for a brand
// — used when the auto-detect path needs to seed the Name field
// without a user-typed prefix. Falls back to `${KEY}_API_KEY` for
// brands without a hard-coded convention.
function defaultEnvVarName(brand: BrandEntry): string {
  switch (brand.key) {
    case "ANTHROPIC": return "ANTHROPIC_API_KEY"
    case "OPENAI": return "OPENAI_API_KEY"
    case "GOOGLE": return "GOOGLE_API_KEY"
    case "GITHUB": return "GH_TOKEN"
    case "GITLAB": return "GITLAB_TOKEN"
    case "AWS": return "AWS_ACCESS_KEY_ID"
    case "STRIPE": return "STRIPE_API_KEY"
    case "NOTION": return "NOTION_API_KEY"
    case "LINEAR": return "LINEAR_API_KEY"
    case "POSTHOG": return "POSTHOG_API_KEY"
    case "BRAVE": return "BRAVE_API_KEY"
    case "VERCEL": return "VERCEL_TOKEN"
    case "SHOPIFY": return "SHOPIFY_ADMIN_TOKEN"
    case "SLACK": return "SLACK_BOT_TOKEN"
    case "SENDGRID": return "SENDGRID_API_KEY"
    case "RESEND": return "RESEND_API_KEY"
    case "CLOUDFLARE": return "CLOUDFLARE_API_TOKEN"
    case "MAPBOX": return "MAPBOX_ACCESS_TOKEN"
    case "HUGGINGFACE": return "HF_TOKEN"
    case "PERPLEXITY": return "PERPLEXITY_API_KEY"
    case "REPLICATE": return "REPLICATE_API_TOKEN"
    case "DOCKER": return "DOCKER_ACCESS_TOKEN"
    case "NVIDIA": return "NVIDIA_API_KEY"
    default: return `${brand.key.replace(/[^A-Z0-9_]/g, "_")}_API_KEY`
  }
}

// Re-export for convenience.
export { BRAND_REGISTRY, detectBrandFromName, detectBrandFromValue }
