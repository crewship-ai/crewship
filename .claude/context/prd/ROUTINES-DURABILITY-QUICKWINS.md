# Routines: Durability + Observability quick-wins (PR #455)

**Status:** Shipped via PR #455.
**Problem:** Routines MVP had four sharp corners — one silent-bypass safety bug, one broken CLI output path, and two missing UI surfaces that turned computed observability into "trust me bro". Each is small individually; bundled they close the most painful spots of the routines MVP without pulling in the multi-week durability work (wait checkpointing, multi-replica state) that deserves its own design.
**Pattern:** Fix what's silently broken first; surface what we already compute second; defer what requires architectural change.
**Why now:** A user asked whether routines were production-ready. The honest answer was "the core works, but there are four observability + safety gaps that make operators nervous." Three of the four were quick to close.

---

## Scope — what shipped in PR #455

1. **`concurrency_key` empty-template fail-fast** (backend bug)
2. **CLI `routine dry-run` JSON decode** (broken since v83)
3. **Step-level cost + duration in Runs tab waterfall** (data we emit but didn't render)
4. **Dry-run report inline in detail panel** (response we computed and discarded)

## Out of scope — captured for follow-up PRs

- Wait checkpointing during `StepWait ≥ 5s` (1–2 weeks, needs design for resume-from-step semantics)
- DB-backed `RunRegistry` for HA (1 week, needs leader election + lock semantics)
- Routine-level retry budgets (separate spec; deeply opinionated tradeoffs)
- Routine versioning + immutable history (separate spec; needs schema migration plan)

---

## 1. `concurrency_key` empty-template fail-fast

### Problem

`concurrency_key: "vendor-alert-{{ inputs.vendor_id }}"` template fails
to resolve when `inputs.vendor_id` is empty — the empty key was being
treated as "no gate", which silently bypassed the concurrency limit
the operator declared. A burst of identical webhook payloads
fanned out N parallel routine runs instead of serializing.

### Fix

`internal/pipeline/concurrency.go` rejects unresolved-empty
`concurrency_key` values at dispatch time with a structured config
error. Operator sees the failure as a dispatch refusal, not a
mysterious surge in parallel runs.

### Rationale

Silent bypass is a class of bug where the failure mode is "unlimited
parallelism for a routine the author asked us to serialize" — that's
a denial-of-self for sustained traffic. An unresolved key value is
unambiguously a config error, not "no gate".

## 2. CLI `routine dry-run` JSON decode

### Problem

`crewship routine dry-run <slug>` returned raw JSON to stdout since
v83 when the response shape changed; the parser still expected the
old shape. CLI's "would_execute" tree, cost estimate, and resolved
tier rendering all silently fell back to "print whole blob" mode.

### Fix

`cmd/crewship/cmd_routine_validate.go` parser updated to the new
shape (matching `internal/pipeline/types.go:DryRunStep`).

### Rationale

`dry-run` is the operator's single most-used safety net before
flipping a routine to enabled; printing raw JSON eroded trust in
the tool. Fixing the parser also gives us the rendered output the
new detail-panel surface (#4) can re-use.

## 3. Step-level cost + duration in Runs tab waterfall

### Problem

`pipeline_runs` table stored per-step cost + duration as columns,
but the Runs tab's waterfall chart in the UI rendered only the
overall run total. Operators could see "this run cost 12 cents and
took 38 s" but not which step was the heavy one.

### Fix

`components/features/routines/runs-waterfall.tsx` now renders one
horizontal band per step with cost + duration overlays. Hover
tooltip shows the resolved tier and exact bytes.

### Rationale

The data was already in the table — we were just not showing it.
Cheap fix; high-value observability.

## 4. Dry-run report inline in detail panel

### Problem

The dry-run computed `would_execute` tree included resolved tier,
estimated cost per step, and total cost — all useful to the
operator at the moment they hit "Dry run". The detail panel
discarded the response after parsing.

### Fix

`triggerAction("dry_run")` in the detail panel now captures
`would_execute` into `dryRunResult` state, and the panel renders
the report conditionally. A new dry-run replaces the prior result;
closing dismisses it.

### Why above the tab bar, not in the Runs tab

The Runs tab is the "real runs" surface — dry runs don't increment
`invocation_count` and don't land in `pipeline_runs`. Splicing
dry-run-only rows in there would muddy the data model. The
detail-panel-level surface is also where the **Dry run** button
lives, so the report renders next to its trigger.

### Tests

The report is presentational; no unit test ships here. The data
shape it consumes (`DryRunStep`) is pinned at the type level by the
parser logic in `triggerAction`, which mirrors
`internal/pipeline/types.go:DryRunStep` field-for-field.

---

## Docs updated

- `docs/guides/routines.mdx` — new "Dry-run preview" section under Triggers
- `docs/guides/routines-cookbook.mdx` — new "`concurrency_key` validation" subsection under Recipe 5
- `docs/cli/routine.mdx` — `dry-run` section gains an example output block

## What this PR explicitly did not change

- No DB migrations
- No schema changes to the `routine.v1.json` spec
- No new API endpoints
- No new CLI subcommands (one existing one — `dry-run` — gets its decode fixed)
- No agent-facing behaviour changes (sidecar pipelines.go untouched)

The blast radius is intentionally small. Three bug fixes + two new
render surfaces over data the server already emits. Anything bigger
lands in its own PR.
