/**
 * Review fixtures for Admin › Backups: the design's own example (mockup v4),
 * shaped exactly like the typed API answers in backups-model.ts. Used by the
 * Vitest render tests and by the dev-only `?demo=1` toggle — never by a
 * production build (use-backups-data.ts checks NODE_ENV).
 *
 * Dates are built relative to `now`, so the demo reads "today 03:00" on any
 * day; tests pass a fixed `now`.
 */

import type {
  BackupPlan, BackupRecipient, BackupRun, BackupSettings, CalendarResponse, InstanceHold, Night,
  OverviewResponse, RestoreChecks, RestoreRecord, RestoreReport, RetentionResponse, ScopeWorkspace,
  VaultKeysResponse, BackupIncident, NightStatus, ProofLevel, OffsiteDestination,
} from "../backups-model"

const GB = 1_000_000_000
const MB = 1_000_000

export const FIXTURE_WORKSPACES: ScopeWorkspace[] = [
  { id: "ws-dess", name: "Dess", slug: "dess" },
  { id: "ws-unify", name: "Unify Lab", slug: "unify-lab" },
  { id: "ws-coolify", name: "Coolify", slug: "coolify" },
  { id: "ws-pages", name: "Pages demo", slug: "pages-demo" },
  { id: "ws-sandbox", name: "Sandbox", slug: "sandbox" },
]

function at(now: Date, daysAgo: number, hh: number, mm = 0): string {
  const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() - daysAgo, hh, mm)
  return d.toISOString()
}

function day(now: Date, daysAgo: number): string {
  const d = new Date(Date.UTC(now.getFullYear(), now.getMonth(), now.getDate() - daysAgo))
  return d.toISOString().slice(0, 10)
}

/** Local YYYY-MM-DD of `now`, what the strip ends on. */
export function fixtureToday(now: Date): string {
  return day(now, 0)
}

function nights(now: Date): Night[] {
  const S: NightStatus[] = ["ok", "ok", "ok", "ok", "skipped", "ok", "ok", "ok", "ok", "failed", "late", "ok", "ok", "incomplete"]
  const P: ProofLevel[] = [0, 0, 2, 0, 0, 0, 2, 0, 0, 0, 0, 3, 0, 2]
  const tip: Partial<Record<NightStatus, string>> = {
    failed: "failed: lock held by another backup",
    skipped: "skipped: no quiet window within 2 h",
    late: "server down at 03:00; catch-up backup at 07:12",
    incomplete: "created · incomplete: 12 attachment files missing",
  }
  return S.map((status, i) => ({ date: day(now, 13 - i), status, proof: P[i], detail: tip[status] ?? null }))
}

export function overviewFixture(now: Date, scope: "instance" | "workspaces", selected: string[] = FIXTURE_WORKSPACES.map((w) => w.id)): OverviewResponse {
  const inst = scope === "instance"
  const cov: Record<string, { last: string | null; status: "ok" | "warn" | "bad" }> = {
    "ws-dess": { last: at(now, 0, 3), status: "ok" },
    "ws-unify": { last: at(now, 2, 3), status: "warn" },
    "ws-coolify": { last: at(now, 0, 3), status: "ok" },
    "ws-pages": { last: at(now, 9, 3), status: "bad" },
    "ws-sandbox": { last: null, status: "bad" },
  }
  const rows = FIXTURE_WORKSPACES.filter((w) => selected.includes(w.id))
  const risk = rows.filter((w) => cov[w.id].status !== "ok")
  return {
    instance_summary: "5 workspaces, 7 users, instance settings, container environments",
    status: {
      label: inst ? "Complete recovery" : "Selected workspaces",
      verdict: "partial",
      summary: inst
        ? "Missing from every backup: 12 attachment files, and on another server the credentials (their vault keys are not in the backup)."
        : `Missing from the latest backups: 12 attachment files in Dess and Coolify.${risk.length ? ` No recent backup: ${risk.map((w) => w.name).join(", ")}.` : ""}`,
      protects: {
        value: inst ? "the whole Crewship: 5 workspaces, 7 users, instance settings, container environments" : `${rows.map((w) => w.name).join(", ")}: all workspace data`,
        detail: "attachment files are not collected yet",
        detail_tone: "warn",
      },
      how_often: {
        value: inst ? "data daily at 03:00 · environments weekly on Sunday" : "daily at 03:10 · memory in Dess and Coolify every 6 h",
        detail: "next run tomorrow 03:00 Europe/Prague",
      },
      where: { value: "this server only", detail: "no off-site copy", detail_tone: "bad" },
      how_long: { value: "at least 3 checked backups · 7 daily · 4 weekly · 12 monthly" },
      really_restored: {
        value: "partially · test restore of the backup from 2 days ago, by hand",
        detail: "latest backup today 03:00, its contents checked · 12 attachments did not open in the test",
      },
      offsite_verified: false,
    },
    needs_attention: [
      { id: "att", severity: "bad", title: "The latest backup is missing 12 attachment files", detail: "They come back as entries that do not open: 9 in Dess, 3 in Coolify.", action: { kind: "see_which", label: "See which", run_id: "run-1" } },
      ...(inst
        ? [{ id: "vault", severity: "bad" as const, title: "On a new server the latest backup cannot unlock credentials", detail: "Key versions v1 and v2 are not in it; restoring elsewhere means re-entering 41 credentials.", action: { kind: "keys" as const, label: "Keys" } }]
        : risk.map((w) => ({
            id: `cov-${w.id}`, severity: "bad" as const,
            title: `${w.name}: ${cov[w.id].last ? `last backup ${w.id === "ws-unify" ? "2 days ago" : "9 days ago"}` : "never backed up"}`,
            detail: "No plan covers it.",
            action: { kind: "back_up_now" as const, label: "Back up now", workspace_id: w.id },
          }))),
      { id: "late", severity: "warn", title: "One backup ran four hours late", detail: "The server was down at 03:00; one catch-up backup ran at 07:12 when it came back." },
    ],
    nights: nights(now),
    space: { backups_bytes: 26 * GB, free_bytes: 212 * GB, total_bytes: 342 * GB, staging_need_bytes: 9 * GB, restore_need_bytes: 48 * GB },
    workspaces: inst ? undefined : rows.map((w) => ({
      workspace_id: w.id, name: w.name, last_backup_at: cov[w.id].last, status: cov[w.id].status,
      plan: cov[w.id].status === "ok" ? "Workspace backup" : null, proof: cov[w.id].status === "ok" ? 2 : 0,
    })),
  }
}

function phases(now: Date, daysAgo: number, spec: [string, number | null, string?][]): BackupRun["phases"] {
  let t = new Date(at(now, daysAgo, 3)).getTime()
  return spec.map(([name, secs, detail]) => {
    if (secs === null) return { name, status: "skipped" as const, detail: detail ?? null }
    const started = new Date(t).toISOString()
    t += secs * 1000
    return { name, status: "done" as const, started_at: started, ended_at: new Date(t).toISOString(), detail: detail ?? null }
  })
}

const INCOMPLETE = [
  { kind: "attachment_missing", count: 9, workspace_id: "ws-dess", detail: "9 attachment files in Dess" },
  { kind: "attachment_missing", count: 3, workspace_id: "ws-coolify", detail: "3 attachment files in Coolify" },
]

export function runsFixture(now: Date): BackupRun[] {
  const base = {
    trigger: "schedule" as const, note: null, workspace_id: null, workspace_name: null, kind: "full" as const,
    incomplete: [], ended_at: null, pinned: false, recipients: ["ops-2026", "ops-backup"], format_version: 3,
    restorable: "direct" as const, retried_at: null, drill_result: null,
  }
  return [
    { ...base, id: "run-1", plan_id: "plan-complete", plan_name: "Complete recovery", scope: "instance", status: "incomplete", started_at: at(now, 0, 3), size_bytes: 3.9 * GB, proof_level: 2, bundle_path: "/b/instance-1.cship", incomplete: INCOMPLETE,
      phases: phases(now, 0, [["copy", 400], ["pack", 660], ["encrypt", 30], ["check", 240], ["off-site", null]]) },
    { ...base, id: "run-2", plan_id: "plan-memory", plan_name: "Memory every 6 h", scope: "workspaces", workspace_id: "ws-dess", workspace_name: "Dess", kind: "custom", status: "done", started_at: at(now, 0, 0), size_bytes: 38 * MB, proof_level: 2, bundle_path: "/b/dess-mem.cship",
      phases: phases(now, 0, [["copy", 12], ["pack", 4], ["encrypt", 1], ["check", 3]]) },
    { ...base, id: "run-3", plan_id: "plan-complete", plan_name: "Complete recovery", scope: "instance", status: "interrupted", retried_at: at(now, 1, 3, 41), started_at: at(now, 1, 3), size_bytes: 3.9 * GB, proof_level: 0, bundle_path: "/b/instance-2.cship",
      phases: [{ name: "pack", status: "failed", detail: "server restarted during pack · staging wiped · retried 03:41, done" }] },
    { ...base, id: "run-4", plan_id: "plan-complete", plan_name: "Complete recovery", scope: "instance", status: "incomplete", started_at: at(now, 2, 3), size_bytes: 3.8 * GB, proof_level: 3, drill_result: "partial", drill_at: at(now, 2, 10, 2), drill_note: "12 attachments did not open", bundle_path: "/b/instance-3.cship", incomplete: INCOMPLETE,
      phases: phases(now, 2, [["copy", 372], ["pack", 600], ["encrypt", 30], ["check", 230]]) },
    { ...base, id: "run-5", plan_id: "plan-complete", plan_name: "Complete recovery", scope: "instance", kind: "environments", status: "done", started_at: at(now, 3, 4), size_bytes: 11.2 * GB, proof_level: 2, bundle_path: "/b/instance-env.cship",
      phases: [{ name: "images", status: "done", detail: "2 new layers, 5 reused" }, { name: "volumes", status: "done" }, { name: "config", status: "done" }] },
    { ...base, id: "run-6", plan_id: null, plan_name: null, trigger: "manual", scope: "workspaces", workspace_id: "ws-pages", workspace_name: "Pages demo", status: "done", started_at: at(now, 9, 14, 10), size_bytes: 48 * MB, proof_level: 1, bundle_path: "/b/pages.cship",
      phases: phases(now, 9, [["copy", 20], ["pack", 10], ["encrypt", 2]]) },
    { ...base, id: "run-7", plan_id: null, plan_name: null, trigger: "manual", note: "before upgrade", scope: "instance", status: "done", started_at: at(now, 16, 22), size_bytes: 4.4 * GB, proof_level: 3, drill_result: "ok", bundle_path: "/b/instance-upgrade.cship", pinned: true,
      phases: phases(now, 16, [["copy", 380], ["pack", 640], ["encrypt", 30], ["check", 240]]) },
  ]
}

export function plansFixture(): BackupPlan[] {
  const base = {
    time_of_day: "03:00", weekday: null, monthday: null, cron_expr: null, timezone: "Europe/Prague", destinations: ["local"],
    recipient_ids: ["rcp-1", "rcp-2"], busy_wait_minutes: 120, busy_retry_minutes: 15, hold_cap_minutes: 20, stale_alert_hours: 36,
    enabled: true, next_run_at: null, last_run_at: null, keep_daily: 7, keep_weekly: 4, keep_monthly: 12,
  }
  return [
    { ...base, id: "plan-complete", name: "Complete recovery", preset: "complete", scope: "instance", workspace_ids: [], contents: [], env_mode: "complete", cadence: "daily", env_cadence: "weekly", keep_min: 3 },
    { ...base, id: "plan-memory", name: "Memory every 6 h", preset: "custom", scope: "workspaces", workspace_ids: ["ws-dess", "ws-coolify"], contents: ["memory"], env_mode: "files", cadence: "custom", cron_expr: "0 */6 * * *", env_cadence: "every", keep_min: 48, keep_daily: 0, keep_weekly: 0, keep_monthly: 0 },
  ]
}

/** The current month and the next: done, skipped, catch-up, failed, planned. */
export function calendarFixture(now: Date): CalendarResponse {
  const days: CalendarResponse["days"] = []
  const first = new Date(Date.UTC(now.getFullYear(), now.getMonth(), 1))
  const today = now.getDate()
  for (let i = 0; i < 70; i++) {
    const d = new Date(first.getTime() + i * 86_400_000)
    const key = d.toISOString().slice(0, 10)
    const past = d.getUTCMonth() === now.getMonth() && d.getUTCDate() <= today
    const offset = today - d.getUTCDate()
    const sunday = d.getUTCDay() === 0
    const entries: CalendarResponse["days"][number]["entries"] = []
    if (past) {
      const status = offset === 4 ? "failed" : offset === 3 ? "catchup" : offset === 9 ? "skipped" : "done"
      entries.push({ at: `${key}T${status === "catchup" ? "07:12" : "03:00"}:00Z`, kind: "data", status })
      if (sunday && offset > 0) entries.push({ at: `${key}T04:00:00Z`, kind: "environments", status: "done" })
    } else {
      entries.push({ at: `${key}T03:00:00Z`, kind: "data", status: "planned" })
      if (sunday) entries.push({ at: `${key}T04:00:00Z`, kind: "environments", status: "planned" })
    }
    days.push({ date: key, entries })
  }
  return { days }
}

export function settingsFixture(): BackupSettings {
  return {
    limits: { concurrency: 1, cpu_cores: 2, disk_mbps: 80, upload_mbps: 20 },
    heartbeat_url: "https://hc.example.com/ping/…",
    recovery_kit_enabled: false,
    channels: [],
    events: { failed: true, incomplete: true, stale: true, offsite: true, drill: true },
    stale_alert_hours: 36,
    drill_reminder: "monthly",
    instance_admins: 3,
    local_path: "~/.crewship/backups",
    destinations: [
      { id: "local", kind: "local", label: "This server", path: "~/.crewship/backups", used_bytes: 26 * GB, verified: true, available: true },
      { id: "drive", kind: "drive", label: "Google Drive", verified: false, available: false },
    ],
  }
}

/** The design's state: no off-site store yet, so every copy sits on this server. */
export function destinationsFixture(): OffsiteDestination[] {
  return []
}

export function recipientsFixture(now: Date): BackupRecipient[] {
  return [
    { id: "rcp-1", name: "ops-2026", public_key: "age1q7xk2v9d8f3l5n0p4r6t8w0y2a4c6e8g0i2k4m3k", holder: "held by the platform lead", created_at: at(now, 60, 9) },
    { id: "rcp-2", name: "ops-backup", public_key: "age1k2pz9x7v5t3r1p9n7l5j3h1f9d7b5z3x1v9t7w8d", holder: "held by a second person, so one lost key is not a lost backup", created_at: at(now, 60, 9) },
  ]
}

export function vaultKeysFixture(): VaultKeysResponse {
  return {
    versions: [
      { version: "v1", env: "ENCRYPTION_KEY", active: false, envelopes: 9 },
      { version: "v2", env: "ENCRYPTION_KEY_V2", active: true, envelopes: 132 },
    ],
    recovery_kit: { available: false, enabled: false },
  }
}

export function incidentsFixture(now: Date): BackupIncident[] {
  return [{ id: "inc-1", plan_id: "plan-complete", plan_name: "Complete recovery", kind: "failed", state: "open", count: 3, first_at: at(now, 1, 3), last_at: at(now, 0, 3), message: "Complete recovery backup failed. Last successful backup: 32 hours ago." }]
}

export function restoresFixture(now: Date): RestoreRecord[] {
  return [
    { id: "rr-1", kind: "drill", actor: "demo@crewship.ai", bundle_path: "/b/instance-3.cship", source_scope: "instance", source_name: "Whole instance", source_date: at(now, 2, 3), target: "isolated instance", result: "partial", warnings: 1, created_at: at(now, 2, 10, 2) },
    { id: "rr-2", kind: "restore", actor: "demo@crewship.ai", bundle_path: "/b/dess.cship", source_scope: "workspaces", source_name: "Dess", source_date: at(now, 9, 3), target: "new workspace", result: "ok", warnings: 2, created_at: at(now, 9, 15, 30) },
  ]
}

export function checksFixture(scope: "instance" | "workspaces"): RestoreChecks {
  return {
    space: { ok: true, need_bytes: 48 * GB, free_bytes: 212 * GB },
    format: { ok: true, version: 3, converter: false },
    runtime: { ok: true, detail: "Docker 27, linux/amd64 for 6 environments", warnings: ["researcher: built for linux/arm64; its data comes back, its environment is rebuilt from the crew image"] },
    unsafe: ["Docker socket mount on ops", "privileged mode on infra", "host path /srv/data on ingest"],
    conflicts: scope === "instance" ? { ok: true, detail: "target is empty" } : { ok: false, detail: "2 users mapped to existing accounts" },
    environments: [
      { crew: "ops", action: "restore", platform: "linux/amd64", host_platform: "linux/amd64", missing_blobs: 0, need_bytes: 3 * GB, detail: "linux/amd64, 3.0 GiB of image layers" },
      { crew: "researcher", action: "rebuild", platform: "linux/arm64", host_platform: "linux/amd64", missing_blobs: 0, need_bytes: 2 * GB, detail: "built for linux/arm64 and this server is linux/amd64: its data comes back, its environment is rebuilt from the crew image" },
    ],
  }
}

export function dryRunFixture(scope: "instance" | "workspaces"): RestoreReport {
  return {
    result: "partial",
    summary: scope === "instance" ? "5 workspaces, 7 users, settings, 6 container environments" : "New workspace with 4 crews, 23 agents",
    warnings: ["12 attachments come back without files", "1 environment rebuilt instead of restored (architecture)"],
    notes: [
      "Processes start fresh; what was only in memory is not restored",
      "Every sign-in session is dropped · journal re-signed",
      "Routines, webhooks, queues and external retries held until you resume",
    ],
  }
}

export function restorePhasesFixture(): RestoreReport {
  return {
    result: "ok",
    summary: "Restoring",
    warnings: [],
    notes: [],
    phases: [
      { name: "Database", status: "done", detail: "3 min" },
      { name: "Files and memory", status: "done", detail: "9 min" },
      { name: "Environments", status: "running", detail: "4 of 6 · image layers loaded, volumes in progress" },
      { name: "Report", status: "pending", detail: "waiting" },
    ],
  }
}

export function holdsFixture(now: Date): InstanceHold[] {
  return [
    { key: "routines", count: 17, detail: "would have run 3× while the server was down", created_at: at(now, 0, 9) },
    { key: "webhooks", count: 6, detail: "answer 503 until resumed; senders retry", created_at: at(now, 0, 9) },
    { key: "queue", count: 23, detail: "would repeat GitHub comments and 2 deploys", created_at: at(now, 0, 9) },
  ]
}

export function retentionFixture(): RetentionResponse {
  return {
    rows: [
      { key: "routine_runs_days", label: "Routine runs", note: "keeps the last 10 per routine", days: 90, foreverAllowed: false },
      { key: "approvals_days", label: "Approvals", note: "decided approvals", days: 90 },
      { key: "inbox_days", label: "Inbox items", note: "resolved only; new limit, off by default", days: null },
      { key: "chats_days", label: "Chats", note: "chats with open work never swept; off by default", days: null },
      { key: "audit_days", label: "Audit log", note: "", days: null },
      { key: "credential_audit_days", label: "Credential audit", note: "who read which secret", days: 90 },
      { key: "keeper_decisions_days", label: "Keeper decisions", note: "decided history only; off by default", days: null },
      { key: "memory_versions_days", label: "Memory version history", note: "the three newest always stay", days: 30, foreverAllowed: false },
      { key: "page_panel_data_days", label: "Page panel data", note: "", days: 7, foreverAllowed: false },
      { key: "journal_compaction", label: "Journal compaction", note: "housekeeping", days: 30, housekeeping: true, fixed: "30 d" },
      { key: "webhook_receipts", label: "Routine webhook receipts", note: "housekeeping", days: 30, housekeeping: true, fixed: "30 d" },
      { key: "delivered_bodies", label: "Delivered work bodies", note: "housekeeping", days: 7, housekeeping: true, fixed: "7 d" },
      { key: "orphans", label: "Page builds, orphaned attachments", note: "housekeeping", days: null, housekeeping: true, fixed: "hourly sweep" },
    ],
  }
}
