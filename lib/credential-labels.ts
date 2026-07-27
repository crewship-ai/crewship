// Helpers for surfacing a routine's required vault credentials in the UI —
// the credential counterpart to lib/integration-labels.ts. A run refused for
// a missing credential returns 422 + Problem Details with a
// `missing_credentials` array of vault TYPES (api_key, ai_cli_token, …).

import { extractProblemStringList } from "@/lib/problem-details"

// Human labels for the vault credential types a routine can declare. Anything
// not listed falls back to per-token Title Case (so a future "webhook_secret"
// → "Webhook Secret") — good enough for the long tail.
const CREDENTIAL_TYPE_LABELS: Record<string, string> = {
  api_key: "API key",
  ai_cli_token: "CLI token",
  cli_token: "CLI token",
  secret: "secret",
  oauth2: "OAuth credential",
  endpoint_url: "endpoint URL",
}

/** credentialTypeLabel renders a vault credential type for human display. */
export function credentialTypeLabel(type: string): string {
  if (!type) return ""
  const key = type.trim().toLowerCase()
  if (CREDENTIAL_TYPE_LABELS[key]) return CREDENTIAL_TYPE_LABELS[key]
  return key
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map((tok) => tok.charAt(0).toUpperCase() + tok.slice(1))
    .join(" ")
}

/** extractMissingCredentials reads the `missing_credentials` extension member
 *  from a parsed Problem Details body. Returns a de-duplicated, trimmed,
 *  string-only list; `[]` when absent or malformed — callers use a non-empty
 *  result to switch from the generic "run failed" toast to the
 *  connect-this-credential UX. */
export function extractMissingCredentials(body: unknown): string[] {
  return extractProblemStringList(body, "missing_credentials")
}
