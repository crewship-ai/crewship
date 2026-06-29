// Helpers for surfacing a routine's required third-party integrations
// (Composio connector slugs like "github", "slack") in the UI.
//
// Two pure functions live here so the rendering layer stays declarative
// and the logic is unit-testable in isolation:
//
//   integrationLabel(slug)        — "github" → "GitHub" for chips/toasts.
//   extractMissingIntegrations(b) — pull the `missing_integrations`
//                                   extension member out of an RFC 7807
//                                   Problem Details body returned by a
//                                   refused run (HTTP 422).

// Brand-correct casings that a naive title-case would get wrong. Keyed by
// the lowercased slug. Anything not here falls back to per-token
// capitalisation (so "hubspot" → "Hubspot", "google-calendar" → "Google
// Calendar"), which is good enough for the long tail of connectors.
const BRAND_LABELS: Record<string, string> = {
  github: "GitHub",
  gitlab: "GitLab",
  slack: "Slack",
  notion: "Notion",
  linear: "Linear",
  jira: "Jira",
  gmail: "Gmail",
  googledrive: "Google Drive",
  google_drive: "Google Drive",
  googlecalendar: "Google Calendar",
  google_calendar: "Google Calendar",
  googlesheets: "Google Sheets",
  google_sheets: "Google Sheets",
  hubspot: "HubSpot",
  salesforce: "Salesforce",
  stripe: "Stripe",
  discord: "Discord",
  asana: "Asana",
  trello: "Trello",
  zendesk: "Zendesk",
  airtable: "Airtable",
  dropbox: "Dropbox",
  openai: "OpenAI",
  pagerduty: "PagerDuty",
  sendgrid: "SendGrid",
  twilio: "Twilio",
  youtube: "YouTube",
}

/** integrationLabel renders a connector slug for human display. Known
 *  brands get their canonical casing; unknown slugs fall back to
 *  per-token Title Case split on `-` / `_` / spaces. */
export function integrationLabel(slug: string): string {
  if (!slug) return ""
  const key = slug.trim().toLowerCase()
  if (BRAND_LABELS[key]) return BRAND_LABELS[key]
  return key
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map((tok) => tok.charAt(0).toUpperCase() + tok.slice(1))
    .join(" ")
}

/** extractMissingIntegrations reads the `missing_integrations` extension
 *  member from a parsed Problem Details body (or any object). Returns a
 *  de-duplicated, trimmed, string-only list; `[]` when the field is
 *  absent or malformed — callers use a non-empty result to switch from
 *  the generic "run failed" toast to the integration-block UX. */
export function extractMissingIntegrations(body: unknown): string[] {
  if (!body || typeof body !== "object") return []
  const raw = (body as Record<string, unknown>)["missing_integrations"]
  if (!Array.isArray(raw)) return []
  const seen = new Set<string>()
  const out: string[] = []
  for (const item of raw) {
    if (typeof item !== "string") continue
    const slug = item.trim()
    if (!slug || seen.has(slug)) continue
    seen.add(slug)
    out.push(slug)
  }
  return out
}
