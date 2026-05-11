# Crewship — MVP Robust Foundation PRD

> ⚠️ **SUPERSEDED 2026-05-07 by `PIPELINES.md`.**
> The 6-epic broad scope in this doc was narrowed after stakeholder devil's-advocate
> review. The new MVP focuses on a single high-leverage feature — AI-authored pipelines
> with two-tier execution + cross-crew reuse — rather than catching up to Trigger.dev
> on background-job primitives. Routines / Inbox / Errors are deferred to Phase 2.
> See `PIPELINES.md` for the active scope. This doc is preserved as historical context
> on the Trigger.dev competitive analysis that led to the narrowing.

**Verze:** 1.0
**Datum:** 2026-05-07
**Status:** Superseded — historical record
**Companion docs:** `ORCHESTRATION.md`, `CREW-EXECUTION.md`, `DATABASE.md`
**Předchůdce:** Trigger.dev competitive deep-dive (7-fork research, 2026-05-07)

> Šest epiců, ~32 ticketů. Estimates: XS = půl dne, S = 1–2 dny, M = 3–5 dní, L = 1–2 týdny.
> Priority: **P0** = MVP-blocker, **P1** = MVP must-have, **P2** = MVP nice-to-have, post-MVP přesunuto pod „Out of scope".

---

## 0. Executive summary

Crewship dnes funguje jako AI-agent orchestrátor v jádru, ale **chybí mu robustní platformová primitiva**, která konkurenti (Trigger.dev, Inngest) považují za samozřejmost — formální retry policy, idempotency, error grouping, skip-if-already-running, manual trigger, calendar-style run history. Současně Trigger.dev má **prokazatelnou reliability bolest** (129 výpadků za 7 měsíců, full-month Stripe refund 5/2024, kritický bug s 3,800 zaseknutými tasky po restartu serveru, 2h18m us-east-1 výpadek 9/2025) — což je pozice, kterou Crewship single-binary architektura strukturálně nemůže mít, ale **dnes ji nikomu neprodává**.

Tento PRD řeší **obojí najednou**: zavádí 6 epiců, které z Crewshipu MVP udělají **prokazatelně robustnější** platformu než Trigger.dev na primitivech, a otevírá narativ „**single binary reliability**" jako sales pitch #1.

**Co po MVP shipu Crewship reálně umí navíc:**

1. **Routines jako first-class entity** s catch-up po výpadku, 6 overlap policies (Temporal-style), manual trigger funkční i v paused stavu, NL → cron přes LLM (volá se přímo přes `internal/llm/` middleware, viz EPIC 6), kalendářová heatmapa run historie. Trigger.dev/Inngest nemá ani polovinu.
2. **Idempotency keys + deklarativní retry policy** napříč všemi triggers (assignments, routines, agent runs). Žádné double-execution při retry. `RetryAfterDelay(secs)` z LLM rate-limit hlavičky. `AbortRunError` strukturovaný permanent-fail.
3. **Errors page s fingerprintingem + bulk replay** — 1,000 failed runs zkolabuje na 5 unique error groups. „Replay all in group" jeden klik. Sparkline 24h jako workspace puls.
4. **Run detail waterfall side-sheet** — span timeline vlevo, inspector vpravo, prev/next nav, replay v headeru. Pro AI agenty (kde 1 run = 50+ tool calls) **nezbytné**.
5. **Live sidebar badges + agent run-counters + sparkline** — psychologický skok ze statického dashboardu na „control room s pulsem".
6. **Inbox / Notification Center** — sjednocený feed: pending Approvals, failed runs requiring attention, expirující credentials, daily routine summary. Bell ikona top-bar.
7. **Auto-disable po N consecutive failures** + recovery scan po restartu. Anti-Trigger.dev #1566.

**Out of scope MVP** (ale zarámované pro fázi 2): Waitpoints primitive, AI-authored versioned Pipelines, Inbound MCP server, official npm SDK, OTEL exporter. Tyto features jsou v `MVP-PHASE-2-PRD.md` (TBD).

---

## 1. Critical path

```
EPIC 1 (Routines first-class) ──┬─→ EPIC 2 (Retry + Idempotency)
                                ├─→ EPIC 4 (Run detail waterfall)
                                └─→ EPIC 6 (NL → cron)

EPIC 3 (Errors page) ───────────┬─→ EPIC 5 (Live UI + Inbox)
                                └─→ závislé na EPIC 2 (fingerprint hook)
```

**EPIC 1 a EPIC 2 paralelně** — jeden je BE infrastruktura, druhý je cross-cutting layer, sdílí jen idempotency-key column.
**EPIC 3 závisí na EPIC 2** (potřebuje strukturovaný error v `runs` tabulce).
**EPIC 4 a 5 závisí na EPIC 1+2+3** (reuse fire/run/error data).
**EPIC 6 nezávisí na ničem** — quick win, pustit první sprint paralelně.

**Total estimate:** ~5–7 týdnů pro 1 full-time inženýra na BE + 1 na FE. Při split je ship za **2–3 sprinty**.

---

## 2. Reliability narrative (sales pitch foundation)

Tento PRD je nejen technický catch-up. Každá feature níže má **second-order benefit** — buduje příběh, který Crewship dnes neumí prodat:

| Feature | Reliability story |
|---|---|
| Routines jako entity + recovery scan | „Po restartu binárky všechny rutiny vědí, co zmeškaly. Trigger.dev #1566 nestane." |
| Idempotency keys napříč | „Když retryneš trigger, kniha bude účtovat jednou. Trigger.dev weego/HN: výchozí retry je nebezpečný pro non-idempotentní operace." |
| Auto-disable po N failures | „Vyklepnutý token nesžere 24 h kvóty. Inngest tohle má, my taky. Trigger.dev ne." |
| Errors fingerprinting | „1,000 failed runs = 5 unique problémů, vyřešíš je všechny jedním klikem. Trigger.dev má od v4.4.3, my máme po MVP shipu." |
| Single binary | „Bez Postgres, bez Redis, bez etcd, bez ClickHouse, bez S3, bez registry. Trigger.dev má 6 systémů, každý může spadnout. Crewship má 1 binárku, která spadne maximálně sebe samu." |

Po shipu MVP **přidáme `/vs/trigger-dev` srovnávací stránku** s těmito argumenty + odkazem na Trigger.dev incident reporty (jejich vlastní blog) a statusgator metriku „129 outages / 7 months". Není to trash-talk, jsou to fakta z jejich vlastních zdrojů.

---

## 3. Goals / Non-goals

### Goals
1. Eliminovat **5 nejhorších produkčních bolestí** současného Crewshipu: tichá selhání rutin, double-execution při retry, neexistující error diagnostika, nefunkční pause/resume, neviditelný run state v dashboardu.
2. **Reliability** jako prokazatelná vlastnost (recovery testy, idempotency testy, restart-survival testy v CI).
3. Demonstrovatelná parita s Trigger.dev/Inngest na background-job primitivech do **8 týdnů**.
4. Otevřít **dva architektonické háčky** pro fázi 2: `target_kind='pipeline'` na Routines, `error_fingerprint` v `runs` tabulce. Bez nich budou Pipelines a Inbound MCP nákladnější.
5. Žádný breaking change na existujícím REST API (additive only). Stávající integrace nesmí spadnout.

### Non-goals
- Waitpoints primitive — fáze 2.
- AI-authored Pipelines — fáze 2.
- Inbound MCP server — fáze 2.
- Polyglot SDK (Python/Go pro caller) — Trigger.dev to taky neumí, nikomu nechybí.
- Vlastní queue engine — držíme SQLite + goroutines.
- OTEL exporter — Enterprise tier feature, fáze 2.
- Suspend-and-respawn step state machine — fáze 4 (Temporal-level durability).

---

## 4. EPIC 1: Routines as first-class entity

**Goal.** Z `agents.schedule_*` sloupců na first-class `routines` + `routine_fires` tabulky. Tohle je foundation pod fází 2 (Pipelines target) a okamžitě otevírá catch-up, manual trigger, history, replay.

**Owner.** Backend Go.

**Estimate.** ~10 dní (M).

### Proč MVP — VÁVU

Dnešní stav: rutina je 5 sloupců na agent rowě. Když restartuje binárka v 03:00 a v 03:00 byla naplánovaná „každodenní email digest", **prostě se nespustí**, nikdo to neřekne. Když uživatel chce „spusť teď" mimo schedule, **nemá tlačítko**. Když rutina selže 5× po sobě, **dál pálí kvótu**. Když chce vidět co se 30 dní dělo, **má 1 řádek `last_run`**.

Trigger.dev #1566 ukázal, co se stane bez first-class fires tabulky: 3,800 duplicit, manuální cleanup. **Crewship dnes má stejný architectural setup jako Trigger.dev v té době.** Jediný důvod, proč jsme to ještě nezažili, je nízký traffic.

První rutina, kterou Pavel chce („každý den ve 8 stáhni emaily a shrň"), je v dnešním Crewshipu **production-křehká** — bez retry, bez catch-up, bez history. Po EPIC 1 je production-grade.

### Tickets

#### CRE-XXX-1.1 — Migration v77: routines + routine_fires tables
**Priority:** P0 — blokuje vše ostatní v EPIC 1
**Estimate:** M

**Acceptance:**
- [ ] `internal/database/migrate.go` v77 vytvoří `routines` table podle níže
- [ ] v77 vytvoří `routine_fires` table podle níže
- [ ] v77 datová migrace: pro každý `agents` row se `schedule_enabled=1` vytvoří 1 routine row (`target_kind='agent'`, `target_id=agent.id`, `cron_expr=schedule_cron`, `prompt=schedule_prompt`)
- [ ] v77 ponechá `agents.schedule_*` sloupce (deprecated, dropne v77+1 po validaci)
- [ ] Indexy: `idx_routines_workspace_enabled`, `idx_routines_next_run`, `idx_routines_external_id`, `idx_fires_routine_scheduled`, `idx_fires_status`, `idx_fires_routine_status`
- [ ] Migration test: forward + rollback, data integrity check (SELECT COUNT před/po)

**Schema:**

```sql
CREATE TABLE routines (
  id                          TEXT PRIMARY KEY,                        -- "rtn_" + CUID
  workspace_id                TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name                        TEXT NOT NULL,
  slug                        TEXT NOT NULL,                           -- workspace-scoped unique
  description                 TEXT,

  -- TARGET
  target_kind                 TEXT NOT NULL CHECK (target_kind IN ('agent','crew','pipeline')),
  target_id                   TEXT NOT NULL,
  target_version              INTEGER,                                 -- pinned pipeline version, NULL = latest
  prompt                      TEXT,                                    -- agent-target only
  payload_json                TEXT,                                    -- crew/pipeline structured input

  -- TRIGGER
  trigger_kind                TEXT NOT NULL DEFAULT 'cron'
                                CHECK (trigger_kind IN ('cron','interval','manual','event')),
  cron_expr                   TEXT,
  interval_seconds            INTEGER,
  timezone                    TEXT NOT NULL DEFAULT 'UTC',             -- IANA
  jitter_seconds              INTEGER NOT NULL DEFAULT 0 CHECK (jitter_seconds &lt;= 300),

  -- LIFECYCLE
  enabled                     INTEGER NOT NULL DEFAULT 1,
  paused_at                   TEXT,
  pause_reason                TEXT,
  external_id                 TEXT,                                    -- AI-authored, per-user routines
  dedup_key                   TEXT,
  next_run_at                 TEXT,                                    -- denormalized
  last_run_at                 TEXT,
  last_run_status             TEXT,

  -- POLICIES
  catchup_window_seconds      INTEGER NOT NULL DEFAULT 3600,           -- 0=skip-all, -1=infinite
  overlap_policy              TEXT NOT NULL DEFAULT 'skip'
                                CHECK (overlap_policy IN ('skip','buffer_one','buffer_all','cancel_other','terminate_other','allow_all')),
  retry_policy                TEXT NOT NULL DEFAULT '{}',              -- JSON, see EPIC 2
  auto_disable_after_failures INTEGER NOT NULL DEFAULT 0,              -- 0=disabled

  -- AUDIT
  created_at                  TEXT NOT NULL DEFAULT (datetime('now','subsec')),
  created_by                  TEXT REFERENCES users(id),
  updated_at                  TEXT NOT NULL DEFAULT (datetime('now','subsec')),
  deleted_at                  TEXT,

  UNIQUE(workspace_id, slug)
);
CREATE UNIQUE INDEX idx_routines_dedup ON routines(workspace_id, dedup_key) WHERE dedup_key IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX idx_routines_workspace_enabled ON routines(workspace_id, enabled) WHERE deleted_at IS NULL;
CREATE INDEX idx_routines_next_run          ON routines(enabled, next_run_at) WHERE enabled=1 AND deleted_at IS NULL;
CREATE INDEX idx_routines_external_id       ON routines(workspace_id, external_id) WHERE external_id IS NOT NULL;

CREATE TABLE routine_fires (
  id                    TEXT PRIMARY KEY,                              -- "fire_" + CUID
  routine_id            TEXT NOT NULL REFERENCES routines(id) ON DELETE CASCADE,
  scheduled_at          TEXT NOT NULL,                                 -- cron-decided fire time (UTC)
  triggered_at          TEXT,                                          -- actual dispatch (NULL if SKIPPED)
  trigger_source        TEXT NOT NULL CHECK (trigger_source IN ('cron','manual','backfill','replay','catchup')),
  triggered_by_user_id  TEXT REFERENCES users(id),
  status                TEXT NOT NULL DEFAULT 'PENDING'
                          CHECK (status IN ('PENDING','RUNNING','COMPLETED','FAILED','SKIPPED','CANCELED','TIMEOUT','INTERRUPTED')),
  skip_reason           TEXT,                                          -- 'overlap_skip', 'paused', 'catchup_window_expired', 'auto_disabled'
  run_id                TEXT,                                          -- linked agent_run / crew_run
  attempts              INTEGER NOT NULL DEFAULT 0,
  duration_ms           INTEGER,
  error_message         TEXT,
  error_fingerprint     TEXT,                                          -- EPIC 3 hook
  metadata_json         TEXT
);
CREATE INDEX idx_fires_routine_scheduled  ON routine_fires(routine_id, scheduled_at DESC);
CREATE INDEX idx_fires_status             ON routine_fires(status, scheduled_at);
CREATE INDEX idx_fires_routine_status     ON routine_fires(routine_id, status, scheduled_at DESC);
CREATE INDEX idx_fires_fingerprint        ON routine_fires(error_fingerprint) WHERE error_fingerprint IS NOT NULL;
```

#### CRE-XXX-1.2 — Scheduler refactor: fire = row, in-flight check, graceful shutdown
**Priority:** P0 — anti-Trigger.dev #1566
**Estimate:** M

**Acceptance:**
- [ ] `internal/scheduler/scheduler.go` přepsat: každý cron tick → `INSERT INTO routine_fires` před dispatch
- [ ] In-flight check: `SELECT COUNT(*) FROM routine_fires WHERE routine_id=? AND status IN ('PENDING','RUNNING')` před fire
- [ ] Aplikuje `overlap_policy` (MVP: pouze `skip`, `allow_all`, `buffer_one` — zbylé 3 v CRE-XXX-1.7)
- [ ] Graceful shutdown: SIGTERM → wait for active goroutines max 30s → status `RUNNING` → `INTERRUPTED` pokud nedoběhnou
- [ ] Recovery scan na startu: každý fire `PENDING` starší než `now - 5min` → status `INTERRUPTED` + Journal entry
- [ ] Restart test (test-suite): start scheduler s 3 pending fires, kill -TERM, restart, ověřit že 3 fires jsou v `INTERRUPTED` ne `PENDING` ani v duplicitě
- [ ] `internal/scheduler/scheduler_test.go` rozšířit o restart-survival test + duplicate-prevention test

#### CRE-XXX-1.3 — Catchup window logic
**Priority:** P0 — Pavlův „rutina přežije noc bez serveru"
**Estimate:** S

**Acceptance:**
- [ ] Při startu scheduleru: pro každou enabled routine spočítat `missed_fires` v `[max(last_run_at, now - catchup_window_seconds), now]` z cron expr
- [ ] Pro každý missed slot vytvořit fire row s `trigger_source='catchup'`
- [ ] Aplikovat overlap_policy na sled fires:
  - `skip` (default) → spustit jen poslední, ostatní status=`SKIPPED`, skip_reason=`overlap_skip`
  - `buffer_all` → spustit všechny sériově
  - `terminate_other` → fallback na `skip` (catchup nemá co terminovat)
- [ ] `catchup_window_seconds=0` → žádný catchup, log "skipped N missed fires"
- [ ] `catchup_window_seconds=-1` → infinite, ale **hard cap 1000 fires** s warning v Journal
- [ ] Test: zastavit scheduler na 3 hodiny (mock clock), restart, ověřit catchup behavior pro `cron='*/15 * * * *'` × 3 různé policies

#### CRE-XXX-1.4 — API: CRUD + manual trigger + pause/resume
**Priority:** P0 — bez API není FE
**Estimate:** S

**Acceptance:**
- [ ] `POST /api/v1/workspaces/{ws}/routines` — create (MANAGER+); body validace; respekt `Idempotency-Key` header (EPIC 2); respekt `dedup_key` v body (409 pokud existuje)
- [ ] `GET /api/v1/workspaces/{ws}/routines?enabled=&target_kind=&external_id=&q=` — list + paginace
- [ ] `GET /api/v1/workspaces/{ws}/routines/{id}` — detail + posledních 50 fires inline
- [ ] `PATCH /api/v1/workspaces/{ws}/routines/{id}` — update config; aktualizuje `next_run_at` pokud cron/interval/timezone změněn
- [ ] `DELETE /api/v1/workspaces/{ws}/routines/{id}` — soft delete
- [ ] `POST /api/v1/workspaces/{ws}/routines/{id}:pause` — set `paused_at` + `pause_reason`; **manual trigger stále funguje** (Temporal pattern)
- [ ] `POST /api/v1/workspaces/{ws}/routines/{id}:resume`
- [ ] `POST /api/v1/workspaces/{ws}/routines/{id}:trigger` — manual fire **NOW**, vytvoří fire row s `trigger_source='manual'` + `triggered_by_user_id`; respektuje overlap_policy ale lze override v body `{"force": true}` (MANAGER+)
- [ ] Tests: full CRUD coverage, paused-but-triggerable test, dedup_key conflict test

#### CRE-XXX-1.5 — API: backfill + replay + upcoming
**Priority:** P1 — operations win
**Estimate:** S

**Acceptance:**
- [ ] `POST /api/v1/workspaces/{ws}/routines/{id}:backfill` — body `{from, to, max_fires?, overlap_policy_override?}`; vytvoří fire rows v daném rangi; default max_fires=1000, hard cap 10,000 (varování v UI)
- [ ] `POST /api/v1/workspaces/{ws}/routines/{id}:replay` — body `{fire_ids[]}` nebo `{filter: {status, from, to}, max?, spread_seconds?}`; vytvoří nové fires s `trigger_source='replay'`; spread_seconds rozprostře přes čas (anti-thundering-herd, Inngest pattern)
- [ ] `GET /api/v1/workspaces/{ws}/routines/{id}/upcoming` — vrátí příštích 5 fires (Trigger.dev pattern); pomáhá UI debugovat cron expr
- [ ] `GET /api/v1/workspaces/{ws}/routines/{id}/fires?status=&source=&from=&to=` — paginated history s filtry

#### CRE-XXX-1.6 — Auto-disable po N consecutive failures
**Priority:** P1 — chrání kvótu/billing
**Estimate:** S

**Acceptance:**
- [ ] Scheduler po každém FAILED fire kontroluje: `SELECT status FROM routine_fires WHERE routine_id=? ORDER BY scheduled_at DESC LIMIT auto_disable_after_failures`
- [ ] Pokud všech `N` rows je `FAILED` → `UPDATE routines SET enabled=0, paused_at=now, pause_reason='auto_disabled_after_N_failures'`
- [ ] WS event `routine.auto_disabled` na `workspace:{id}` channel
- [ ] Inbox notification (EPIC 5) — sjednocený feed
- [ ] `auto_disable_after_failures=0` → vypnuto (default pro existující rutiny po migraci)
- [ ] Test: 5 consecutive failures s `auto_disable_after_failures=5` → routine `enabled=0`

#### CRE-XXX-1.7 — Pokročilé overlap policies (cancel_other, terminate_other, buffer_all)
**Priority:** P2 — pokročilé use-cases
**Estimate:** M

**Acceptance:**
- [ ] `cancel_other`: před fire poslat `Cancel(run_id)` všem `RUNNING` fires, čekat na cleanup (max 30s), pak fire
- [ ] `terminate_other`: kill container ihned (`docker stop --time=5`), fire bez čekání
- [ ] `buffer_all`: vždy create PENDING, dispatcher serializuje (potřebuje per-routine queue worker)
- [ ] Tests: každá policy má dedikovaný integration test s mock clock

#### CRE-XXX-1.8 — Multi-replica leader election (single-leader explicit)
**Priority:** P1 — anti-duplicate fires na multi-instance deployech
**Estimate:** S

**Acceptance:**
- [ ] V `internal/scheduler/scheduler.go` při startu: zkontrolovat `CREWSHIP_SCHEDULER_LEADER=1` env var; pokud ne, scheduler spustí pouze recovery scan, nedispatchuje
- [ ] Default `CREWSHIP_SCHEDULER_LEADER=1` (single-binary deploy chce být leader)
- [ ] Multi-replica deploy: ops nastaví na 1 instanci `=1`, na ostatních `=0`. EE roadmap: bbolt lease nebo Postgres advisory lock pro automatic election (TBD)
- [ ] Health endpoint `GET /api/v1/scheduler/status` vrací `{is_leader: bool, last_tick_at, pending_fires, lag_seconds}`
- [ ] Documentation update v `DEPLOYMENT.md`

---

## 5. EPIC 2: Retry + Idempotency layer

**Goal.** Cross-cutting layer napříč všemi triggers: routines, manual API calls, MCP tool calls (až přijdou). Idempotency-Key header support, deklarativní retry policy DSL, strukturované error class. Žádný feature visible, ale **odemyká spolehlivost všech ostatních feature**.

**Owner.** Backend Go.

**Estimate.** ~7 dní (M).

### Proč MVP — VÁVU

Dnes: když user retrynue trigger crew (network blip, accidental refresh), spustí se **dvakrát**. Když LLM dostane 429, agent fail-loopuje bez backoff. Když cron dispatcher restartne mid-tick, fire je dispatched **opět**. Žádný způsob jak říct „tohle je fatální, nezkoušej znovu".

HN feedback (weego, 2025-09): **Trigger.dev výchozí auto-retry pro non-idempotentní operace = duplicate Stripe charges.** To je real-world bug. Crewship má dnes ještě horší stav (žádný idempotency layer vůbec).

Tahle epic je **prerequisita pro AI-authored Pipelines** (fáze 2): když agent emituje pipeline, runtime musí garantovat exactly-once semantics — bez Idempotency-Key headeru a strukturovaných error class to neuděláme.

### Tickets

#### CRE-XXX-2.1 — Migration v78: idempotency_keys table + retry_policy on agent_runs
**Priority:** P0
**Estimate:** S

**Acceptance:**
- [ ] v78 vytvoří `idempotency_keys` table:

```sql
CREATE TABLE idempotency_keys (
  key                TEXT NOT NULL,
  scope              TEXT NOT NULL CHECK (scope IN ('global','run','attempt')),
  scope_ref          TEXT,                                              -- run_id when scope='run'/'attempt'
  workspace_id       TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  endpoint           TEXT NOT NULL,                                     -- 'POST /api/v1/.../trigger'
  request_hash       TEXT NOT NULL,                                     -- sha256(method+path+body)
  response_status    INTEGER,
  response_body      TEXT,
  expires_at         TEXT NOT NULL,
  created_at         TEXT NOT NULL DEFAULT (datetime('now','subsec')),
  PRIMARY KEY (workspace_id, key, scope, scope_ref)
);
CREATE INDEX idx_idempotency_expires ON idempotency_keys(expires_at);
```
- [ ] v78 přidá na `agent_runs` (a `crew_runs`): `idempotency_key TEXT`, `retry_policy TEXT NOT NULL DEFAULT '{}'`, `attempt INTEGER NOT NULL DEFAULT 1`, `parent_run_id TEXT`, `error_class TEXT`, `error_fingerprint TEXT`
- [ ] Background sweeper (existující backup-cleanup goroutine) maže expired keys každých 10 min
- [ ] Default TTL 30 dní (Trigger.dev parita)

#### CRE-XXX-2.2 — Idempotency-Key middleware
**Priority:** P0 — gate pro všechny write endpoints
**Estimate:** S

**Acceptance:**
- [ ] `internal/api/middleware/idempotency.go` middleware aplikovaný na: `POST /api/v1/.../trigger`, `POST /api/v1/.../routines`, `POST /api/v1/assignments`, `POST /api/v1/crews/.../runs`, `POST /api/v1/agents/.../runs`
- [ ] Header `Idempotency-Key: &lt;uuid&gt;` (povinný 1–256 chars; ne-povinný — bez něj middleware no-op)
- [ ] Při hitu: spočítat `request_hash`, compare:
  - shoda → vrátit cached `response_status` + `response_body`, header `X-Idempotent-Replay: true`
  - mismatch → 409 `IDEMPOTENT_KEY_MISMATCH` (klient retrynul s jiným payloadem)
  - new key → uložit po response &lt; 5xx (5xx neuložit, ať user může retrynout)
- [ ] `Idempotency-Key-Scope: run|global` header (default `global`)
- [ ] Tests: replay test, mismatch test, 5xx-no-store test, race-condition test (2 paralelní requesty s same key → druhý 409)

#### CRE-XXX-2.3 — Retry policy DSL + executor
**Priority:** P0
**Estimate:** S

**Acceptance:**
- [ ] `internal/orchestrator/retry.go` parser pro `retry_policy` JSON:

```jsonc
{
  "max_attempts": 5,                         // total incl. first
  "factor": 2.0,                             // backoff multiplier
  "min_timeout_ms": 1000,
  "max_timeout_ms": 300000,
  "randomize": true,                         // jitter
  "non_retryable_exit_codes": [127, 130],
  "non_retryable_error_classes": ["AuthError", "PermissionDenied", "AbortRunError"]
}
```
- [ ] Executor: po failure agent runu zkontroluje policy, pokud retryable → `INSERT new agent_run with attempt=attempt+1, parent_run_id=current.id, scheduled_at=now+backoff`
- [ ] Backoff: `min(min_timeout * factor^attempt, max_timeout)`; if `randomize`, multiply by `[0.5, 1.5]` random
- [ ] Hierarchy: routine.retry_policy → crew.retry_policy → agent.retry_policy → workspace default
- [ ] Tests: backoff math, max_attempts cap, non_retryable bypass

#### CRE-XXX-2.4 — Strukturované error classes
**Priority:** P0
**Estimate:** S

**Acceptance:**
- [ ] `internal/orchestrator/errors.go` definuje:
  - `AbortRunError(reason)` — permanent fail, **never retry**, status='FAILED', `error_class='AbortRunError'`
  - `RetryAfterError(reason, delay_seconds)` — retry **až po delay** ignoruje backoff (exact rate-limit handling z `Retry-After` headeru)
  - `RetryableError(reason)` — explicit retry signal (orchestrator default ji řeší stejně jako neznámé exit code)
- [ ] Adapter layer (CLAUDE_CODE/CODEX_CLI/GEMINI_CLI/OPENCODE) parsuje JSON error output a maps na error_class
- [ ] Special case: HTTP 429 z Anthropic/OpenAI/Gemini → `RetryAfterError(delay = max(Retry-After header, 60))`
- [ ] Tests: každá error class → správné retry behavior

#### CRE-XXX-2.5 — Idempotent routines fire
**Priority:** P0 — anti-Trigger.dev #1566
**Estimate:** XS

**Acceptance:**
- [ ] Scheduler při dispatch fire generuje `idempotency_key = "fire_{routine_id}_{scheduled_at_unix}"` scope=`global`
- [ ] Prevents duplicate dispatch při restart mid-tick (recovery scan vidí PENDING fire, idempotency check vidí klíč → re-emit no-op)
- [ ] Tests: kill scheduler během dispatch, restart, ověřit single fire

#### CRE-XXX-2.6 — Workspace-level retry defaults
**Priority:** P1
**Estimate:** XS

**Acceptance:**
- [ ] `workspaces.default_retry_policy TEXT NOT NULL DEFAULT '{"max_attempts":3,"factor":2.0,"min_timeout_ms":1000,"max_timeout_ms":60000,"randomize":true}'`
- [ ] Settings UI: workspace admin může edit defaults
- [ ] Each crew/agent inherits, can override

---

## 6. EPIC 3: Errors page — fingerprinting + bulk replay

**Goal.** Z `1,000 failed runs = 1,000 řádků k probrání` na `5 unique error groups, klik = replay all`. Single highest-leverage UI feature pro AI-flakiness era.

**Owner.** Backend Go + Frontend React.

**Estimate.** ~7 dní (M).

### Proč MVP — VÁVU

LLM jsou **prokazatelně flaky**: Anthropic/OpenAI 429, Gemini timeouts, network blips, expirující tokeny. Současný Crewship zobrazuje failed runs jako flat list v Journalu — když týden běžela rutina každou hodinu a 30× failed kvůli 1 expirovanému tokenu, user vidí 30 řádků a neví, že jsou všechny stejné root-cause.

Trigger.dev v4.4.3 (2026-03-10) launchla Errors page s fingerprintingem jako **headline feature** v changelogu. Proč: protože v AI-jobs světě **bez tohohle nelze produkčně provozovat**. Inngest má bulk replay od 2024.

Druhý důvod pro MVP: **Errors page je perfect demo proti Cursoru/Vercel AI**. Konkurence to nemá v podstatě vůbec. „Klikni replay all" je 2-vteřinový demo moment.

### Tickets

#### CRE-XXX-3.1 — Error fingerprinting v orchestrátoru
**Priority:** P0
**Estimate:** S

**Acceptance:**
- [ ] `internal/orchestrator/fingerprint.go`: `Fingerprint(error_class, error_message_first_200_chars, agent_id, exit_code) → sha256_hex_first_16chars`
- [ ] Při FAILED run: spočítat fingerprint, zapsat do `runs.error_fingerprint` + `routine_fires.error_fingerprint` (pokud fire-driven)
- [ ] Stable: stejné inputs → stejný fingerprint napříč restartami
- [ ] Tests: fingerprint stability, near-duplicate clustering (variant message → similar but distinct fingerprint)

#### CRE-XXX-3.2 — Aggregation queries + API
**Priority:** P0
**Estimate:** S

**Acceptance:**
- [ ] `GET /api/v1/workspaces/{ws}/errors?since=&until=&crew_id=&agent_id=` vrací error groups:
```jsonc
[{
  "fingerprint": "a1b2c3...",
  "first_seen": "2026-05-01T...",
  "last_seen": "2026-05-07T...",
  "count": 47,
  "trend": "up|down|flat",                  // vs previous equivalent window
  "sample_run_ids": ["run_x", "run_y", "run_z"],
  "agent_id": "agt_...",
  "crew_id": "crw_...",
  "error_class": "RetryAfterError",
  "error_message_preview": "Anthropic 429 rate limit...",
  "affected_routines": ["rtn_a", "rtn_b"]
}]
```
- [ ] `GET /api/v1/workspaces/{ws}/errors/{fingerprint}/runs` — paginated affected runs
- [ ] `POST /api/v1/workspaces/{ws}/errors/{fingerprint}/replay` body `{max?, spread_seconds?}` — bulk replay all affected runs (spread anti-thundering-herd)
- [ ] Sparkline endpoint: `GET /api/v1/workspaces/{ws}/errors/sparkline?since=&until=&bucket_seconds=` vrací time-bucketed counts

#### CRE-XXX-3.3 — Errors page UI
**Priority:** P0
**Estimate:** M

**Acceptance:**
- [ ] Nová route `app/(dashboard)/errors/page.tsx`
- [ ] Top: time-range picker (presets 1h/24h/7d/30d + custom) + agent/crew filter dropdown + text search
- [ ] **Sparkline strip** (24h default) — peak tooltip „12 errors at 14:32 — click to filter to that hour"; reuse `components/features/logs/logs-histogram.tsx`
- [ ] Tabulka groups:
  - `Severity` (chip, by error_class)
  - `Error message preview` (truncated 80 chars)
  - `Crew/Agent` (linked)
  - `First seen` (relative)
  - `Last seen` (relative + absolute on hover)
  - `Count` (large number)
  - `Trend` (↑/↓/→ chip vs prev window)
  - `Affected runs` (number, klik → drill-in)
  - `Actions` (… menu: Replay all, View runs, Mute fingerprint)
- [ ] Klik na řádek → side-sheet s detailem: full error message, stack trace (pokud existuje), affected runs list (virtualized), inline `Replay all` + `Replay selected` buttons
- [ ] Empty state: „No errors in the selected window 🎉" + CTA na nastavení alertingu
- [ ] Sidebar item `Errors` s live counter badge (count za 24h)

#### CRE-XXX-3.4 — Mute fingerprint (suppress noise)
**Priority:** P2
**Estimate:** XS

**Acceptance:**
- [ ] `POST /api/v1/workspaces/{ws}/errors/{fingerprint}/mute` body `{until?: ISO8601, reason}` (default 7d)
- [ ] Mute table: `error_mutes(workspace_id, fingerprint, muted_until, reason, muted_by)`
- [ ] Errors page UI hides muted by default + toggle „Show muted (3)"
- [ ] Inbox notifications respektují mute

---

## 7. EPIC 4: Run detail waterfall side-sheet

**Goal.** Z flat log listu na timeline waterfall + inspector, prev/next navigation, replay button v headeru. Pro AI agenty (1 run = desítky tool callů) **nepostradatelné**.

**Owner.** Frontend React.

**Estimate.** ~10 dní (L).

### Proč MVP — VÁVU

Pavel explicitně zmínil v posledním kole: „líbí se mi, že tam ty rany běží a když bych si rozklikl nějaký task, takže tam uvidím rany". Dnes Crewship rozkliknutí runu → flat log list bez hierarchie. Pro AI agenta s 50+ tool calls je to **nepoužitelné** pro debugging.

Trigger.dev waterfall + inspector je hlavní důvod, proč Mordrel v Medium srovnání označuje DX jako jejich top moat. Cursor `Show Reasoning` je primitivum tohoto patternu — Trigger.dev to dotáhl do production-grade. Crewship to potřebuje, jinak vypadá jako „grep tool" vedle nich.

Druhý důvod: Crewship Crews **přirozeně tvoří graf** (Lead → Members, tool calls, sub-agents). Žádná konkurence (CrewAI/AutoGen/LangGraph) nemá production-grade graph viewer. Tohle je přesně místo, kde předjedem.

### Tickets

#### CRE-XXX-4.1 — Span model na journal events
**Priority:** P0
**Estimate:** S

**Acceptance:**
- [ ] Migration v79 přidá na `journal_events` (existující tabulka): `parent_span_id TEXT`, `span_id TEXT`, `duration_ms INTEGER`, `span_kind TEXT` (`'agent_run','tool_call','llm_call','crew_handoff','wait'`)
- [ ] Orchestrator při emit eventu populuje span_id (CUID) a parent_span_id (z context propagation)
- [ ] Sidecar emit eventu pro tool_call s parent = current agent_run span
- [ ] Backfill stávajících eventů: best-effort, NULL parent = top-level

#### CRE-XXX-4.2 — Run detail API
**Priority:** P0
**Estimate:** S

**Acceptance:**
- [ ] `GET /api/v1/workspaces/{ws}/runs/{run_id}` vrací:
```jsonc
{
  "run": { ...full run row },
  "spans": [{ "id", "parent_id", "kind", "name", "started_at", "duration_ms", "status", "attributes": {...}, "input": {...}, "output": {...} }],
  "logs": [{ "span_id", "timestamp", "level", "message" }],
  "next_run_id": "run_xyz",        // for prev/next nav
  "prev_run_id": "run_abc",
  "cost_usd": 0.034,
  "tokens": { "input": 1234, "output": 567 }
}
```
- [ ] `GET /api/v1/workspaces/{ws}/runs/{run_id}/stream` SSE pro live runs
- [ ] Tests: span hierarchy reconstruction, prev/next ordering by started_at

#### CRE-XXX-4.3 — Run detail side-sheet UI
**Priority:** P0
**Estimate:** L

**Acceptance:**
- [ ] Komponenta `components/features/runs/run-detail-sheet.tsx`
- [ ] Trigger: klik na run řádek v Journal/Errors/Routines history
- [ ] Layout: side-sheet (resizable, persisted v localStorage, default 60% width)
- [ ] **Header:** breadcrumb `Workspace / Crew / Run #ID` | `[← Prev] [Next →]` (keyboard `j`/`k`) | `[Replay] [Cancel] [Copy ID]` | status chip
- [ ] **Levý panel (resizable, default 40%):** waterfall span timeline
  - každý span = horizontal bar, hierarchie indented
  - barvy: `running=blue (pulsing)`, `success=green`, `failed=red`, `pending=gray`, `wait=purple`
  - klik na span → vybere ho, pravý panel ukáže detail
  - keyboard nav arrow up/down
- [ ] **Pravý panel:** tabs `Logs | Attributes | I/O | Errors`
  - `Logs`: virtualized log list filtered by selected span
  - `Attributes`: key-value table (model name, tokens, cost, tool name, ...)
  - `I/O`: input/output JSON inspector (collapsible)
  - `Errors`: full error message + stack + fingerprint link to Errors page
- [ ] **Bottom bar:** souhrnné metriky `Total: 4m23s | Cost: $0.034 | Tokens: 1.2k in / 567 out | Spans: 47`
- [ ] **Empty/streaming state:** pokud běží, spans přibývají bez reload, log auto-scroll s pause-on-hover
- [ ] Reuse Virtuoso (existuje v `components/features/logs/`)

#### CRE-XXX-4.4 — Replay z Run detailu
**Priority:** P1
**Estimate:** S

**Acceptance:**
- [ ] `[Replay]` button v headeru → modal:
  - `Replay with same input` (default)
  - `Replay with modified input` (JSON editor pre-filled)
  - `Replay from this step` (vybere se span; orchestrátor naváže na předchozí spans pokud je to safe)
- [ ] Vytvoří nový run s `parent_run_id` = current, `idempotency_key` auto-generated
- [ ] Po klik nav na nový run side-sheet (zachovává context)

---

## 8. EPIC 5: Live UI — sidebar badges, agent stats, Inbox

**Goal.** Z statického dashboardu na „control room s pulsem". Drobné, ale psychologicky obrovský dopad.

**Owner.** Frontend React + Backend Go (small).

**Estimate.** ~6 dní (M).

### Proč MVP — VÁVU

Pavel řekl: „Trigger Dev má ten dashboard takový nic neříkajícího, ale líbí se mi, že tam je počet runů, líbí se mi, že ty runy běží." A: „bych tam měl třeba i nějaký inbox".

Dnes Crewship: otevřeš dashboard, **nic se nehýbe**. Žádný indikátor, jestli něco běží. Žádný badge. Žádný puls.

Trigger.dev tohle dělá explicitně: live-counter v sidebaru, sparkline na errors page, pulsing kruh u running runů. Drobnosti, ale `customer success teams can look up what happened during a user's job run` (jejich vlastní marketing) — protože dashboard **ukazuje aktivitu**, ne staticky data.

Inbox pak řeší **decision fatigue**: dnes user musí kontrolovat 5 různých UI pro „co potřebuje moji pozornost". Po MVP: 1 panel, 1 bell.

### Tickets

#### CRE-XXX-5.1 — Workspace counters WS push
**Priority:** P0
**Estimate:** S

**Acceptance:**
- [ ] `internal/ws/hub.go` přidá channel `workspace:{id}:counters` s eventy:
```jsonc
{
  "running_runs": 3,
  "queued_fires": 12,
  "pending_approvals": 2,
  "failed_24h": 5,
  "auto_disabled_routines": 1,
  "expiring_credentials_7d": 3
}
```
- [ ] Push při každé state change (run start/end, fire create, approval create/decide, credential rotation)
- [ ] Debounced 500ms (max 2 events/sec na klienta)
- [ ] Initial state na connect: full snapshot

#### CRE-XXX-5.2 — Sidebar live badges
**Priority:** P0
**Estimate:** S

**Acceptance:**
- [ ] `components/layout/app-sidebar.tsx` přidat per-item badge počítadla:
  - `Routines (3)` — běžící + queued
  - `Runs (12)` — running
  - `Errors (5)` — failed za 24h
  - `Approvals (2)` — pending
- [ ] Reuse `hooks/use-journal-stream.ts` pattern; nový hook `useWorkspaceCounters()`
- [ ] Badges: `&lt; 100` exact number, `100-999` zaokrouhlené, `1000+` → `1k+`
- [ ] Pulsing animace pro `running_runs &gt; 0`

#### CRE-XXX-5.3 — Per-agent / per-crew stats karty
**Priority:** P0
**Estimate:** M

**Acceptance:**
- [ ] `app/(dashboard)/agents/page.tsx`: každá agent karta zobrazuje:
  - `runs (24h)` count
  - `success rate (24h)` percentage
  - `avg duration` (z posledních 50 runs)
  - `last error` (truncated, klik → run detail side-sheet)
  - `next scheduled` (pokud má rutinu)
  - mini sparkline 24h (status barvy)
- [ ] Klik na sparkline / count → filtered Runs view (`/runs?agent_id=...`)
- [ ] Endpoint `GET /api/v1/workspaces/{ws}/agents/{id}/stats?window=24h` vrací aggregaci
- [ ] Same pro `app/(dashboard)/crews/page.tsx`

#### CRE-XXX-5.4 — Notification Center / Inbox
**Priority:** P0
**Estimate:** M

**Acceptance:**
- [ ] Migration v80: `notifications` table:
```sql
CREATE TABLE notifications (
  id            TEXT PRIMARY KEY,
  workspace_id  TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  user_id       TEXT REFERENCES users(id),                  -- NULL = workspace-wide
  kind          TEXT NOT NULL CHECK (kind IN ('failed_run','pending_approval','expiring_credential','auto_disabled_routine','daily_summary','platform')),
  severity      TEXT NOT NULL CHECK (severity IN ('info','warning','error')),
  title         TEXT NOT NULL,
  body          TEXT,
  ref_kind      TEXT,                                        -- 'run','routine','credential','approval'
  ref_id        TEXT,
  url           TEXT,                                        -- deep link
  read_at       TEXT,
  dismissed_at  TEXT,
  created_at    TEXT NOT NULL DEFAULT (datetime('now','subsec'))
);
CREATE INDEX idx_notifications_user_unread ON notifications(workspace_id, user_id, read_at) WHERE read_at IS NULL AND dismissed_at IS NULL;
```
- [ ] Producent eventů (existující kód):
  - failed run → notification (level=error)
  - approval pending &gt; 1h → notification (level=warning)
  - credential expires v &lt; 7d → notification (level=warning)
  - routine auto-disabled → notification (level=error)
  - daily summary @ 8:00 user-local → notification (level=info)
- [ ] `GET /api/v1/workspaces/{ws}/notifications?read=&kind=` paginated
- [ ] `POST /api/v1/workspaces/{ws}/notifications/{id}:mark-read`, `:dismiss`, `:mark-all-read`
- [ ] Bell ikona v `components/layout/app-toolbar.tsx` s unread count badge
- [ ] Klik bell → side-sheet `&lt;NotificationCenter&gt;` (pravý), kategorizováno: `Needs attention | Activity | Platform`
- [ ] Každá notif: ikona by kind, title, body, time-ago, klik = navigate to `url`
- [ ] WS push channel `user:{id}:notifications` realtime
- [ ] Tests: produce/dismiss/mark-read flows

---

## 9. EPIC 6: NL → cron via LLM

**Goal.** User napíše „every weekday at 9am Prague" → dostane cron expr + lidský preview „spustí se v 9:00 každý všední den, příští fire úterý 8.5. 9:00".

**Owner.** Backend Go (1 endpoint) + Frontend React (1 wizard step).

**Estimate.** ~2 dny (S).

### Proč MVP — VÁVU

Trigger.dev má feature „Use AI to help you create cron patterns" a uživatelé na PH/HN to **opakovaně cituji jako delight moment**. Crewship má LLM middleware (`internal/llm/`) + multi-CLI adapters + existující routine-authoring tok v `internal/pipeline/` → 2 dny implementace, **nadprůměrně velký wow effect** v demu.

> **Pozn.:** Dřívější verze tohoto PRD odkazovaly na samostatný autorský LLM orchestrátor s package `internal/api/<retired>` — žádný takový package neexistuje (potvrzeno v `PIPELINES.md §15`). NL→cron call jde přímým middleware voláním přes `internal/llm/` a saves se přes `internal/pipeline/schedules.go`.

Druhý důvod: cron syntax je **měřitelná bariéra** pro non-engineery. Pavel v memory zmiňuje, že Crewship cílí na product owners + AI engineers — ne pure DevOps. Bez NL→cron je rutina-creation tax pro polovinu cílovky.

### Tickets

#### CRE-XXX-6.1 — Endpoint POST /routines:nl
**Priority:** P0
**Estimate:** S

**Acceptance:**
- [ ] `POST /api/v1/workspaces/{ws}/routines:nl` body `{prompt: "every weekday 9am Prague"}`
- [ ] Internal call přes `internal/llm/` middleware (Anthropic/OpenAI/Ollama, dle workspace LLM provider) s prompt:
  ```
  Convert natural-language schedule to UTC cron expression + IANA timezone.
  Output JSON only: {"cron": "...", "timezone": "...", "human_repr": "...", "next_5_fires_iso": [...], "notes": "..."}
  Input: {user_prompt}
  ```
- [ ] Response validuje:
  - cron parser accepts (robfig/cron/v3 + případně gronx pro `L`)
  - timezone is valid IANA (`tzdata.LoadLocation`)
  - next_5_fires aktuální (regenerované server-side, ne LLM-trusted)
- [ ] Pokud parse fail → 422 s LLM `notes` jako error message
- [ ] Tests: 10 fixture inputs (CZ + EN), edge cases (DST, „last day of month", „every other Tuesday")

#### CRE-XXX-6.2 — Wizard step na new-routine UI
**Priority:** P0
**Estimate:** XS

**Acceptance:**
- [ ] V `app/(dashboard)/routines/new/page.tsx` (nová route z EPIC 1.4) přidat schedule step:
  - input field „Describe schedule in plain language" + CZ/EN placeholder
  - klik `Generate` → POST `:nl` → vyplní `cron_expr`, `timezone`, ukáže lidský preview blok + příští 5 fires chips
  - user může edit raw cron field (advanced)
- [ ] CTA „Generate" disabled pokud prompt &lt; 5 chars
- [ ] Spinner (LLM round-trip ~ 2–4s)

---

## 10. Out of scope MVP (Phase 2)

Tyto features **nejsou** v tomto PRD, ale jsou strukturálně připravené (data model hooks, API surface):

- **Waitpoints primitive** (token-based human-in-the-loop, public access tokens, CORS POST z prohlížeče bez backend roundtripu). Keeper bude refaktorován na waitpoint v Phase 2.
- **AI-authored versioned Pipelines** (`pipelines` + `pipeline_versions` immutable, JSON DSL, agent tool calls `crewship_pipeline.create/update/test_run`). EPIC 1.1 schema má `target_kind='pipeline'` placeholder.
- **Inbound MCP server** (`crewship mcp` Go subcommand, 15 tools, install wizard pro Claude Desktop / Cursor / Windsurf). EPIC 5.4 notifications kanál bude reusable jako MCP `list_pending_approvals` tool.
- **Official `@crewship/sdk` na npm** s typed `runs.subscribeToRun()`. EPIC 4.2 stream endpoint je foundation.
- **OTEL exporter** (Datadog / Honeycomb / Grafana). EPIC 4.1 span model je OTEL-compatible.
- **Suspend-and-respawn step state machine** (Temporal-level durability pro multi-day waits). Phase 4 — nedělat dříve než reálná data ukáží potřebu.
- **Pokročilé overlap policies** `cancel_other`, `terminate_other`, `buffer_all` — EPIC 1.7 je P2 a může klouznout do Phase 2.
- **Event triggers** na rutinách (webhook → fire). Phase 2.
- **Visual workflow editor** (drag-drop). Nikdy — JSON-first agent authoring je strategický směr.

---

## 11. Risks &amp; mitigations

| Riziko | Severity | Mitigation |
|---|---|---|
| **Migrace v77 z `agents.schedule_*` rozbije existující rutiny** | High | Datová migrace v rámci v77 (1 row na agent → 1 routine row); v77+1 dropne sloupce až po validaci v staging |
| **Recovery scan po restartu zaspí PENDING fires** | High | Test-suite restart-survival test je gate; max 5min stale window pro PENDING; INTERRUPTED je explicit final state |
| **Idempotency-key middleware přidá latency** | Medium | DB query je single index lookup (workspace_id, key); benchmark gate &lt; 5ms p99 |
| **Error fingerprinting clusteruje nepříbuzné errory** | Medium | Fingerprint zahrnuje agent_id + error_class + message_first_200; tuning na real data; mute mechanism (CRE-XXX-3.4) safety net |
| **Run detail waterfall renders pomalu pro runs s 1000+ spans** | Medium | Virtuoso virtualization; LOD (level-of-detail) — collapse spans &lt; 10ms; pagination spans pokud &gt; 5000 |
| **NL → cron LLM fail při edge cases (DST)** | Low | Server-side regenerate next_5_fires (LLM nelze trustovat); explicit DST warning v UI ("during DST transition this routine may run 0× or 2×") |
| **WS counters push spam na clienta** | Low | 500ms debounce; max 2 events/sec; reconnect with full snapshot |
| **Multi-replica deploy spustí duplicate fires** | High | EPIC 1.8 explicit single-leader env var; documentation update; EE auto-election v Phase 2 |
| **`auto_disable_after_failures` zablokuje rutinu při intermittent issue** | Medium | Default = 0 (vypnuto pro existující); user explicitly opt-in při create; inbox notification s 1-click resume |
| **Idempotency-key TTL expiruje předčasně, replay fail** | Low | Default 30d; configurable; cleanup sweeper kontroluje aktivní use před delete |

---

## 12. Success metrics

Po MVP shipu (target Q3 2026 end), tyto metriky musí být **prokazatelné**:

| Metrika | Cíl | Měření |
|---|---|---|
| **Restart survival rate** | 100% rutin přežije plánovaný restart bez ztráty fire | `routine_fires.status='INTERRUPTED'` count po test suite restart × 100 cyklů |
| **Idempotency miss rate** | &lt; 0.01% (žádné double-execution při retry) | Synthetic test: 1000 paralelních triggers se same key → exactly 1 execution |
| **Errors page time-to-resolution** | &lt; 5 min od fail po replay (median) | Telemetrie z UI: failed_at → replay_clicked_at |
| **Run detail open rate** | &gt; 30% userů otevře alespoň 1× za session | Frontend analytics |
| **Inbox action rate** | &gt; 50% notifikací má action (read/dismiss) do 24h | DB query na `notifications.read_at`/`dismissed_at` |
| **NL → cron success rate** | &gt; 80% prvních pokusů parsuje correctly | Telemetrie endpoint |
| **Crewship.ai homepage „reliability" pitch live** | 1 deploy | `/vs/trigger-dev` page exists s incident-report links |

---

## 13. Implementation order (sprint-level)

**Sprint 1 (week 1–2):**
- Paralelně:
  - BE: EPIC 1.1 (migration), EPIC 1.2 (scheduler refactor), EPIC 1.3 (catchup), EPIC 1.8 (leader)
  - BE: EPIC 2.1, 2.2, 2.3, 2.4 (idempotency + retry layer)
  - FE: EPIC 6 (NL → cron) — quick win, demo-ready end of sprint 1

**Sprint 2 (week 3–4):**
- BE: EPIC 1.4, 1.5, 1.6 (API + auto-disable + recovery)
- BE: EPIC 3.1, 3.2 (fingerprinting + aggregation API)
- BE: EPIC 4.1, 4.2 (span model + run detail API)
- FE: EPIC 1 routines list + detail UI; EPIC 5.1, 5.2 (counters + sidebar badges)

**Sprint 3 (week 5–6):**
- FE: EPIC 3.3 (Errors page UI)
- FE: EPIC 4.3 (run detail waterfall)
- FE: EPIC 5.3 (agent stats), 5.4 (Inbox)
- BE: EPIC 1.7 advanced overlap policies (P2, klouže pokud nestíhá)
- Marketing: `/vs/trigger-dev` srovnávací stránka, blog post „Why single binary matters for AI orchestration"

**Sprint 4 (week 7, polish):**
- E2E test suite napříč EPICs
- Migration na staging → production validation
- Demo recording („Crewship MVP robust foundation — 90s tour")

---

## 14. Sources

- Trigger.dev competitive deep-dive (7-fork research, 2026-05-07): `~/.claude/projects/.../memory/project_trigger_dev_competitive.md`
- Trigger.dev incident reports: [May 2024 full-month outage](https://trigger.dev/blog/incident-report-may-10-2024), [Sep 2025 us-east-1](https://trigger.dev/blog/incident-report-sep-26-2025)
- Trigger.dev issue #1566 (3,800 stuck/duplicate tasks): https://github.com/triggerdotdev/trigger.dev/issues/1566
- HN sentiment threads: [V4 GA](https://news.ycombinator.com/item?id=45250720), [V3 GA](https://news.ycombinator.com/item?id=41614642), [Show HN V1](https://news.ycombinator.com/item?id=34610686)
- Statusgator Trigger.dev metrics: 129 outages / 7 months
- Temporal Schedules docs (CatchupWindow, 6 overlap policies): https://docs.temporal.io/schedule
- Inngest replay + retry: https://www.inngest.com/docs/platform/replay
- Crewship Trigger.dev memory: `project_trigger_dev_competitive.md`, `project_ai_authored_pipelines_vision.md`, `feedback_mcp_gateway_not_strength.md`
