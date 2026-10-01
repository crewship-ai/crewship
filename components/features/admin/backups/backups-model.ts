/**
 * Admin › Backups and Data retention: the wire types (PLAN.md §API, typed
 * ahead of the backend tracks so they can match them) and every piece of
 * logic that is not drawing — the contents dependency map, the 14-night
 * strip, next-run arithmetic, the scope in the URL, and formatting.
 *
 * Nothing here touches React or the network, so all of it is unit-tested
 * on its own (__tests__/backups-model.test.ts).
 */

import type { BackupsSection } from "@/app/(dashboard)/admin/navigation"

// ─── Wire types ─────────────────────────────────────────────────────────────

export type BackupScope = "instance" | "workspaces"
/** 0 none, 1 checksum, 2 contents checked, 3 test restore. Never collapsed. */
export type ProofLevel = 0 | 1 | 2 | 3
export type DrillResult = "ok" | "partial" | "failed"
export type Tone = "ok" | "warn" | "bad" | "muted"

/** One row of the Overview status card, composed by the server. */
export interface StatusRow {
  value: string
  detail?: string | null
  detail_tone?: Tone | null
}

/**
 * The headline of the status card. The server picks the verdict; the words
 * are the UI's (verdictHeadline), so a verdict can never be phrased up.
 */
export type RestoreVerdict = "verified" | "partial" | "failed" | "contents_checked" | "checksum_only" | "none"

export interface OverviewStatus {
  /** "Complete recovery" or the plan that covers the selection. */
  label: string
  verdict: RestoreVerdict
  /** One sentence under the headline: what is missing, if anything. */
  summary?: string | null
  protects: StatusRow
  how_often: StatusRow
  where: StatusRow
  how_long: StatusRow
  really_restored: StatusRow
  /** False until an off-site copy has been uploaded and checked. */
  offsite_verified: boolean
}

export type AttentionAction = "see_which" | "keys" | "storage" | "schedules" | "back_up_now" | "history" | "recovery"

export interface AttentionItem {
  id: string
  severity: "bad" | "warn"
  title: string
  detail: string
  action?: { kind: AttentionAction; label: string; workspace_id?: string | null; run_id?: string | null } | null
}

/** incomplete: a backup was created that night but recorded gaps — never a green ok. */
export type NightStatus = "ok" | "incomplete" | "skipped" | "late" | "failed" | "none"

export interface Night {
  /** YYYY-MM-DD in the instance's timezone. */
  date: string
  status: NightStatus
  proof: ProofLevel
  detail?: string | null
}

export interface SpaceInfo {
  backups_bytes: number
  free_bytes: number
  total_bytes: number
  staging_need_bytes: number
  restore_need_bytes: number
  /** The space floor in force (CREWSHIP_BACKUP_MIN_FREE_PERCENT, default 10). */
  min_free_percent?: number
  /** Why the next run would not start for lack of room, and what to do; null when it would. */
  refusal?: string | null
}

export interface WorkspaceCoverage {
  workspace_id: string
  name: string
  last_backup_at: string | null
  status: "ok" | "warn" | "bad"
  plan?: string | null
  proof: ProofLevel
}

/** GET /api/v1/admin/instance/backups/overview?scope=&ws= */
export interface OverviewResponse {
  status: OverviewStatus
  needs_attention: AttentionItem[]
  nights: Night[]
  space: SpaceInfo
  /** Present for scope=workspaces: one row per selected workspace. */
  workspaces?: WorkspaceCoverage[]
  /** For the scope bar: "5 workspaces, 7 users, instance settings, …". */
  instance_summary?: string | null
}

export type RunStatus = "running" | "done" | "incomplete" | "failed" | "interrupted" | "skipped"
export type RunTrigger = "schedule" | "manual" | "catchup" | "drill"

export interface RunPhase {
  name: string
  started_at?: string | null
  ended_at?: string | null
  status: "pending" | "running" | "done" | "failed" | "skipped"
  detail?: string | null
}

export interface IncompleteItem {
  kind: "attachment_missing" | "memory_blob_missing" | "container_missing" | "attachment_conflict" | string
  count: number
  workspace_id?: string | null
  detail: string
}

/** One row of GET /api/v1/admin/instance/backups/runs (a run and its bundle). */
export interface BackupRun {
  id: string
  plan_id: string | null
  plan_name: string | null
  trigger: RunTrigger
  /** A manual run's note, e.g. "before upgrade". */
  note?: string | null
  scope: BackupScope
  workspace_id: string | null
  workspace_name: string | null
  kind: "full" | "custom" | "environments"
  status: RunStatus
  /** An interrupted run that was retried and finished. */
  retried_at?: string | null
  phases: RunPhase[]
  bundle_path: string | null
  size_bytes: number | null
  error?: string | null
  incomplete: IncompleteItem[]
  started_at: string
  ended_at: string | null
  proof_level: ProofLevel
  drill_result?: DrillResult | null
  drill_at?: string | null
  drill_note?: string | null
  pinned: boolean
  recipients: string[]
  format_version?: number | null
  /** How this server can restore it: directly, or through a converter first. */
  restorable?: "direct" | "converter" | "unsupported" | null
  /** Set on rows that came from the legacy per-workspace bundle list. */
  legacy?: boolean
  /** How long writes were held for an instance run's consistent copy. */
  hold_ms?: number | null
}

export type CategoryKey = "agents" | "memory" | "chats" | "att" | "routines" | "journal" | "creds" | "pages" | "files" | "env"
export type Preset = "complete" | "workspace" | "custom"
export type Cadence = "daily" | "weekly" | "monthly" | "custom"
export type EnvCadence = "every" | "weekly" | "monthly"

/** GET/POST/PUT /api/v1/admin/instance/backups/plans[/{id}] */
export interface BackupPlan {
  id: string
  name: string
  preset: Preset
  scope: BackupScope
  /** For scope=workspaces; empty means every workspace. */
  workspace_ids: string[]
  /** For preset=custom: the categories chosen (dependencies are added by the server). */
  contents: CategoryKey[]
  env_mode: "files" | "complete"
  cadence: Cadence
  /** "03:00" */
  time_of_day: string
  /** 0 = Sunday. */
  weekday: number | null
  /** null with cadence=monthly means the first Sunday of the month. */
  monthday: number | null
  cron_expr: string | null
  timezone: string
  env_cadence: EnvCadence
  keep_min: number
  keep_daily: number
  keep_weekly: number
  keep_monthly: number
  destinations: string[]
  recipient_ids: string[]
  busy_wait_minutes: number
  busy_retry_minutes: number
  hold_cap_minutes: number
  stale_alert_hours: number
  enabled: boolean
  next_run_at: string | null
  last_run_at: string | null
}

/** GET …/plans/{id}/next?n=5 */
export interface NextRunsResponse { runs: { at: string; environments: boolean }[] }

export type CalendarEntryStatus = "done" | "skipped" | "catchup" | "failed" | "planned"
/** GET …/plans/{id}/calendar?from=&to= */
export interface CalendarResponse {
  days: { date: string; entries: { at: string; kind: "data" | "environments"; status: CalendarEntryStatus }[] }[]
}

/** POST …/plans/preview-contents */
export interface PreviewContentsResponse {
  included: CategoryKey[]
  required: { key: CategoryKey; because: CategoryKey[] }[]
  excluded: CategoryKey[]
}

export interface Destination {
  id: string
  kind: "local" | "s3" | "drive"
  label: string
  path?: string | null
  used_bytes?: number | null
  /** Counted as a copy only once an upload has been checked. */
  verified: boolean
  /** False while this server cannot use the kind yet (shown as later). */
  available: boolean
}

export interface AlertEvents { failed: boolean; incomplete: boolean; stale: boolean; offsite: boolean; drill: boolean }

/** The outcome of the newest backup alert sent to a notification channel. */
export interface AlertDelivery {
  status: "pending" | "sent" | "failed"
  error: string | null
  at: string
  incident_id: string
}

/** A notification channel backup alerts can go to (the Tell picker). */
export interface AlertChannel {
  id: string
  /** "Slack · Platform", "Webhook hooks.example.com · Platform". */
  name: string
  kind: "chat" | "push" | "incident" | "email" | "webhook"
  provider: string
  workspace_id: string
  workspace_name: string
  last_delivery: AlertDelivery | null
}

/** POST /api/v1/admin/instance/backups/settings/test-alert */
export interface AlertTestResult {
  ok: boolean
  channel_id: string
  channel: string
  error: string | null
  sent_at: string
}

/** GET/PUT /api/v1/admin/instance/backups/settings */
export interface BackupSettings {
  /** disk_mbps / upload_mbps: 0 is no limit. */
  limits: { concurrency: number; cpu_cores: number; disk_mbps: number; upload_mbps: number }
  heartbeat_url: string | null
  /** The last heartbeat ping: when, and whether it answered 2xx. */
  heartbeat_last_at?: string | null
  heartbeat_last_ok?: boolean | null
  heartbeat_last_error?: string | null
  recovery_kit_enabled: boolean
  /** Notification channel ids every alert also goes to, beside the inboxes. */
  channels: string[]
  /** The channels that can carry backup alerts now (absent on older servers). */
  available_channels?: AlertChannel[]
  /** Per chosen channel: still available, and how its newest alert went. */
  channel_status?: { id: string; available: boolean; last_delivery: AlertDelivery | null }[]
  events: AlertEvents
  stale_alert_hours: number
  drill_reminder: "weekly" | "monthly" | "off"
  destinations: Destination[]
  /** How many instance admins receive alerts. */
  instance_admins: number
  /** Where the local copies live, e.g. ~/.crewship/backups. */
  local_path?: string | null
}

/** GET/POST/DELETE /api/v1/admin/instance/backups/recipients[/{id}] */
export interface BackupRecipient {
  id: string
  name: string
  public_key: string
  holder: string
  created_at: string
  /** "SHA256:1a2b3c4d5e6f7a8b", for the printed recovery sheet. */
  fingerprint?: string
  /** Plans that encrypt to this key; a key in use cannot be removed. */
  used_by?: string[]
}

/**
 * GET/POST/DELETE /api/v1/admin/instance/backups/destinations[/{id}] — an
 * S3-compatible store. The secret access key goes in once and never comes back.
 */
export interface OffsiteDestination {
  id: string
  name: string
  kind: "s3"
  endpoint: string
  region: string
  bucket: string
  prefix: string
  access_key_id: string
  path_style: boolean
  allow_private_network: boolean
  last_test_at: string | null
  last_test_error: string | null
  created_at: string
  /** Bundles with a copy verified here, and their size. */
  copies: number
  copy_bytes: number
  last_verified_at: string | null
  /**
   * What proved the newest copy's stored bytes: the store's own SHA-256
   * checksum, or a download and re-hash. "" for a copy recorded before this
   * was kept (counted on size and the uploader's metadata alone).
   */
  last_verified_by?: VerifiedBy | null
  used_by: string[]
}

export type VerifiedBy = "provider_checksum" | "download_rehash" | ""

/** What proved an off-site copy's stored bytes, in words. */
export function verifiedByText(by: VerifiedBy | null | undefined): string {
  switch (by) {
    case "provider_checksum": return "verified by the provider's checksum"
    case "download_rehash": return "downloaded and re-hashed"
    default: return "stored bytes not proven"
  }
}

/** POST …/destinations body. */
export interface NewOffsiteDestination {
  name: string
  endpoint: string
  region: string
  bucket: string
  prefix: string
  access_key_id: string
  secret_access_key: string
  path_style: boolean
  allow_private_network: boolean
}

/** One bundle at an off-site destination (GET /admin/instance/backups/copies). */
export interface OffsiteCopy {
  key: string
  size: number
  modified: string
  scope: "instance" | "workspace"
  workspace_id: string | null
  /** Already on this server, at local_path. */
  local: boolean
  local_path: string | null
}

export interface OffsiteCopyList { destination_id: string; destination_name: string; copies: OffsiteCopy[] }

/** An off-site fetch job (POST …/copies/fetch, GET …/copies/fetch/{id}). */
export interface OffsiteFetch {
  id: string
  destination_id: string
  key: string
  status: "running" | "done" | "failed"
  path: string | null
  size: number
  layers: number
  error: string | null
  started_at: string
  ended_at: string | null
}

/** POST …/destinations/{id}/test, and the test part of a create. */
export interface DestinationTest { ok: boolean; error: string | null; tested_at: string }

export interface DestinationCreated { destination: OffsiteDestination; test: DestinationTest | null; warning: string | null }

/** GET /api/v1/admin/instance/backups/vault-keys */
export interface VaultKeysResponse {
  versions: { version: string; env: string; active: boolean; envelopes: number }[]
  recovery_kit: { available: boolean; enabled: boolean }
}

/** GET /api/v1/admin/instance/backups/incidents */
export interface BackupIncident {
  id: string
  plan_id: string | null
  plan_name?: string | null
  kind: "failed" | "incomplete" | "stale" | "offsite" | "drill"
  state: "open" | "resolved"
  count: number
  first_at: string
  last_at: string
  resolved_at?: string | null
  message: string
  /** The run the incident last came from (View failure). */
  run_id?: string | null
}

/** POST /api/v1/admin/instance/backups/restore/checks */
export interface RestoreChecks {
  space: { ok: boolean; need_bytes: number; free_bytes: number }
  format: { ok: boolean; version: number; converter: boolean }
  runtime: { ok: boolean; detail: string; warnings: string[] }
  /** Settings restored switched off: "Docker socket mount on ops". */
  unsafe: string[]
  conflicts: { ok: boolean; detail: string }
  /** One runtime check per complete container environment the backup carries. */
  environments?: EnvironmentCheck[]
}

/**
 * A complete container environment before a restore: restore it as captured,
 * rebuild it from the crew image (architecture mismatch or layers missing —
 * its data still comes back), or skip it (no Docker).
 */
export interface EnvironmentCheck {
  crew: string
  action: "restore" | "rebuild" | "skip"
  platform: string
  host_platform: string
  missing_blobs: number
  need_bytes: number
  detail: string
}

/** One crew's complete environment after a restore or dry run. */
export interface EnvironmentOutcome {
  crew: string
  result: "restored" | "rebuilt" | "skipped"
  reason?: string
  image?: string
  container?: string
  unsafe: string[]
  notes?: string[]
}

export type RestoreTarget = "empty_server" | "isolated" | "replace" | "new_workspace" | "crew"

/** The dry run / restore report: what comes back, what does not, what is held. */
export interface RestoreReport {
  result: "ok" | "partial" | "failed"
  summary: string
  warnings: string[]
  notes: string[]
  phases?: RunPhase[]
}

/** GET /api/v1/admin/instance/backups/restores */
export interface RestoreRecord {
  id: string
  kind: "restore" | "dry_run" | "drill"
  actor: string
  bundle_path: string
  source_scope: BackupScope
  source_name: string
  source_date: string
  target: string
  result: "ok" | "partial" | "failed"
  warnings: number
  created_at: string
}

/** GET /api/v1/admin/instance/holds */
export interface InstanceHold {
  key: "routines" | "webhooks" | "queue" | string
  count: number
  detail: string
  created_at: string
}

/** One kind of live data on the Data retention page. */
export interface RetentionRow {
  key: string
  label: string
  note?: string | null
  /** Days kept; null is forever. With several workspaces: the value they share, or `mixed`. */
  days: number | null
  mixed?: boolean
  /** Housekeeping the server runs on its own schedule; shown read-only. */
  housekeeping?: boolean
  /** Read-only text for housekeeping rows ("hourly sweep", "last 10"). */
  fixed?: string | null
  /** False where the server cannot keep this forever (its sweep needs a limit). */
  foreverAllowed?: boolean
}

/** GET /api/v1/admin/instance/retention?ws= */
export interface RetentionResponse { rows: RetentionRow[] }

/** PUT /api/v1/admin/instance/retention (dry_run first for several workspaces). */
export interface RetentionChange {
  workspace_id: string
  workspace_name: string
  key: string
  from: number | null
  to: number | null
  /** Rows the next sweep deletes because of this change. */
  rows_affected: number
}
/** preview_id fingerprints the targets and every from → to; a confirmation sends it back as expect_preview. */
export interface RetentionPutResponse { dry_run: boolean; changes: RetentionChange[]; preview_id?: string }

/** The workspaces the scope bar lists (GET /api/v1/admin/workspaces). */
export interface ScopeWorkspace { id: string; name: string; slug: string }

// ─── Sections ───────────────────────────────────────────────────────────────

export const SECTION_TITLE: Record<BackupsSection | "retention", { title: string; sub: string }> = {
  overview: { title: "Overview", sub: "What is protected, how often, where, for how long, and whether a restore really worked" },
  history: { title: "Backup history", sub: "Every run, what it went through, and what each backup proves" },
  schedules: { title: "Schedules", sub: "Backup plans: what goes in, when, for how long, and where" },
  storage: { title: "Storage", sub: "Where backups are kept, how much room they need, and how hard they may push the server" },
  recovery: { title: "Recovery", sub: "A guided restore with checks first, a dry run, its full report, and drills" },
  keys: { title: "Keys & alerts", sub: "What opens a backup, what opens the secrets inside, and who hears when something is wrong" },
  retention: { title: "Data retention", sub: "How long the server keeps each kind of live data; backup copies are kept under Backups › Schedules" },
}

/** Sections whose settings are the instance's alone: the scope bar says so. */
export const INSTANCE_ONLY: ReadonlySet<string> = new Set(["storage", "keys"])

// ─── Scope in the URL ───────────────────────────────────────────────────────

export interface ScopeState {
  scope: BackupScope
  /** Workspace slugs or ids from ?ws=; null means every workspace. */
  ws: string[] | null
}

/** `?scope=instance|workspaces&ws=slug,slug` — no ws is all, ws=none is none. */
export function parseScope(search: string): ScopeState {
  const p = new URLSearchParams(search)
  const scope: BackupScope = p.get("scope") === "workspaces" ? "workspaces" : "instance"
  const raw = p.get("ws")
  if (raw === null) return { scope, ws: null }
  if (raw === "none") return { scope, ws: [] }
  return { scope, ws: raw.split(",").map((s) => s.trim()).filter(Boolean) }
}

/** The selected workspace ids, resolving slugs; unknown entries drop out. */
export function resolveSelection(state: ScopeState, all: ScopeWorkspace[]): Set<string> {
  if (state.ws === null) return new Set(all.map((w) => w.id))
  const want = new Set(state.ws)
  return new Set(all.filter((w) => want.has(w.slug) || want.has(w.id)).map((w) => w.id))
}

/** Write scope and selection into a search string, leaving other params. */
export function writeScope(search: string, scope: BackupScope, selected: Set<string>, all: ScopeWorkspace[]): string {
  const p = new URLSearchParams(search)
  p.set("scope", scope)
  if (all.length === 0 || selected.size === all.length) p.delete("ws")
  else if (selected.size === 0) p.set("ws", "none")
  else p.set("ws", all.filter((w) => selected.has(w.id)).map((w) => w.slug).join(","))
  const s = p.toString()
  return s ? `?${s}` : ""
}

/** The query the overview/runs endpoints take for the current scope. */
export function scopeQuery(scope: BackupScope, selected: Set<string>, all: ScopeWorkspace[]): string {
  const p = new URLSearchParams({ scope })
  if (scope === "workspaces" && selected.size !== all.length) p.set("ws", [...selected].join(","))
  return p.toString()
}

// ─── Contents: presets and the dependency map ───────────────────────────────

/** Each category and the one it cannot be restored without. */
export const CATEGORIES: { key: CategoryKey; label: string; needs: CategoryKey | null }[] = [
  { key: "agents", label: "Crews, agents and their settings", needs: null },
  { key: "memory", label: "Memory and its version history", needs: "agents" },
  { key: "chats", label: "Chats", needs: null },
  { key: "att", label: "Attachment files", needs: "chats" },
  { key: "routines", label: "Routines, schedules and run history", needs: "agents" },
  { key: "journal", label: "Journal and checkpoints", needs: null },
  { key: "creds", label: "Credentials and integrations", needs: "agents" },
  { key: "pages", label: "Pages and their files", needs: null },
  { key: "files", label: "Crew working files", needs: null },
  { key: "env", label: "Complete container environments", needs: "files" },
]

export const PRESETS: Record<Preset, { label: string; about: string }> = {
  complete: { label: "Complete recovery", about: "The whole Crewship: every workspace, users, settings, the keys a restore needs, and complete container environments." },
  workspace: { label: "Workspace backup", about: "Selected workspaces with everything valuable in them and the links they need." },
  custom: { label: "Custom backup", about: "Your own choice, e.g. memory every few hours and environments weekly. Does not count as protecting a workspace." },
}

export interface ContentsRow {
  key: CategoryKey
  label: string
  state: "included" | "required" | "excluded"
  /** For a required dependency: the included categories that need it. */
  neededBy: CategoryKey[]
  /** Attachment files left out while chats go in. */
  warning?: string
}

/**
 * Included / Required dependency / Excluded, the same rule the server applies
 * (preview-contents): a preset includes everything; a custom plan includes
 * what was not dropped. Complete container environments follow the
 * environment mode, not the checkbox list. Whatever an included (or required)
 * category needs is kept, transitively, and says who needs it.
 */
export function resolveContents(preset: Preset, envMode: "files" | "complete", dropped: ReadonlySet<CategoryKey>): ContentsRow[] {
  const on = (k: CategoryKey) => (k === "env" ? envMode === "complete" : preset !== "custom" || !dropped.has(k))
  const neededBy = new Map<CategoryKey, CategoryKey[]>()
  const kept = new Set<CategoryKey>(CATEGORIES.filter((c) => on(c.key)).map((c) => c.key))
  const queue = [...kept]
  while (queue.length) {
    const k = queue.shift()!
    const dep = CATEGORIES.find((c) => c.key === k)?.needs
    if (!dep) continue
    neededBy.set(dep, [...(neededBy.get(dep) ?? []), k])
    if (!kept.has(dep)) {
      kept.add(dep)
      queue.push(dep)
    }
  }
  return CATEGORIES.map((c) => {
    const included = on(c.key)
    const required = !included && kept.has(c.key)
    return {
      key: c.key,
      label: c.label,
      state: included ? "included" : required ? "required" : "excluded",
      neededBy: required ? neededBy.get(c.key) ?? [] : [],
      warning: c.key === "att" && !included && on("chats") ? "chats will open with missing files" : undefined,
    }
  })
}

/** "kept because memory and its version history, routines … need it" */
export function requiredWhy(row: ContentsRow): string {
  if (row.state !== "required") return ""
  const names = row.neededBy.map((k) => CATEGORIES.find((c) => c.key === k)!.label.toLowerCase())
  return `kept because ${names.join(", ")} need it`
}

/** Fold the server's preview into rows, when it answered. */
export function contentsFromPreview(p: PreviewContentsResponse): ContentsRow[] {
  const req = new Map(p.required.map((r) => [r.key, r.because]))
  const inc = new Set(p.included)
  return CATEGORIES.map((c) => ({
    key: c.key,
    label: c.label,
    state: inc.has(c.key) ? "included" : req.has(c.key) ? "required" : "excluded",
    neededBy: req.get(c.key) ?? [],
    warning: c.key === "att" && !inc.has("att") && inc.has("chats") ? "chats will open with missing files" : undefined,
  }))
}

// ─── The 14-night strip ─────────────────────────────────────────────────────

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"]
const WDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]

/** "29 Sep" — fixed English short months (ICU now writes "Sept" for en-GB). */
export function shortDate(d: Date, utc = false): string {
  return utc ? `${d.getUTCDate()} ${MONTHS[d.getUTCMonth()]}` : `${d.getDate()} ${MONTHS[d.getMonth()]}`
}

export interface StripCell {
  date: string
  day: number
  status: NightStatus
  mark: "◆" | "✓" | ""
  title: string
}

const NIGHT_TIP: Record<NightStatus, string> = {
  ok: "backup created",
  incomplete: "created · incomplete",
  failed: "failed",
  skipped: "skipped",
  late: "ran late",
  none: "no run",
}

/**
 * Always fourteen equal cells ending on `today`, oldest first. A night the
 * server did not report is "no run", never a gap that shifts the others.
 */
export function stripCells(nights: Night[], today: string): StripCell[] {
  const byDate = new Map(nights.map((n) => [n.date, n]))
  const end = parseDay(today)
  return Array.from({ length: 14 }, (_, i) => {
    const d = new Date(Date.UTC(end.y, end.m - 1, end.d - (13 - i)))
    const key = d.toISOString().slice(0, 10)
    const n = byDate.get(key)
    const status = n?.status ?? "none"
    const proof = n?.proof ?? 0
    const label = shortDate(d, true)
    return {
      date: key,
      day: d.getUTCDate(),
      status,
      mark: proof === 3 ? "◆" : proof >= 2 ? "✓" : "",
      title: `${label} · ${n?.detail || NIGHT_TIP[status]}`,
    }
  })
}

function parseDay(s: string): { y: number; m: number; d: number } {
  const [y, m, d] = s.split("-").map(Number)
  return { y, m, d }
}

// ─── Next runs ──────────────────────────────────────────────────────────────

export interface Schedule {
  cadence: Cadence
  time: string
  weekday: number | null
  monthday: number | null
  cron: string | null
  envMode: "files" | "complete"
  envCadence: EnvCadence
}

export interface PlannedRun {
  /** Wall-clock date in the plan's timezone, YYYY-MM-DD. */
  date: string
  time: string
  environments: boolean
}

function ymd(d: Date): string {
  return `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, "0")}-${String(d.getUTCDate()).padStart(2, "0")}`
}

function firstSunday(y: number, m0: number): number {
  const dow = new Date(Date.UTC(y, m0, 1)).getUTCDay()
  return 1 + ((7 - dow) % 7)
}

/** Whether a day with a data run also takes the environments. */
function envOn(s: Schedule, d: Date): boolean {
  if (s.envMode !== "complete") return false
  if (s.envCadence === "every") return true
  if (s.envCadence === "weekly") return d.getUTCDay() === 0
  return d.getUTCDay() === 0 && d.getUTCDate() === firstSunday(d.getUTCFullYear(), d.getUTCMonth())
}

/** Expand one cron field of minutes/hours: "*", "n", "a,b", "*\/n", "a-b". */
function cronField(f: string, max: number): number[] | null {
  const out = new Set<number>()
  for (const part of f.split(",")) {
    const step = /^\*\/(\d+)$/.exec(part)
    const range = /^(\d+)-(\d+)$/.exec(part)
    if (part === "*") for (let i = 0; i <= max; i++) out.add(i)
    else if (step) for (let i = 0; i <= max; i += Math.max(1, +step[1])) out.add(i)
    else if (range) for (let i = +range[1]; i <= Math.min(max, +range[2]); i++) out.add(i)
    else if (/^\d+$/.test(part) && +part <= max) out.add(+part)
    else return null
  }
  return [...out].sort((a, b) => a - b)
}

/**
 * The next `n` runs after `from` (a wall-clock instant in the plan's zone,
 * given as a Date whose UTC fields are that wall clock). The server's
 * /next answer wins when it exists; this is what the editor shows while a
 * change is unsaved. A cron with day fields other than "*" returns null —
 * only the server can say.
 */
export function computeNextRuns(s: Schedule, from: Date, n = 5): PlannedRun[] | null {
  const out: PlannedRun[] = []
  const nowKey = `${ymd(from)} ${String(from.getUTCHours()).padStart(2, "0")}:${String(from.getUTCMinutes()).padStart(2, "0")}`
  if (s.cadence === "custom") {
    const f = (s.cron ?? "").trim().split(/\s+/)
    if (f.length !== 5 || f[2] !== "*" || f[3] !== "*" || f[4] !== "*") return null
    const mins = cronField(f[0], 59)
    const hours = cronField(f[1], 23)
    if (!mins || !hours) return null
    for (let day = 0; day < 60 && out.length < n; day++) {
      const d = new Date(Date.UTC(from.getUTCFullYear(), from.getUTCMonth(), from.getUTCDate() + day))
      for (const h of hours) for (const m of mins) {
        const time = `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}`
        if (`${ymd(d)} ${time}` <= nowKey || out.length >= n) continue
        out.push({ date: ymd(d), time, environments: false })
      }
    }
    return out
  }
  if (!/^\d{2}:\d{2}$/.test(s.time)) return null
  for (let day = 0; day < 800 && out.length < n; day++) {
    const d = new Date(Date.UTC(from.getUTCFullYear(), from.getUTCMonth(), from.getUTCDate() + day))
    if (`${ymd(d)} ${s.time}` <= nowKey) continue
    const dow = d.getUTCDay()
    const dom = d.getUTCDate()
    const hit =
      s.cadence === "daily" ||
      (s.cadence === "weekly" && dow === (s.weekday ?? 0)) ||
      (s.cadence === "monthly" && (s.monthday ? dom === s.monthday : dow === 0 && dom === firstSunday(d.getUTCFullYear(), d.getUTCMonth())))
    if (hit) out.push({ date: ymd(d), time: s.time, environments: s.cadence === "daily" ? envOn(s, d) : s.envMode === "complete" })
  }
  return out
}

/** "Sun 4 Oct 03:00 + environments 04:00" */
export function formatPlannedRun(r: PlannedRun, withEnvTime = true): string {
  const [y, m, d] = r.date.split("-").map(Number)
  const dt = new Date(Date.UTC(y, m - 1, d))
  const day = `${WDAYS[dt.getUTCDay()]} ${shortDate(dt, true)}`
  if (!r.environments) return `${day} ${r.time}`
  if (!withEnvTime) return `${day} ${r.time} + environments`
  const [hh, mm] = r.time.split(":").map(Number)
  return `${day} ${r.time} + environments ${String((hh + 1) % 24).padStart(2, "0")}:${String(mm).padStart(2, "0")}`
}

/** An instant from the server as the plan's wall clock, for formatPlannedRun. */
export function instantToPlanned(iso: string, timezone: string, environments: boolean): PlannedRun {
  const parts = new Intl.DateTimeFormat("en-CA", { timeZone: timezone, year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hourCycle: "h23" })
    .formatToParts(new Date(iso))
  const get = (t: string) => parts.find((p) => p.type === t)?.value ?? "00"
  return { date: `${get("year")}-${get("month")}-${get("day")}`, time: `${get("hour")}:${get("minute")}`, environments }
}

/** The current wall clock in a timezone, as a Date whose UTC fields hold it. */
export function wallClock(now: Date, timezone: string): Date {
  const p = instantToPlanned(now.toISOString(), timezone, false)
  const [y, m, d] = p.date.split("-").map(Number)
  const [hh, mm] = p.time.split(":").map(Number)
  return new Date(Date.UTC(y, m - 1, d, hh, mm))
}

export const CADENCE_LABEL: Record<Cadence, string> = { daily: "Daily", weekly: "Weekly", monthly: "Monthly", custom: "Custom" }
const WEEKDAY = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"]

/** "data daily 03:00 · environments weekly" — a plan's one-line when. */
export function describePlanWhen(p: Pick<BackupPlan, "cadence" | "time_of_day" | "weekday" | "monthday" | "cron_expr" | "env_mode" | "env_cadence">): string {
  const when =
    p.cadence === "daily" ? `daily ${p.time_of_day}`
    : p.cadence === "weekly" ? `weekly on ${WEEKDAY[p.weekday ?? 0]} ${p.time_of_day}`
    : p.cadence === "monthly" ? `monthly${p.monthday ? ` on day ${p.monthday}` : ", first Sunday"} ${p.time_of_day}`
    : `cron ${p.cron_expr ?? ""}`.trim()
  if (p.env_mode !== "complete") return when
  const env = p.env_cadence === "every" ? "with every run" : p.env_cadence
  return `data ${when} · environments ${env}`
}

/** "keep 3 checked, 7 daily, 4 weekly, 12 monthly" */
export function describeKeep(p: Pick<BackupPlan, "keep_min" | "keep_daily" | "keep_weekly" | "keep_monthly">): string {
  const parts = [`${p.keep_min} checked`]
  if (p.keep_daily) parts.push(`${p.keep_daily} daily`)
  if (p.keep_weekly) parts.push(`${p.keep_weekly} weekly`)
  if (p.keep_monthly) parts.push(`${p.keep_monthly} monthly`)
  return `keep ${parts.join(", ")}`
}

// ─── Formatting ─────────────────────────────────────────────────────────────

export const PROOF_LABEL: Record<ProofLevel, string> = { 0: "—", 1: "checksum", 2: "contents checked", 3: "test restore" }

export function proofLabel(level: ProofLevel, drill?: DrillResult | null): string {
  if (level === 3 && drill && drill !== "ok") return `test restore · ${drill}`
  return PROOF_LABEL[level]
}

export function verdictHeadline(v: RestoreVerdict): { text: string; tone: Tone } {
  switch (v) {
    case "verified": return { text: "Restore verified", tone: "ok" }
    case "partial": return { text: "Partial restore verified", tone: "warn" }
    case "failed": return { text: "Test restore failed", tone: "bad" }
    case "contents_checked": return { text: "Latest backup: contents checked, not test-restored yet", tone: "warn" }
    case "checksum_only": return { text: "Latest backup: checksum only, not test-restored yet", tone: "warn" }
    default: return { text: "No backup yet", tone: "bad" }
  }
}

/** Result chip words for a run: "done", "done · incomplete", "interrupted · retried". */
export function runResult(r: Pick<BackupRun, "status" | "retried_at">): { text: string; tone: Tone } {
  switch (r.status) {
    case "done": return { text: "done", tone: "ok" }
    case "incomplete": return { text: "done · incomplete", tone: "warn" }
    case "interrupted": return { text: r.retried_at ? "interrupted · retried" : "interrupted", tone: "warn" }
    case "failed": return { text: "failed", tone: "bad" }
    case "skipped": return { text: "skipped", tone: "warn" }
    default: return { text: "running", tone: "muted" }
  }
}

/** "Complete recovery", "Memory every 6 h", "manual · before upgrade", "… · environments". */
export function runPlanLabel(r: Pick<BackupRun, "plan_name" | "trigger" | "note" | "kind">): string {
  const base = r.plan_name ?? (r.trigger === "manual" ? "manual" : r.trigger === "drill" ? "drill" : "—")
  const withKind = r.kind === "environments" ? `${base} · environments` : base
  return r.note ? `${withKind} · ${r.note}` : withKind
}

function duration(a?: string | null, b?: string | null): string {
  if (!a || !b) return ""
  const s = Math.max(0, Math.round((Date.parse(b) - Date.parse(a)) / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const r = s % 60
  return r && m < 10 ? `${m}m${r}s` : `${m}m`
}

/** "copy 6m40s · pack 11m · encrypt · check 4m · off-site —" */
export function formatPhases(phases: RunPhase[]): string {
  return phases
    .map((p) => {
      if (p.status === "skipped" || p.status === "pending") return `${p.name} —`
      const d = duration(p.started_at, p.ended_at)
      const t = [p.name, d].filter(Boolean).join(" ")
      return p.detail ? `${t} (${p.detail})` : t
    })
    .join(" · ")
}

/** Decimal units, as disks are sold: "3.9 GB", "38 MB". */
export function formatSize(bytes: number | null | undefined): string {
  if (bytes == null || !Number.isFinite(bytes)) return "—"
  const units = ["B", "KB", "MB", "GB", "TB"]
  let v = bytes
  let i = 0
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000
    i++
  }
  const n = i === 0 || v >= 100 ? String(Math.round(v)) : v.toFixed(1).replace(/\.0$/, "")
  return `${n} ${units[i]}`
}

/** "today 03:00", "yesterday 14:10", "29 Sep 03:00" in the viewer's zone. */
export function formatWhen(iso: string | null | undefined, now: Date = new Date()): string {
  if (!iso) return "never"
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return "—"
  const time = d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit", hourCycle: "h23" })
  const day = (x: Date) => `${x.getFullYear()}-${x.getMonth()}-${x.getDate()}`
  if (day(d) === day(now)) return `today ${time}`
  const y = new Date(now)
  y.setDate(now.getDate() - 1)
  if (day(d) === day(y)) return `yesterday ${time}`
  return `${shortDate(d)} ${time}`
}

/** "2 days ago", "today 03:00" — for the per-workspace coverage table. */
export function formatAgo(iso: string | null | undefined, now: Date = new Date()): string {
  if (!iso) return "never"
  const days = Math.floor((startOfDay(now) - startOfDay(new Date(iso))) / 86_400_000)
  if (days <= 0) return formatWhen(iso, now)
  if (days === 1) return "yesterday"
  return `${days} days ago`
}

function startOfDay(d: Date): number {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
}

/** "age1q7x…m3k" */
export function shortKey(k: string): string {
  return k.length > 14 ? `${k.slice(0, 7)}…${k.slice(-3)}` : k
}

// ─── Data retention ─────────────────────────────────────────────────────────

export const RETENTION_OPTIONS: { days: number | null; label: string }[] = [
  { days: 7, label: "7 d" },
  { days: 30, label: "30 d" },
  { days: 90, label: "90 d" },
  { days: 365, label: "1 y" },
  { days: null, label: "Forever" },
]

export function retentionLabel(days: number | null): string {
  return RETENTION_OPTIONS.find((o) => o.days === days)?.label ?? `${days} d`
}

/** The edits that differ from what the server holds, in row order. */
export function retentionDiff(rows: RetentionRow[], draft: Record<string, number | null>): { key: string; from: number | null; to: number | null; mixed: boolean }[] {
  return rows
    .filter((r) => !r.housekeeping && r.key in draft && (draft[r.key] !== r.days || r.mixed))
    .map((r) => ({ key: r.key, from: r.days, to: draft[r.key], mixed: !!r.mixed }))
}
