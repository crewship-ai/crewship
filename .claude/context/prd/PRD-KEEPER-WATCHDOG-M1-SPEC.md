# M1 Implementation Spec — Keeper Watchdog: admin-defined watch spec

Status: ready-to-implement · Owner: (assignee) · Grounded against `main` post-#1025 (M0 merged) · EPIC #1001
Parent PRD: `.claude/context/prd/PRD-KEEPER-WATCHDOG-2026.md`

This is a **complete, standalone implementation brief**. You should not need
other context to execute it. It states what M1 is, why the surrounding code is
shaped the way it is (M0 background), the exact seams to touch with file:line
references, the recommended design with rationale, acceptance tests (TDD), docs
to ship, and the gotchas that bit us in M0.

---

## 0. TL;DR — what you are building

Today the Keeper watchdog flags a **hard-coded** set of anti-patterns (tight
loops, scope creep, destructive sequences, credential probing) baked into a Go
prompt template. M1 lets an OWNER/ADMIN **author their own watch rules per
workspace** — a mix of (a) **structured presets** ("watch credential access",
"watch egress", "watch memory writes", "watch destructive fs", "watch sensitive
file reads") and (b) **free-form natural-language rules** ("flag any read of
~/.ssh or id_rsa", "flag credential access outside 08:00–18:00"). These get
injected into the Keeper evaluator prompts so the LLM judges against the
operator's policy, not just the built-in list. Full API + CLI + UI parity,
mirroring M0.

**Deliverable shape:** one migration, a handful of Go edits centered on a single
injection seam, CLI subcommands, one UI editor row, tests, docs. No new
service, no new LLM path.

---

## 1. Why the code looks like this (M0 background — read before touching)

M0 (#1025) built the per-workspace governance layer this milestone extends. Key
decisions and where they live — understanding these prevents you from
re-litigating them:

- **The watchdog is per-workspace, opt-in, default OFF.** No server inheritance.
  Resolution goes through one seam: `governance.Resolve(ctx, db, logger, wsID)`
  → returns `Settings` (disabled + defaults when unconfigured). See
  `internal/keeper/governance/governance.go:101`. M0 originally tried a
  "inherit server default" model; a self-review found it made read sites
  disagree on what "enabled" means, so we collapsed to opt-in default-OFF.
  **M1 must keep this contract**: an unconfigured workspace has an empty watch
  spec and monitoring stays off until enabled.
- **Storage is one row per workspace** in `keeper_governance_settings`
  (migration v137, `internal/database/migrate_consts_v137_keeper_governance.go`).
  The `governance.Settings` struct is the single accessor. Extend this — do NOT
  create a parallel table.
- **The API PUT is a partial update** (all-pointer body, nil = leave unchanged),
  so each CLI subcommand and the UI form send only what they change without a
  read-merge-write race. `internal/api/keeper_governance.go:59` (body) + `:76`
  (merge). M1's watch-spec writes must use the same partial-update shape.
- **Targeting is a superset** (a configured security contact is highlighted via
  the inbox item's `TargetUserID` **and** the MANAGER fanout is kept as a
  fallback). Not relevant to M1's writes, but don't be surprised by it.
- **The evaluators are injection-hardened** (random delimiters + `%q` escaping
  around agent-controlled text). The watch spec is *admin-authored config*, a
  different trust tier — inject it as authoritative instruction, not as fenced
  untrusted data (see §5).

---

## 2. Scope

### In scope (M1)
1. Per-workspace **watch spec**: free-form NL rules + a set of structured presets.
2. Injection of the resolved watch spec into the **behavior** and
   **credential-access** evaluator prompts (the two paths an agent's live
   activity flows through). Skill/memory/negative F4 sweeps are optional stretch
   (see §9).
3. API extension (GET returns the spec; PUT partial-updates it), CLI parity
   (`crewship keeper watch …`), UI editor (textarea + preset multi-select).
4. Docs (`docs/guides/keeper.mdx`) + tests.

### Out of scope (later milestones — do NOT build)
- CEL / structured predicate DSL, cron-time conditions, regex engines. M1 is
  **LLM-evaluated natural language + presets**, per the parent PRD non-goals.
- Per-agent watch rules (governance is per-workspace by design).
- The local-Ollama model selection (M2) and managed model container (M2).
- Actionable approve/dismiss reviews (M3).

---

## 3. Design decision + rationale (the load-bearing choice)

**Resolve the watch spec ONCE inside `Gatekeeper.Evaluate`, not at each call
site.** This is the recommended plumbing. Rationale:

1. Every evaluator path funnels through `Gatekeeper.Evaluate`
   (`internal/keeper/gatekeeper/gatekeeper.go:236`) and the single `buildPrompt`
   choke point (`:280`). The behavior evaluator is not a separate LLM path — it
   builds an `EvalRequest` and calls `e.gk.Evaluate(...)`
   (`internal/keeper/gatekeeper/behavior_evaluator.go:158`).
2. Every `EvalRequest` already carries the workspace id at
   `req.Request.WorkspaceID` (set at `keeper_request.go:158` and
   `behavior_evaluator.go:141`; already read in `Evaluate` at `gatekeeper.go:290`).
3. **The sampled behavior hook has no DB handle** (`Hook` struct at
   `internal/keeper/behaviorhook/behaviorhook.go:53`). A per-call-site
   `EvalRequest.WatchSpec` field would force new DB plumbing into
   `behaviorhook.New` and its server wiring — avoidable.
4. `Gatekeeper` is constructed in exactly two places, both with `db` in scope:
   `internal/server/server.go:636` and `internal/server/keeper_phase2.go:119`.

So: give `Gatekeeper` a way to resolve the spec, resolve it in `Evaluate` from
`req.Request.WorkspaceID`, thread it into `buildPrompt`. One seam covers access
+ behavior + all F4 paths for free.

**Keep the gatekeeper package DB-agnostic** — inject a resolver *function*, not a
`*sql.DB`. Define in the gatekeeper package:
```go
// WatchSpecResolver returns the workspace's compiled watch-spec prompt block
// (already preset-expanded), or "" when none/unresolvable. Never errors.
type WatchSpecResolver func(ctx context.Context, workspaceID string) string
```
Pass it into `New(...)`; default to a nil-safe no-op resolver when absent (tests,
bring-up). At the two construction sites, supply a closure that calls
`governance.Resolve(ctx, db, logger, wsID)` and returns the compiled block
(§4.3). This keeps `internal/keeper/gatekeeper` free of an `internal/database`
dependency and free of an import cycle with `internal/keeper/governance`.

---

## 4. Implementation — layer by layer

### 4.1 Storage (migration v138)

**Next free migration version is 138** (max is v137 at
`internal/database/migrate.go:1614`; see §11 gotcha — verify it's still free the
day you branch).

New file `internal/database/migrate_consts_v138_keeper_watch_spec.go`:
```go
package database

// migrationKeeperWatchSpec (v138) adds the admin-authored watch spec to the
// per-workspace keeper governance row (#1001 M1). watch_spec is free-form NL
// rules; watch_presets is a JSON array of preset keys. Empty = no custom
// watch rules (the evaluator falls back to its built-in anti-pattern list).
const migrationKeeperWatchSpec = `
ALTER TABLE keeper_governance_settings ADD COLUMN watch_spec TEXT NOT NULL DEFAULT '';
ALTER TABLE keeper_governance_settings ADD COLUMN watch_presets TEXT NOT NULL DEFAULT '';
`
```
Register in `internal/database/migrate.go` right after the v137 entry:
```go
{version: 138, name: "keeper_watch_spec", sql: migrationKeeperWatchSpec},
```
> Note: SQLite runs each `ALTER TABLE … ADD COLUMN` fine in one migration string;
> the migrate runner already executes multi-statement SQL (v137 was single, but
> v133/v135 etc. run multi-statement). Confirm the runner splits on `;` — if it
> executes the whole string as one `Exec`, SQLite accepts both ALTERs in one call.

**Backup intent (CI-gated):** the table `keeper_governance_settings` is already
`IntentInclude` in `internal/backup/intent.go` — new columns ride along, no
change needed. (Only *new tables* need a `BackupTableIntent` entry.)

### 4.2 Governance struct + accessors

`internal/keeper/governance/governance.go`:
- Extend `Settings` (**:27**):
  ```go
  WatchSpec    string   `json:"watch_spec"`
  WatchPresets []string `json:"watch_presets"`
  ```
- `Get` (**:43**): add `watch_spec` and `watch_presets` to the SELECT; scan
  `watch_spec` into a string and `watch_presets` (JSON TEXT) into `[]string` via
  `json.Unmarshal` (guard empty → nil).
- `Upsert` (**:66**): add both columns to the INSERT column list, the `VALUES`,
  and the `ON CONFLICT … DO UPDATE SET`. Marshal `WatchPresets` to JSON TEXT
  (`""` when empty for a stable default).
- `Resolve` (**:101**): **no logic change** — it returns the whole `Settings`,
  so `WatchSpec`/`WatchPresets` come through automatically.

### 4.3 Preset catalog + compiler (new, in the governance package)

Presets are stable keys that expand to canned NL rule strings. Define them where
both the API (validation) and the prompt compiler can see them —
`internal/keeper/governance/presets.go` (new):

```go
package governance

// WatchPreset is a stable preset key. The map value is the human rule injected
// into the evaluator prompt when the preset is enabled.
var WatchPresets = map[string]string{
    "credentials": "Flag credential access that is disproportionate to the stated task — rapid or bulk access to multiple secrets, access to high-security-level credentials without a matching justification, or credential reads unrelated to the current work.",
    "egress":      "Flag network egress or tool calls that look like data exfiltration — posting data to non-allowlisted hosts, encoding/piping secrets to the network, or unexpected outbound connections.",
    "memory":      "Flag writes or deletes to agent/crew memory that look like tampering — overwriting prior facts, mass deletions, or planting misleading entries.",
    "destructive": "Flag destructive filesystem or database operations without verification — recursive deletes (rm -rf), DROP/TRUNCATE/DELETE without a WHERE clause, force-pushes, or overwriting source files wholesale.",
    "secret_files": "Flag reads of sensitive files — ~/.ssh, id_rsa/id_ed25519, .env files, cloud credential files (~/.aws, ~/.config/gcloud), or private keys.",
}

// CompileWatchSpec turns a Settings' presets + free-form rules into a single
// prompt block, or "" when there are none. Admin-authored config, so it is
// authoritative instruction text (NOT fenced as untrusted). Length-capped.
func CompileWatchSpec(s Settings) string { /* expand presets in a stable order, append s.WatchSpec, cap length */ }
```
- **Stable order** when expanding presets (sort keys) so the prompt is
  deterministic (matters for reproducibility + our determinism harness).
- **Length cap** the whole block (reuse the gatekeeper truncation helper's limit
  or a local constant ~2 KB) so a huge NL spec can't blow the prompt budget.
- `ValidatePresets([]string) error` — reject unknown keys at the API layer.

### 4.4 Gatekeeper plumbing (the injection seam)

`internal/keeper/gatekeeper/gatekeeper.go`:
- Add the `WatchSpecResolver` type (§3) and a field on `Gatekeeper` (**:193**):
  `watchSpec WatchSpecResolver`. Add it as a param (or functional option) to
  `New` (**:208**); nil → a no-op resolver returning `""`.
- In `Evaluate`, **before** `buildPrompt` at **:280**:
  ```go
  spec := ""
  if g.watchSpec != nil {
      spec = g.watchSpec(ctx, req.Request.WorkspaceID)
  }
  prompt := g.buildPrompt(req, spec)
  ```
- Thread `spec string` through `buildPrompt` (**:375**) into each builder.
  Inject at these exact points (append the block as a labelled section):
  - **`buildAccessPrompt`** (`:395`): after the "CURRENT REQUEST TO EVALUATE"
    block closes (**:423**), before "Decision criteria:" (**:425**).
  - **`buildBehaviorPrompt`** (`:478`): augment the hard-coded "Anti-patterns to
    flag:" list at **:497-501** — append the watch-spec block after the built-in
    list (keep the built-ins; the watch spec is additive).
  - Skill/memory/negative builders: optional (§9) — same pattern, before the
    final JSON-instruction line.
- **Injection format** (see §5): a labelled authoritative block, e.g.
  ```
  [WORKSPACE WATCH POLICY — operator-defined; flag activity matching these rules]
  <compiled spec>
  ```
  Place it **above** the untrusted conversation/tool-arg fences and **above** the
  final strict-JSON instruction line (e.g. `:431`, `:507`) so it can't disrupt
  the response contract.

Update the two construction sites to pass the resolver closure:
- `internal/server/server.go:636` — `db` (`deps.DB`) is in scope.
- `internal/server/keeper_phase2.go:119` — `db` is local at `:118`.
  Closure: `func(ctx, wsID string) string { return governance.CompileWatchSpec(governance.Resolve(ctx, db, logger, wsID)) }`.

### 4.5 API (extend the M0 partial-update handler)

`internal/api/keeper_governance.go`:
- `keeperGovernancePutBody` (**:59**): add `WatchSpec *string` and
  `WatchPresets *[]string`.
- `Put` merge block (**:86-98**): apply when non-nil. **Validate presets** with
  `governance.ValidatePresets` (400 on unknown key). Cap `WatchSpec` length
  server-side (e.g. 4 KB → 400 if longer) so the DB/prompt stays bounded.
- `keeperGovernanceResponse` (**:35**) already embeds `governance.Settings`, so
  GET returns the new fields automatically.
- Journal audit entry (**:138**): include `watch_preset_count` +
  `watch_spec_len` (do NOT log the full spec text into the journal payload if
  you want to keep entries small — a length + preset list is enough for audit).
- Routes: no new route — the existing `GET`/`PUT /api/v1/admin/keeper/governance`
  (`internal/api/router_admin.go:55-57`) carry it. (A dedicated `keeper watch`
  endpoint is unnecessary; keep one governance resource.)

### 4.6 CLI (`crewship keeper watch …`)

`cmd/crewship/cmd_keeper.go`:
- Extend the local `keeperGovernance` mirror struct (**:44**) with `WatchSpec`
  + `WatchPresets`.
- Add a `keeperWatchCmd` sub-group with:
  - `keeper watch get` — prints the current spec + presets (reuse
    `getKeeperGovernance`, `:67`).
  - `keeper watch set <text>` — partial-update `{"watch_spec": <text>}` via
    `putKeeperGovernanceFields` (`:80`). Support reading from stdin (`-`) for
    multi-line rules.
  - `keeper watch clear` — `{"watch_spec": ""}`.
  - `keeper watch preset add <key>` / `remove <key>` / `list` — presets. `add`/
    `remove` need read-merge for the array (GET current presets, mutate, PUT the
    new array) — this is the one place a read-merge is unavoidable; keep it small
    and document it. `list` prints the catalog (`governance.WatchPresets` keys +
    descriptions) with a ✓ on enabled ones.
- Model the subcommands on `keeperThresholdCmd` (**:281**) and `keeperContactCmd`
  (**:231**, for the `--clear`/string-arg shape). Register in `init()` (**:314**).
- Client-side: validate preset keys against the catalog before the call so the
  operator gets a specific error (server also validates).

### 4.7 UI (extend the M0 panel)

`components/features/admin/keeper-governance-panel.tsx`:
- `GovernanceResponse` (**:36**) + `FormState` (**:55**): add `watch_spec` +
  `watch_presets`.
- Add two `SettingsRow`s in the render (**:242-333**):
  - A **preset multi-select** (checkboxes/toggles for the 5 catalog keys with
    their descriptions as captions).
  - A **`Textarea`** for the free-form NL rules, with a caption + a short
    placeholder example ("flag any read of ~/.ssh; flag credential access
    outside 08:00–18:00").
- Hydrate on load (**:130**), include in dirty-tracking (**:155**) and the
  `save()` PUT body (**:160**). Gate editing on `abilities.can("manage","Workspace")`
  (**:80**), same as M0.
- Show the preset catalog descriptions client-side (hard-code them to match
  `governance.WatchPresets`, or add a tiny `GET` for the catalog — hard-coding is
  fine for 5 stable keys; add a code comment pointing at the Go source of truth).

---

## 5. Prompt-injection safety (do this right)

The evaluators are hardened against **agent-controlled** input:
`gatekeeper.go` wraps `req.ConvHistory` in unguessable random-delimiter fences
(`randomDelimiter()` `:625`, used `:402-411`) and `%q`-escapes every
agent-derived scalar (Intent `:417`, Command `:420`, tool args
`behavior_evaluator`/`buildBehaviorPrompt:491`).

**The watch spec is a different trust tier — admin-authored config, gated by
`roleManage` (OWNER/ADMIN) and journal-audited.** It legitimately *instructs*
the evaluator what to flag, so:
- **Do** present it as an authoritative, clearly-labelled instruction block
  (`[WORKSPACE WATCH POLICY …]`), placed in the criteria region, **above** the
  untrusted fences and **above** the final JSON-instruction line.
- **Do NOT** `%q`-escape it into a data literal — that would defeat its purpose.
- **Do** length-cap it (server-side validation + a prompt-side cap) so it can't
  blow the token budget or push the JSON-instruction line out of the model's
  attention.
- Residual risk (a malicious admin authoring a prompt that neuters the
  evaluator) is **out of the M1 threat model** — an OWNER/ADMIN can already
  disable the watchdog entirely. Note this in the code comment so a reviewer
  doesn't flag it.

---

## 6. Acceptance criteria (TDD — write tests FIRST, per project rule)

Ship a failing test, then the code. Concretely:

**Go unit — governance (`internal/keeper/governance/governance_test.go`,
`presets_test.go`):**
- `Get`/`Upsert` round-trip `WatchSpec` + `WatchPresets` (incl. empty → nil).
- `Resolve` returns the spec for a configured workspace, empty for unconfigured.
- `CompileWatchSpec`: presets expand in stable order; unknown/empty → "";
  free-form appended; total length capped.
- `ValidatePresets` rejects unknown keys.

**Go unit — gatekeeper (`internal/keeper/gatekeeper/*_test.go`):**
- With a stub `WatchSpecResolver` returning a sentinel string, assert the
  sentinel appears in the built prompt for **both** `buildAccessPrompt` and
  `buildBehaviorPrompt` (drive via `Evaluate` with a fake `llm.Provider` that
  captures the prompt — see the existing `kp2Provider` fake).
- Assert the watch block sits **before** the final JSON-instruction line and
  **before** the conversation fence.
- Nil resolver → no watch block, no panic (backward-compatible).

**Go unit — API (`internal/api/keeper_governance_test.go`):**
- PUT `{watch_spec, watch_presets}` round-trips through GET.
- PUT with an unknown preset key → 400.
- PUT with an over-long `watch_spec` → 400.
- Partial update: setting `watch_spec` alone leaves `enabled`/`contact`/presets
  untouched (mirror the M0 partial-update tests).

**CLI (`cmd/crewship/cmd_keeper_test.go`):**
- `keeper watch set/clear` send the right single-field PUT body.
- `keeper watch preset add/remove` read-merge the array correctly.
- `keeper watch preset list` prints the catalog with ✓ on enabled.
- Unknown preset key → non-zero exit, no PUT.

**Frontend (`components/features/admin/__tests__/keeper-governance-panel.test.tsx`):**
- Renders the textarea + preset toggles; hydrates from GET.
- Save PUTs the watch spec + presets; dirty-tracking enables Save.

**Runtime harness (extend `scripts/test-harness/test-keeper.sh`):**
- `crewship keeper watch set "..."` then `keeper watch get` round-trips against
  a live server; `preset add/remove/list` works; a bogus preset is rejected.
- (Driving an actual escalation that *fires because of* a watch rule needs the
  gatekeeper LLM/Ollama configured — out of scope for the harness, same as M0.)

Gates (all green before PR): `gofmt`, `go vet ./...`, `go build ./...`,
`go test ./internal/... ./cmd/...`, `npx tsc --noEmit`, panel vitest.

---

## 7. Docs to ship in the same PR (project rule #2)

- `docs/guides/keeper.mdx` — extend the "Watchdog Governance" section with a
  "Watch rules" subsection: the preset catalog (table: key → what it flags),
  the free-form NL rules, and the `crewship keeper watch …` CLI block. Keep the
  "opt-in, default OFF" framing.
- Update this spec's parent PRD milestone line (M1 → shipped) when done.

---

## 8. Files you will touch (checklist)

| Layer | File | Change |
|---|---|---|
| Migration | `internal/database/migrate_consts_v138_keeper_watch_spec.go` (new) | 2 columns |
| Migration | `internal/database/migrate.go:1614` | register v138 |
| Governance | `internal/keeper/governance/governance.go:27,43,66` | Settings + Get + Upsert |
| Governance | `internal/keeper/governance/presets.go` (new) | catalog + CompileWatchSpec + ValidatePresets |
| Gatekeeper | `internal/keeper/gatekeeper/gatekeeper.go:193,208,236,280,375,395,478` | resolver + thread spec + inject |
| Wiring | `internal/server/server.go:636`, `internal/server/keeper_phase2.go:119` | pass resolver closure |
| API | `internal/api/keeper_governance.go:59,86,138` | body + merge + validate + audit |
| CLI | `cmd/crewship/cmd_keeper.go:44,231,281,314` | `keeper watch` subgroup |
| UI | `components/features/admin/keeper-governance-panel.tsx:36,55,130,155,160,242` | textarea + preset select |
| Docs | `docs/guides/keeper.mdx` | Watch rules subsection |
| Harness | `scripts/test-harness/test-keeper.sh` | watch set/get/preset assertions |
| Tests | the `*_test.go` + panel test above | TDD |

---

## 9. Optional stretch (decide with Pavel, don't assume)

- **F4 sweep prompts** (skill/memory/negative). The same injection seam covers
  them for free if you thread the spec into their builders too. Value is lower
  (those are audit sweeps, not live agent activity) — ship the behavior +
  access paths first, treat F4 as a follow-up unless asked.
- **Per-preset severity / risk bump** — out of scope for M1; presets are
  on/off. Note if requested.

---

## 10. Open decisions for Pavel (surface these before building)

1. **NL-only vs NL + presets in M1?** The parent PRD leans **both** (presets are
   cheap and make it usable without prompt-writing). This spec assumes both. If
   Pavel wants NL-only first, drop §4.3 preset catalog + the preset CLI/UI and
   ship just `watch_spec`.
2. **Preset catalog contents** — the 5 keys in §4.3 (`credentials`, `egress`,
   `memory`, `destructive`, `secret_files`) are a proposal. Confirm the set +
   wording with Pavel; the wording is the actual prompt text the LLM sees.
3. **Where the watch spec applies** — behavior + credential-access (this spec).
   Confirm whether F4 sweeps are wanted now (§9).

---

## 11. Gotchas from M0 (will bite you the same way)

- **Migration version race.** M0 grabbed v136, main merged another v136 the same
  day (#1009), and we had to renumber to v137 mid-review. **`git fetch origin
  main` and re-check the max migration version the moment you open your PR and
  again right before merge.** If someone took v138, renumber.
- **`internal/api` has load-flaky tests** — `TestConsolidateRun_HappyPath_ReturnsWorkerID`
  and an OAuth-timeout test (dials TEST-NET `192.0.2.1`) pass in isolation but
  flake under the full-package parallel run. If CI's `Go (macos-arm64)`/`Go`
  fails on one of *those*, it's not you — rerun or (if you've confirmed it's one
  of these) merge past it. Always confirm by running the failing test in
  isolation (`go test -run <Name> -count=3`).
- **`gofmt` stragglers from other merges** land on your branch via rebase and
  fail `Go Lint` — just `gofmt -w` the flagged file (it may not even be yours).
- **CodeRabbit is disabled by team preference** — do your own adversarial review
  (the `/code-review` workflow at high effort is what we used on M0; it found the
  opt-in-vs-inherit design bug). Don't wait for CodeRabbit.
- **Commits: no Claude co-author / generated-by trailer.**

## 12. How to validate on dev2 (runtime, like M0)

Deploy the merged main and drive the harness against the live server (the
project rule: "test it like a real app"):
```
CREWSHIP_DEPLOY_HOST=crewship-dev CREWSHIP_DEPLOY_PATH=/opt/crewship_2 \
  ./scripts/deploy-dev.sh main
# harness (build CLI from main — the installed one may be older):
go build -o /tmp/crewship-main ./cmd/crewship
cd scripts/test-harness && CREWSHIP=/tmp/crewship-main \
  SERVER=http://192.168.1.201:8082 ./test-keeper.sh
```
- The dev2 login token is issued for host `192.168.1.201`, so drive the harness
  with `--server http://192.168.1.201:8082` (NOT the `crewship-dev2.unifylab.cz`
  name — it trips the token host-mismatch guard).
- A slot reconciler reverts the dev slot to its pinned branch every ~10 min —
  run the harness promptly after deploy.
- M0's `test-keeper.sh` (12/12) is the template; add the watch-spec assertions.

---

*Foundation: M0 shipped in #1015 (security binding) + #1025 (governance),
runtime-validated on dev2 (harness 12/12). This spec continues EPIC #1001. When
M1 lands, M2 = local-Ollama governance model + managed model container, M3 =
actionable reviews + per-crew watch level.*
