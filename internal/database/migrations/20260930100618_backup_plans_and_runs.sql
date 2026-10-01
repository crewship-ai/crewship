-- Backups redesign, Track C1: backup plans and the runs they (or an admin)
-- start. Both are instance-level bookkeeping: no workspace FK, so a plan
-- names workspaces by id in JSON and a run's history survives the deletion
-- of the workspace it backed up. Neither rides a workspace bundle (see
-- NonBackedUpTables in internal/backup/intent.go). The old workspace-bound
-- scheduled_jobs table stays as it is and new code does not use it.
--
-- backup_plans — what goes in, when, for how long, and where.
--   preset        complete | workspace | custom. A custom plan's bundles never
--                 count as protecting a workspace.
--   scope         instance | workspaces; workspace_ids (JSON array) for
--                 scope=workspaces, where [] means every workspace.
--   contents      JSON array of category keys (custom only; the server adds
--                 required dependencies when it runs).
--   env_mode      files | complete — complete container environments on top.
--   cadence       daily | weekly | monthly | custom (cron_expr, 5 fields).
--   time_of_day   "HH:MM" wall clock in timezone (an IANA zone).
--   weekday       0 = Sunday (weekly); monthday 1..31 (monthly; NULL means the
--                 first Sunday of the month).
--   env_cadence   every | weekly | monthly — which runs also take environments.
--   keep_*        retention of this plan's bundles; keep_min is a floor age
--                 never touches (>= 1).
--   recipient_ids JSON array — every plan is encrypted; there is no
--                 plaintext plan.
--   busy_*        how long a run waits for running agents, and how often it
--                 looks again, before it is recorded as skipped.
--   next_run_at   the next due time the scheduler will act on (UTC, RFC 3339).
CREATE TABLE backup_plans (
    id                 TEXT PRIMARY KEY,
    name               TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    preset             TEXT NOT NULL CHECK (preset IN ('complete', 'workspace', 'custom')),
    scope              TEXT NOT NULL CHECK (scope IN ('instance', 'workspaces')),
    workspace_ids      TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(workspace_ids)),
    contents           TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(contents)),
    env_mode           TEXT NOT NULL DEFAULT 'files' CHECK (env_mode IN ('files', 'complete')),
    cadence            TEXT NOT NULL CHECK (cadence IN ('daily', 'weekly', 'monthly', 'custom')),
    time_of_day        TEXT NOT NULL DEFAULT '03:00',
    weekday            INTEGER CHECK (weekday IS NULL OR weekday BETWEEN 0 AND 6),
    monthday           INTEGER CHECK (monthday IS NULL OR monthday BETWEEN 1 AND 31),
    cron_expr          TEXT,
    timezone           TEXT NOT NULL DEFAULT 'UTC',
    env_cadence        TEXT NOT NULL DEFAULT 'weekly' CHECK (env_cadence IN ('every', 'weekly', 'monthly')),
    keep_min           INTEGER NOT NULL DEFAULT 3 CHECK (keep_min >= 1),
    keep_daily         INTEGER NOT NULL DEFAULT 7 CHECK (keep_daily >= 0),
    keep_weekly        INTEGER NOT NULL DEFAULT 4 CHECK (keep_weekly >= 0),
    keep_monthly       INTEGER NOT NULL DEFAULT 12 CHECK (keep_monthly >= 0),
    destinations       TEXT NOT NULL DEFAULT '["local"]' CHECK (json_valid(destinations)),
    recipient_ids      TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(recipient_ids)),
    busy_wait_minutes  INTEGER NOT NULL DEFAULT 120 CHECK (busy_wait_minutes >= 0),
    busy_retry_minutes INTEGER NOT NULL DEFAULT 15 CHECK (busy_retry_minutes >= 1),
    hold_cap_minutes   INTEGER NOT NULL DEFAULT 20 CHECK (hold_cap_minutes >= 1),
    stale_alert_hours  INTEGER NOT NULL DEFAULT 36 CHECK (stale_alert_hours >= 1),
    enabled            INTEGER NOT NULL DEFAULT 1,
    next_run_at        TEXT,
    last_run_at        TEXT,
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    created_by         TEXT,
    updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_by         TEXT
);
CREATE INDEX idx_backup_plans_next ON backup_plans(next_run_at) WHERE enabled = 1;

-- backup_runs — one row per bundle attempt (a plan over several workspaces
-- makes one run per workspace, so each row has at most one bundle).
--   trigger       schedule | manual | catchup | drill.
--   status        running | done | incomplete | failed | interrupted | skipped.
--   phase         where a running run is: queued, busy_wait, starting, copy,
--                 pack, encrypt, check, off-site (and "" once finished).
--   phases        JSON [{name, started_at, ended_at, status, detail}].
--   due_at        the scheduled occurrence the run serves (NULL for manual).
--                 A (plan, due_at, workspace) triple is claimed once: the
--                 unique index is what stops a restart or a second replica
--                 from running the same night twice.
--   next_attempt_at  when a queued or busy-waiting run may start.
--   retry_of      the interrupted run this one retries (each is retried once).
--   retried_at    on the interrupted run: when its retry finished.
--   environments  1 when this run should also take complete container
--                 environments (recorded intent; the capture is Track E's).
--   categories    JSON array of category keys the bundle carries (custom).
CREATE TABLE backup_runs (
    id              TEXT PRIMARY KEY,
    plan_id         TEXT,
    trigger         TEXT NOT NULL CHECK (trigger IN ('schedule', 'manual', 'catchup', 'drill')),
    note            TEXT,
    scope           TEXT NOT NULL CHECK (scope IN ('instance', 'workspaces')),
    workspace_id    TEXT,
    kind            TEXT NOT NULL DEFAULT 'full' CHECK (kind IN ('full', 'custom', 'environments')),
    categories      TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(categories)),
    environments    INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'done', 'incomplete', 'failed', 'interrupted', 'skipped')),
    phase           TEXT NOT NULL DEFAULT 'queued',
    phases          TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(phases)),
    due_at          TEXT,
    next_attempt_at TEXT,
    retry_of        TEXT,
    retried_at      TEXT,
    bundle_path     TEXT,
    catalog_id      TEXT,
    size            INTEGER,
    error           TEXT,
    incomplete      TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(incomplete)),
    recipients      TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(recipients)),
    actor_user_id   TEXT,
    started_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    ended_at        TEXT,
    hold_ms         INTEGER
);
CREATE INDEX idx_backup_runs_started ON backup_runs(started_at);
CREATE INDEX idx_backup_runs_plan ON backup_runs(plan_id, started_at) WHERE plan_id IS NOT NULL;
CREATE INDEX idx_backup_runs_active ON backup_runs(status, next_attempt_at) WHERE status = 'running';
CREATE UNIQUE INDEX idx_backup_runs_claim ON backup_runs(plan_id, due_at, COALESCE(workspace_id, ''))
    WHERE plan_id IS NOT NULL AND due_at IS NOT NULL AND retry_of IS NULL;
