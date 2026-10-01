# Credential detail — the provenance the vault already has and never shows

Status: **Phase 0 (audit) complete. D1–D3, D5–D7 decided, D4 open — phases 1–4
ready to build, phase 5 blocked on D4.** Date: 2026-07-29.

Scope: the credential **detail** surface (card / sheet / CLI `credential get`),
not the vault's storage or mount behaviour — that is
[`CREDENTIALS-VAULT.md`](./CREDENTIALS-VAULT.md) and does not change here.

Baseline measured against **dev3** (`crewship --profile dev3`, workspace
`demo-cms3qu72`), 2026-07-29.

The premise this started from was a competitor comparison: four things a mature
secrets tool shows on a key's detail page — a **reference instead of a value**,
**who and what last used it** (including bindings that never did), **temporary
access with a countdown**, and **age against a rotation policy**. The audit
found that three of the four are mostly a *display* problem over data we already
store, and that the fourth — the reference — is the only one that needs a
semantic decision before any UI is worth building.

---

## Phase 0 — Audit

### The five paths that hand out a credential

```
  ┌─ boot injection ───────────────────────────────────────────────┐
  │  InternalHandler.resolveAgentCredentials                        │  knows agent_id
  │  internal/api/agent_config.go:629  (called from :178)           │  decrypts EVERY
  │  → container env vars + /secrets files + sidecar credstore      │  assigned cred
  └────────────────────────────────────────────────────────────────┘  ✗ NO audit

  ┌─ sidecar discovery ────────────────────────────────────────────┐
  │  InternalHandler.ListCredentials (metadata only)                │  knows crew_id
  │  internal/api/internal_credentials.go:98                        │  (bound token)
  └────────────────────────────────────────────────────────────────┘  ✗ NO audit

  ┌─ LLM proxy TokenSyncer ────────────────────────────────────────┐
  │  same endpoint, ?include_values=true, loopback-gated            │  knows nothing
  │  internal_credentials.go:269 → maybeRecordSidecarUse            │  ✓ USE, agent=""
  └────────────────────────────────────────────────────────────────┘  ← the ONLY writer

  ┌─ keeper execute / access ──────────────────────────────────────┐
  │  keeper_execute.go:350,:406 · keeper_request.go:190             │  knows agent,
  │  → INSERT INTO keeper_requests                                  │  crew, task,
  └────────────────────────────────────────────────────────────────┘  command, decision
                                                                       ✗ separate table

  ┌─ routine {{ secrets.<type> }} ─────────────────────────────────┐
  │  NewVaultCredentialResolver                                     │  knows run,
  │  internal/pipeline/credential_resolver.go:47                    │  routine, crew
  │  → decrypts, returns plaintext to the step runner               │  ✗ NO audit
  └────────────────────────────────────────────────────────────────┘
```

`credential_audit` (v69, `internal/database/migrate.go:1097`) accepts nine event
types (`internal/api/credential_audit.go:47`) and has exactly three writers
outside the mutation handlers. The mutation ones — `CREATED`, `ROTATE` — are
sound. Everything about *consumption* is not.

### Finding 1 — the only path that knows the agent writes nothing

`resolveAgentCredentials` (`agent_config.go:629`) is the boot delivery path. It
takes `agentID`, joins `agent_credentials`, enforces the #1373 lease gate,
decrypts every ACTIVE assigned credential, and returns plaintext that becomes
the container's env vars and `/secrets` files for the container's whole life.

It records nothing. Not a `USE` row, not a `last_used_at` bump, nothing.

This is the single highest-leverage gap in the subsystem: the one place with
`agent_id` in hand, decrypting real secrets, silent.

### Finding 2 — the one `USE` writer sits behind a loopback gate

`maybeRecordSidecarUse` (`internal_credentials.go:40`) is called from exactly
one place — `internal_credentials.go:269` — **inside the `includeValues`
branch**. `include_values=true` is rejected for any non-loopback caller
(`:102`), so in practice the only thing that ever produces a `USE` event is the
in-process LLM proxy `TokenSyncer`. A normal sidecar doing metadata discovery
produces none.

When it does fire it passes `"" /* agent unknown at this layer */` and an empty
IP, debounced to one row per credential per 60s (`:20`). So the richest signal
the timeline can carry today is *"an AI provider token was synced, by nobody, at
some point in the last minute."*

### Finding 3 — per-binding usage does not exist

`agent_credentials` has `id, agent_id, credential_id, env_var_name, priority,
created_at, mount_type` + the lease columns. No `last_used_at`. And
`credential_audit.agent_id`, the only other place attribution could live, is
never populated (Finding 2).

The API reflects this: `credentialResponse.AgentNames` is `[]string`
(`internal/api/credentials.go:109`) — names, nothing else. The detail sheet's
"Used by" tab (`credential-detail-sheet.tsx:329`) renders them as flat rows.

**"This binding has never been used" is therefore not answerable today.** That
is the one claim on the competitor card with real operational value — it is the
safest possible revocation, because nothing breaks — and it is precisely the one
we cannot make.

Measured on dev3:

```
$ crewship --profile dev3 credential list
GH_TOKEN ......................... AGENTS: 7
CLAUDE_CODE_OAUTH_TOKEN .......... AGENTS: 7
(11 more, AGENTS: 0)

$ crewship --profile dev3 credential audit GH_TOKEN -f json
[ { "event_type": "CREATED", "agent_id": null, "occurred_at": "2026-07-28T16:53:20Z" } ]

$ crewship --profile dev3 credential get GH_TOKEN -f json
  → no last_used_at
```

Seven bindings, one audit row, zero usage evidence.

### Finding 4 — two audit trails, the card shows the poorer one

`keeper_requests` (`migrate_consts_v02_v15.go:133`, extended v10/v102) carries
`credential_id`, `requesting_agent_id`, `requesting_crew_id`, `task_id`,
`intent`, `decision`, `reason`, `risk_score`, `command`, `exit_code`,
`created_at`, `decided_at` — and is indexed on `credential_id`.

That is a *better* record of credential access than `credential_audit` holds,
and nothing joins the two. The credential's Audit tab cannot show that an agent
asked for this key, why, what command it ran, or whether the Keeper allowed it.

### Finding 5 — leases are readable only from the agent side

v149 added `agent_credentials.expires_at`; v165 added `lease_source`,
`lease_issued_at`, `lease_request_id` and `keeper_governance_settings.
auto_lease_seconds`, plus the `LEASED` audit event
(`internal/api/credential_lease.go:152`).

The full "temporary access, auto-issued on a Keeper ALLOW, with provenance
pointing at the authorising request" model exists and is enforced on every
injection path. It is exposed on `GET /api/v1/agents/{id}/credentials`
(`agent_credentials.go:23,:60`) — **and nowhere on the credential**. To answer
"which agents currently hold a live lease on this key, and when does it lapse?"
an operator must iterate every agent.

We built the differentiator and never gave it a home.

### Finding 6 — there is no rotation policy

No `rotate_after_days`, no workspace default, no column anywhere. The "Stale"
state is derived in the **frontend** from a hardcoded constant:

```ts
// app/(dashboard)/credentials/page.tsx:88
const STALE_THRESHOLD_DAYS = 90
```

So the age-vs-policy gauge would today be rendering a rule the UI invented,
against a `last_used_at` that Findings 1–2 leave mostly NULL.

### Finding 7 — a never-used credential renders green

`deriveStatus` (`page.tsx:90`) returns `"Connected"` when `last_used_at` is
NULL: the `Stale` branch is nested inside `if (c.last_used_at)`. A credential
that has never once been touched is indistinguishable from a healthy one, and
can never go stale. On dev3 all 13 credentials show green.

### Finding 8 — secret references are type-addressed, and collide

`{{ secrets.<type> }}` resolves through `NewVaultCredentialResolver`
(`credential_resolver.go:47`) by **credential type** — never by name or id, so a
marketplace routine can run in any workspace. Selection:

```sql
WHERE workspace_id = ? AND UPPER(type) = UPPER(?) AND status = 'ACTIVE'
  AND deleted_at IS NULL AND (crew_id IS NULL OR crew_id = '' OR crew_id = ?)
ORDER BY CASE WHEN crew_id = ? THEN 0 ELSE 1 END, created_at DESC, id
LIMIT 1
```

dev3 holds three `CLI_TOKEN` / `GITHUB` credentials — `GH_TOKEN`,
`github-acme`, `github-globex`. `{{ secrets.cli_token }}` silently resolves to
whichever was created last. There is no warning at author time, no indication at
run time, and no audit row afterwards.

A "Copy reference" button on `GH_TOKEN`'s card would therefore hand the user a
string that points at a *different key*. This is the one item that must not ship
as a UI change.

Two further constraints on the reference idea: the namespace is scanned only for
`http | code | script | notify` steps (`executor_render.go:68`), so it does
nothing in an agent prompt; and the resolver decrypts without recording, so
routine usage leaves no trace on the very timeline the card would advertise.

### Finding 9 — the Audit tab renders less than the table stores

`credential_audit.metadata_json` holds the actor (`created_by`), provider, type,
and for `LEASED` the source / expiry / authorising request id. The timeline
endpoint returns it (`credential_audit.go:410`, exposed at
`router_crews.go:227`). The sheet (`credential-detail-sheet.tsx:351`) renders
only `event_type`, relative time, and IP — and drops `metadata` on the floor.

### Finding 10 — the two tables cannot be sorted against each other as text

D2 merges `credential_audit.occurred_at` with `keeper_requests.created_at /
decided_at`. Both columns are `TEXT` with `DEFAULT (datetime('now'))`, and the
Go writers disagree on format:

| writer | format | example |
|---|---|---|
| `credential_audit.go:147` | `time.RFC3339` | `2026-07-29T10:00:00Z` |
| `keeper_request.go:193`, `keeper_execute.go:355,:410` | `time.RFC3339` | `2026-07-29T10:00:00Z` |
| **`keeper_phase2.go:315`** | **`time.RFC3339Nano`** | `2026-07-29T10:00:00.5Z` |
| column DEFAULT (any writer that omits the column) | legacy | `2026-07-29 10:00:00` |

Sorted as text — which is what `ORDER BY occurred_at DESC` does on a SQLite
TEXT column — this is wrong in two ways:

- `'.'` (0x2E) < `'Z'` (0x5A), so a RFC3339Nano row at `10:00:00.5` sorts
  **before** a plain RFC3339 row at `10:00:00`. Inverted within the second.
- `' '` (0x20) < `'T'` (0x54), so **every** legacy-format row sorts before
  **every** RFC3339 row of the same date, regardless of actual time.

Today this is invisible because the two tables are never merged and
`credential_audit` has a single consistent writer. The moment the timeline
UNIONs them, "the history is in the wrong order" becomes a user-visible defect
— and a subtle one, since it only misorders rows within the same second or
across a format boundary. Related: `project_timestamp_defaults_followup` (85
`datetime('now')` DEFAULTs repo-wide).

---

## Decisions

**D1 — Attribution belongs at the injection point, not the fetch point.**
`USE` is recorded where the value is decrypted *for a named consumer*:
`resolveAgentCredentials` (agent), `/keeper/execute` (agent + command), the
pipeline vault resolver (run + routine). The loopback TokenSyncer path keeps its
current anonymous, debounced behaviour — it genuinely has no consumer identity.
Every new write is best-effort and debounced: a boot-path audit failure must
never fail a container start.

**D2 — One timeline, assembled at read.** Keeper decisions are *not* copied into
`credential_audit` (double-writing an append-only trail invites divergence, and
#1482 is a fresh reminder of what a second writer over an audit table costs).
The timeline endpoint UNIONs `credential_audit` with `keeper_requests` scoped to
the credential, tagging each row with its source. `credential_audit` stays the
tamper-evident record of what the *vault* did; `keeper_requests` stays the
record of what the *governance plane* decided.

**D3 — Rotation policy is a column with a workspace default**, not a frontend
constant. `credentials.rotate_after_days INTEGER` (NULL = inherit),
`workspaces.credential_rotate_after_days INTEGER NOT NULL DEFAULT 0` (0 = no
policy, i.e. today's behaviour). Age is measured from the later of `created_at`
and the newest `credential_rotations.rotated_at`. The FE constant is deleted.

**D5 — `agent_names` becomes `bindings`.** The detail response returns objects
(`agent_id, agent_name, env_var_name, mount_type, granted_at, expires_at,
lease_source, last_used_at, last_used_context`), not strings. `agent_names` is
kept as a deprecated mirror for one release so the MCP panes
(`components/features/mcp/`) don't break.

**D4 — OPEN: name-addressed secret references.** Required before "Copy
reference" can exist (Finding 8). Three shapes, none free:

| | behaviour | cost |
|---|---|---|
| **a** | add `{{ secrets.name.<NAME> }}` alongside the type form | two namespaces to explain; type form keeps its silent collision |
| **b** | make `{{ secrets.<X> }}` prefer an exact name match, fall back to type | no author-visible change; a credential *named* `api_key` changes meaning |
| **c** | keep type-only; make the collision loud (`routine doctor` error + author-time warning when >1 ACTIVE row of a type) | no new syntax, no portability loss; no per-key reference, so no button |

Recommendation: **(c) now, (a) later if demand appears.** The button is a
convenience; a reference that resolves to the wrong secret is an incident. (c)
also removes a live footgun on every existing routine — which is worth doing
whether or not the button ever ships.

Whatever is chosen must not break existing type-addressed routines: marketplace
portability is why the resolver is type-addressed in the first place.

**D6 — Every list on the card is sorted, explicitly and reversibly.** No list
ships with implicit insertion order. Details in "UI conventions" below.

**D7 — Icons come from the app-wide `lucide-react` vocabulary.** No emoji, no
inline SVG, no per-card icon dialect. Details in "UI conventions" below.

---

## UI conventions (binding)

The comparison mock this epic came from uses emoji (🔑, 🔒) and an unsorted
event list. Neither ships. This section is a requirement, not a suggestion —
the detail card is a *dense operational surface*, and density without ordering
is the failure mode it is meant to fix.

### Icons

`lucide-react` (`^1.24.0`) is the app-wide icon set — skills, tools, memory,
journal, credentials all already import from it, including this very sheet
(`credential-detail-sheet.tsx:19`). Every new element in this epic uses it:

| element | icon |
|---|---|
| credential (title, list row) | `Key` |
| binding / agent row | `Bot` (agent), `Users` (crew inheritance) |
| live lease + countdown | `Timer` |
| lapsed lease | `TimerOff` |
| pending Keeper request | `ShieldQuestion` |
| Keeper allowed / denied | `ShieldCheck` / `ShieldX` |
| vault timeline event | `Activity` |
| rotation | `RefreshCw` |
| overdue against policy | `AlertTriangle` |
| never used | `CircleOff` |
| sort affordance | `ArrowUpDown` / `ArrowUp` / `ArrowDown` |

Sizes follow the sheet's existing scale (`h-3` inline, `h-3.5` on actions).
Semantic colour comes from the #749 token palette (`text-warn`,
`text-destructive`, `text-success`) — never a hardcoded hex.

The animated icon components in `components/ui/*.tsx` (`key.tsx`, `brain.tsx`,
`activity.tsx`, `bell.tsx` — `motion/react`, imperative
`startAnimation`/`stopAnimation` handles) are a *separate* set, currently used
in one place (`components/ai-elements/reasoning.tsx`). They are appropriate for
a section/hero header at most; they are not to be used for row-level icons,
where 20 simultaneous motion controllers would cost more than they convey.

### Sorting

**Timeline.** Default: newest first. Direction toggleable. Filterable by source
(`vault` / `keeper` / `all`) and by event type. The UNION **must not** order on
the raw TEXT columns (Finding 10) — it normalizes both sides to a single
comparable instant in SQL and orders on that, with `id` as the stable
tie-break so keyset pagination can't duplicate or skip a row:

```sql
ORDER BY sort_ts DESC, id DESC   -- sort_ts = normalized RFC3339 UTC, never the raw column
```

A unit test feeds one row of each of the four formats from Finding 10 at known
instants and asserts the returned order — that test is the point of the phase,
not an afterthought.

**Bindings, pending requests, rotations.** Every column header is a sort
control, with a stated default:

| list | default sort | sortable columns |
|---|---|---|
| bindings | never-used first, then oldest-used | agent, env var, granted, last used, lease expiry |
| pending requests | oldest first (an aging approval is the urgent one) | agent, intent, risk, age |
| rotations | newest first | rotated, grace, by |

Pinned rows win over the chosen sort — a lapsed lease and a pending request
stay at the top the way `routines-list-view.tsx:52` pins live runs. A sort that
buries the thing demanding attention is worse than no sort.

**Reuse, don't re-hand-roll.** `SortBtn` already exists
(`components/features/routines/routines-list-view.tsx:274`: active-column
state, direction arrows, `align`), but its props are typed to that file's local
`SortKey`, so nobody else can use it — `issues-list-view.tsx` sorts without it
and the credentials page sorts with a single-key `Select` and no direction
toggle at all (`page.tsx:160,:597`). Phase 1 **generalizes `SortBtn` into the
shared kit** (generic key type, alongside `components/layout/sidebar-kit.tsx`
per the #749 page-chrome convention) and moves routines onto it. Three
hand-rolled sorters is already one too many; this epic must not add a fourth.

### The timeline is history, and its rows are destinations

Confirmed: the timeline is the credential's history, and after D2 it is the
*whole* history — vault events and governance decisions in one list. Rows
become clickable drill-throughs, which is new work (today's rows are inert
`<li>`s, `credential-detail-sheet.tsx:351`):

| row | opens |
|---|---|
| Keeper decision | the request — intent, command (scrubbed), risk score, decision |
| `LEASED` | the authorising request via `lease_request_id`, and the binding it minted |
| `ROTATE` | the `credential_rotations` record — grace window, who |
| `USE` (after phase 3) | the agent, and the run / routine that consumed it |

Keyboard reachable, `aria-label` on every sort control and every drill-through.

### Space

Everything above lands in the **existing sheet and its four tabs** — no new
full-page route, no third layout. The pending-request panel is the only element
allowed above the tab strip, and only while a request is actually pending.

---

## Non-goals

- No change to storage, encryption, or mount behaviour (`CREDENTIALS-VAULT.md`).
- No value reveal in the UI. The value stays write-only; "Show value" from the
  mock is out of scope, and the "every reveal is audited" property it implies is
  free only because reveal does not exist.
- No new external secret backends, no dynamic secrets, no PKI.
- No usage sparkline until phase 3 has produced real events. A chart over
  today's data would be a flat line at zero, dressed as insight.

---

## Phase 1 — Show what we already store *(no migration)*

1. **Audit tab renders `metadata`** — actor, source, provider, and for `LEASED`
   the source + expiry + authorising request. Secrets are structurally absent
   from `metadata_json`; a test asserts the renderer allowlists keys rather than
   dumping the blob, so a future writer can't leak a value into the UI.
2. **Timeline UNIONs `keeper_requests`** per D2. Response rows gain
   `source: "vault" | "keeper"`, and for keeper rows `intent`, `decision`,
   `risk_score`, `command` (scrubbed via `internal/scrubber`), `agent_id`.
3. **`agent_id` resolves to a name** in the response, so the timeline can say
   *who* once phase 3 starts populating it.

4. **Normalized sort key** per Finding 10 + D6: the UNION emits `sort_ts` and
   orders `sort_ts DESC, id DESC`. Raw columns are never the sort key.
5. **`SortBtn` moves to the shared kit** (D6) with a generic key type;
   `routines-list-view.tsx` migrates onto the shared one in the same PR so the
   extraction is proven by two call sites, not one.

- API: `GET /api/v1/credentials/{id}/audit` gains `source`, `?source=` filter,
  `?sort=asc|desc`, and keyset pagination on `(sort_ts, id)`.
- CLI (rule #3): `crewship credential audit` gains a SOURCE column,
  `--source vault|keeper|all` (default `all`) and `--sort asc|desc`.
- FE: `credential-detail-sheet.tsx` Audit tab — sortable, filterable, rows
  clickable per "UI conventions"; lucide icons per the table there.
- Tests: acceptance test drives the CLI binary (rule #3); **unit test feeds one
  row in each of Finding 10's four timestamp formats at known instants and
  asserts the returned order** — plus a keyset-pagination test that walks two
  pages across a tie and asserts no row is repeated or skipped.
- Docs: `docs/guides/credentials.mdx` — audit section.

## Phase 2 — Lease + pending request on the credential card *(no migration)*

The mock's "temporary access with a countdown" panel, over v149/v165 data.

1. **`GET /api/v1/credentials/{id}` returns `bindings`** (D5) including
   `expires_at`, `lease_source`, `lease_issued_at`, `lease_request_id`.
2. **Pending Keeper requests for this credential** — `keeper_requests` where
   `decision IS NULL`, with `intent`, `risk_score`, requesting agent, age.
   Approve / deny reuse the existing Keeper endpoints; no new decision path.
3. **FE**: live countdown for leased bindings, a distinct row state for a lapsed
   lease, and the pending-request panel above the fold.

- CLI: `crewship credential bindings <id>` (`--json`), showing GRANTED, EXPIRES,
  SOURCE per binding, with `--sort` mirroring the FE columns.
- Tests: a leased binding at `expires_at = now-1s` must render as lapsed, not
  hidden — matching the injection gate's fail-closed semantics. Sort test: a
  lapsed lease and a pending request stay pinned above the chosen column sort
  (D6).
- Docs: `docs/guides/credentials.mdx` + the Keeper guide's escalation section.

## Phase 3 — `USE` attribution *(migration)*

The core of the epic. Everything else is presentation; this is the missing fact.

```sql
ALTER TABLE agent_credentials ADD COLUMN last_used_at TEXT;
ALTER TABLE agent_credentials ADD COLUMN last_used_context TEXT; -- 'boot' | 'keeper' | 'routine:<id>'
CREATE INDEX IF NOT EXISTS idx_agent_credentials_last_used
    ON agent_credentials(credential_id, last_used_at);
```

Writers, all best-effort + debounced, reusing the CAS pattern already proven in
`maybeRecordSidecarUse` (`internal_credentials.go:48` — it exists because
CodeRabbit caught a double-write race on the read-then-decide version):

- `resolveAgentCredentials` — `USE` with `agent_id`, `metadata.source="boot"`,
  plus the per-binding `last_used_at` bump. **Must not fail or slow a container
  start**; one row per (credential, agent) per debounce window.
- `/keeper/execute` — `USE` with `agent_id` and the authorising request id, so
  the vault timeline and the governance timeline cross-reference.
- pipeline vault resolver — `USE` with `metadata.run_id` + `routine_id`. The
  resolver has no `agent_id`; the run is the consumer. This closes the "routines
  leave no trace" half of Finding 8.

Only then:

- `bindings[].last_used_at` is real → **"never used" is trustworthy**, and the
  card can offer per-binding revoke as the safe first cleanup.
- `deriveStatus` gains an `Unused` state (Finding 7): never used + older than
  the policy is *not* green.
- The 14-day usage sparkline becomes meaningful (`credential_audit` already has
  `idx_credential_audit_occurred`).

CLI: `crewship credential usage <id>` — per-binding last-used and a
`--unused-only` filter for cleanup sweeps.

Risk to watch: `credential_audit` row volume. The debounce is per (credential,
agent, source) rather than per credential, so a 20-agent crew rebooting can add
20 rows a minute. Ship with the existing `crewshipd_credential_audit_dropped_
total` metric watched, and reuse the `admin prune` path for retention.

## Phase 4 — Rotation policy *(migration)*

Per D3. Age gauge, `Overdue` state, "Schedule rotation" wired to the existing
grace-overlap rotation flow (`credential_rotation.go`) — the rotation mechanism
itself is done and unchanged.

- CLI: `crewship credential policy set <id> --rotate-after 90d`,
  `crewship credential policy list --overdue`.
- The overdue set becomes a natural notification category producer — worth
  revisiting once `security` has a producer (see
  `PRD-NOTIFY-CHANNELS-2026.md`, Finding 1: the category exists and nothing
  fires it).

## Phase 5 — References *(blocked on D4)*

Under recommendation (c): author-time + `routine doctor` warning when more than
one ACTIVE credential of a referenced type exists in scope, naming the row that
would win. No UI button. Under (a): the button copies
`{{ secrets.name.GH_TOKEN }}` and the resolver gains an exact-name branch.

---

## Acceptance — verified on dev3, via the CLI

Per the ops rule, everything below is driven with `crewship --profile dev3`,
never against the box.

1. `credential audit GH_TOKEN` shows Keeper decisions interleaved with vault
   events, each tagged with its source. *(phase 1)*
2. `credential bindings GH_TOKEN` lists 7 bindings with grant time and lease
   state; a `--ttl`-assigned one shows a shrinking countdown. *(phase 2)*
3. Boot a crew agent holding `GH_TOKEN`; `credential usage GH_TOKEN` names that
   agent within one debounce window, and the other 6 bindings report **never
   used**. *(phase 3)*
4. A routine using `{{ secrets.cli_token }}` produces a `USE` row carrying the
   run id — and `routine doctor` warns that three credentials match that type.
   *(phase 3 + 5)*
5. `credential policy set GH_TOKEN --rotate-after 90d` flips the card to
   Overdue at 91 days. *(phase 4)*
6. **Sorting holds across the format boundary**: with a Keeper decision written
   by `keeper_phase2.go` (RFC3339Nano) and a vault event written by
   `credential_audit.go` (RFC3339) in the same second,
   `credential audit GH_TOKEN --sort desc` returns them in true chronological
   order — and `--sort asc` returns the exact reverse. *(phase 1, Finding 10)*

The runtime harness (`scripts/test-harness/`) covers credential escalation vs.
self-service already; phase 3 extends that suite with a boot-attribution
assertion rather than adding a parallel harness.

---

## Related code

- Schema: `migrate.go:1034` (`last_used_at` / `last_used_ips`), `:1097`
  (`credential_audit`), `:1127` (`credential_rotations`);
  `migrate_consts_v98_credential_attribution.go`,
  `migrate_consts_v149_credential_lease.go`,
  `migrate_consts_v165_credential_lease_mint.go`,
  `migrate_consts_v02_v15.go:133` (`keeper_requests`)
- API: `internal/api/credentials.go` (`credentialResponse:70`),
  `credentials_loaders.go`, `credential_audit.go`, `credential_lease.go`,
  `agent_credentials.go`, `agent_config.go:629`, `internal_credentials.go`,
  `keeper_execute.go`, `keeper_request.go`
- Routes: `internal/api/router_crews.go:227` (audit), `:386` (get)
- Pipeline: `internal/pipeline/credential_resolver.go:47`,
  `executor_render.go:68`
- CLI: `cmd/crewship/cmd_credential.go`, `cmd_credential_mutate.go`,
  `cmd_credential_assignment.go`; the D4(c) warning lands in
  `cmd/crewship/routine_doctor_credentials.go`, which already walks a routine's
  credential requirements
- FE: `components/features/credentials/credential-detail-sheet.tsx`,
  `app/(dashboard)/credentials/page.tsx:88` (`STALE_THRESHOLD_DAYS`), `:160`
  + `:597` (single-key `Select` sort to be replaced)
- Sorting/icons: `components/features/routines/routines-list-view.tsx:274`
  (`SortBtn`, to be generalized), `components/layout/sidebar-kit.tsx` (#749
  page-chrome kit), `lucide-react` (app-wide icon set),
  `components/ui/{key,brain,activity,bell}.tsx` (animated set — header use only)
- Storage/mount reference: [`CREDENTIALS-VAULT.md`](./CREDENTIALS-VAULT.md)
