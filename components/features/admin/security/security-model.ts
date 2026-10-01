import type { KeeperLogEntry, KeeperStatus } from "@/app/(dashboard)/admin/types"

/**
 * Admin › Security: the model behind the page. Posture, Keeper and the Keeper
 * reviews were three tabs; they are one page now, and these pure functions
 * decide what it says — which streams exist, what needs attention, how a
 * server setting reads — so the rules are tested once, here.
 */

// ── Sections ────────────────────────────────────────────────────────────────

export type Scope = "instance" | "workspace"

export type SettingsSection = "judge" | "rules" | "defaults" | "workspace-judge" | "background" | "watchdog" | "alerts" | "leases"
export type Section = "overview" | "matrix" | "activity" | SettingsSection

/** Settings sections, in the order an operator meets them, each with the scope
 *  its changes reach. Mirrors the server: the judge, its rules and the
 *  background-check models are instance-wide (authedInstanceMut); the workspace
 *  judge, watchdog, alerts and leases live on the workspace governance row. */
export const SETTINGS: { key: SettingsSection; label: string; scope: Scope; about: string }[] = [
  { key: "judge", label: "Credential judge", scope: "instance", about: "The model that allows, denies or escalates a secret" },
  { key: "rules", label: "Decision rules", scope: "instance", about: "What the judge may use, and when a person confirms" },
  { key: "defaults", label: "Defaults for new workspaces", scope: "instance", about: "What a new workspace's watchdog starts with; existing workspaces do not change" },
  { key: "workspace-judge", label: "Judge per workspace", scope: "workspace", about: "A different judge for some workspaces, including a hosted one" },
  { key: "background", label: "Background checks", scope: "instance", about: "The models behind the scheduled reviews" },
  { key: "watchdog", label: "Watchdog", scope: "workspace", about: "Samples tool calls and raises findings" },
  { key: "alerts", label: "Alerts & approvals", scope: "workspace", about: "Who hears about a finding, who confirms" },
  { key: "leases", label: "Credential leases", scope: "workspace", about: "Whether an approval lasts or expires" },
]

export const SCOPE_LABEL: Record<Scope, string> = { instance: "Instance", workspace: "Per workspace" }
export const SCOPE_HINT: Record<Scope, string> = {
  instance: "Applies to every workspace on this server",
  workspace: "Set for the workspaces ticked in the panel",
}

const SECTIONS = new Set<string>(["overview", "matrix", "activity", ...SETTINGS.map((s) => s.key)])
export const isSection = (s: string | null | undefined): s is Section => !!s && SECTIONS.has(s)

// ── Activity streams ────────────────────────────────────────────────────────

/** One list, five kinds: every keeper_requests row is either a credential
 *  request (access / execute) or one of the four Phase-2 reviews. */
export type Stream = "requests" | "skill_review" | "behavior" | "memory_health" | "negative_learning"

export const STREAMS: { key: Stream; label: string; about: string }[] = [
  { key: "requests", label: "Credential requests", about: "An agent asked for a secret; the judge decided" },
  { key: "skill_review", label: "Skill review", about: "Skills proposed for a crew, checked before use" },
  { key: "behavior", label: "Behavior", about: "Sampled tool calls, while the watchdog is on" },
  { key: "memory_health", label: "Memory health", about: "Daily sweep of AGENT.md and CREW.md" },
  { key: "negative_learning", label: "Negative learning", about: "Lessons proposed from failures" },
]

export function streamOf(e: Pick<KeeperLogEntry, "request_type">): Stream | null {
  const t = e.request_type
  if (t === "access" || t === "execute" || !t) return "requests"
  return (STREAMS.some((s) => s.key === t) ? t : null) as Stream | null
}

export type DecisionFilter = "all" | "ALLOW" | "DENY" | "ESCALATE"

export function filterActivity(entries: KeeperLogEntry[], stream: Stream | "all", decision: DecisionFilter): KeeperLogEntry[] {
  return entries.filter((e) =>
    (stream === "all" || streamOf(e) === stream) &&
    (decision === "all" || (e.decision ?? "PENDING") === decision))
}

export function streamCounts(entries: KeeperLogEntry[]): Record<Stream, number> {
  const out: Record<Stream, number> = { requests: 0, skill_review: 0, behavior: 0, memory_health: 0, negative_learning: 0 }
  for (const e of entries) {
    const s = streamOf(e)
    if (s) out[s]++
  }
  return out
}

// ── Server setup (the old Posture card) ─────────────────────────────────────

export interface PostureWarning { key: string; severity: string; message: string }
export interface Posture {
  environment: string
  encryption_key_configured: boolean
  plaintext_secrets_allowed: boolean
  private_endpoints_ceiling: boolean
  signup_open: boolean
  oauth_configured: boolean
  email_configured: boolean
  rate_limit_disabled: boolean
  rate_limit_effectively_disabled: boolean
  warnings: PostureWarning[]
}

export type Tone = "ok" | "warn" | "bad" | "muted"
export interface SetupRow { label: string; value: string; tone: Tone }

/** How each deploy-time setting reads. An insecure value says so in words —
 *  "true" would read as fine at a glance, which is the whole point lost. */
export function setupRows(p: Posture): SetupRow[] {
  return [
    { label: "Encryption key", value: p.encryption_key_configured ? "configured" : "NOT configured", tone: p.encryption_key_configured ? "ok" : "bad" },
    { label: "Plaintext secrets", value: p.plaintext_secrets_allowed ? "ALLOWED (insecure)" : "refused", tone: p.plaintext_secrets_allowed ? "bad" : "ok" },
    { label: "Private egress", value: p.private_endpoints_ceiling ? "open" : "closed", tone: p.private_endpoints_ceiling ? "warn" : "ok" },
    { label: "Signup", value: p.signup_open ? "OPEN" : "invite-only", tone: p.signup_open ? "bad" : "ok" },
    // A flag production ignores is not an exposure; collapsing the two would
    // invent one.
    p.rate_limit_effectively_disabled
      ? { label: "Rate limiter", value: "DISABLED", tone: "bad" }
      : p.rate_limit_disabled
        ? { label: "Rate limiter", value: "flag set — IGNORED in production", tone: "muted" }
        : { label: "Rate limiter", value: "enabled", tone: "ok" },
    { label: "Email (Resend)", value: p.email_configured ? "configured" : "not configured", tone: p.email_configured ? "ok" : "warn" },
    { label: "OAuth (Google)", value: p.oauth_configured ? "configured" : "not configured", tone: p.oauth_configured ? "ok" : "muted" },
    { label: "Environment", value: p.environment || "(unset)", tone: "muted" },
  ]
}

// ── Needs attention ─────────────────────────────────────────────────────────

export interface Finding {
  key: string
  severity: "high" | "medium" | "info"
  title: string
  /** The server's explanation, whole; the page shows its first sentence. */
  detail: string
  action?: { label: string; href: string }
}

/** Short names for the posture keys (internal/api/admin_security_posture.go).
 *  An unknown key keeps the server's own first sentence as its title. */
const TITLES: Record<string, string> = {
  plaintext_secrets_allowed: "Secrets can be stored unencrypted",
  encryption_key_missing: "No encryption key",
  encryption_key_generated: "Master key stored beside the database",
  rate_limit_disabled: "Rate limiter switched off",
  rate_limit_disabled_ignored_in_prod: "Rate-limit flag ignored in production",
  signup_open: "Anyone can sign up",
  private_endpoints_ceiling_open: "Agents may reach private networks",
  private_endpoints_in_use: "Agents reach private endpoints",
  privileged_credentials_enabled: "Privileged crews receive credentials",
  seed_account_default_password: "Seed account keeps the default password",
  no_backup_recorded: "No backup recorded",
}

const RANK: Record<string, number> = { high: 0, medium: 1, low: 2, info: 3 }

export function firstSentence(text: string): string {
  const m = /^(.+?[.!?])(\s|$)/.exec(text.trim())
  return m ? m[1] : text.trim()
}

/** Everything that needs a person, most pressing first: the Keeper's own state
 *  (off means secrets reach agents ungated; on without an answering judge
 *  means every request fails closed), then the server's posture warnings. */
export function findings(
  posture: Posture | null,
  status: KeeperStatus | null,
  actions: Record<string, { label: string; href: string }> = {},
): Finding[] {
  const out: Finding[] = []
  if (status && !status.enabled) {
    out.push({
      key: "keeper_off", severity: "high", title: "Keeper is off",
      detail: "Secrets reach agents ungated: SECRET credentials are written into each agent's container at start, with no judgement and no audit trail.",
      action: { label: "Turn it on", href: "/admin/security?section=judge" },
    })
  } else if (status && status.enabled && !status.ollama_online) {
    out.push({
      key: "judge_down", severity: "high", title: "The judge is not answering",
      detail: status.ollama_url
        ? "Keeper is on but its model server does not answer, so every credential request is denied."
        : "Keeper is on but no model server is set, so every credential request is denied.",
      action: { label: "Fix the judge", href: "/admin/security?section=judge" },
    })
  }
  for (const w of posture?.warnings ?? []) {
    out.push({
      key: w.key,
      severity: (w.severity === "high" || w.severity === "medium" ? w.severity : "info"),
      title: TITLES[w.key] ?? firstSentence(w.message),
      detail: w.message,
      action: actions[w.key],
    })
  }
  return out.sort((a, b) => (RANK[a.severity] ?? 9) - (RANK[b.severity] ?? 9))
}

/** One line for the top of the page: is Keeper on, does the judge answer. */
export function judgeState(status: KeeperStatus | null): { tone: Tone; text: string } {
  if (!status) return { tone: "muted", text: "Keeper status unknown" }
  if (!status.enabled) return { tone: "bad", text: "Keeper off" }
  if (status.ollama_online) return { tone: "ok", text: `Judge answering · ${status.model || "no model"}` }
  if (!status.ollama_url) return { tone: "bad", text: "No judge server set" }
  if (status.ollama_probed === false) return { tone: "muted", text: "Judge not checked yet" }
  return { tone: "bad", text: "Judge not answering" }
}
