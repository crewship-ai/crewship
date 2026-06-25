# Crewship CLI integration harness

Runtime, end-to-end tests that drive the **real `crewship` CLI** against a
running dev server — the way a real operator (and a real agent) uses it. These
complement the Go/Vitest unit suites: they validate behaviour that only exists
at runtime and can't be pre-seeded — agent **memory** recall, **crew-shared**
memory, **notifications** landing after a routine run, recipe **determinism**,
and credential **self-service vs. escalation**.

> Per project policy, all operations go through the local CLI pointed at your
> clone's dev target — never a DB shell or hand-rolled `curl`. Dogfooding the
> CLI is the QA budget.

## Prereqs

1. A dev server is up and **seeded** with the release-demo template:
   ```bash
   SEED_ANTHROPIC_API_KEY=sk-ant-... \
   SEED_GITHUB_TOKEN=ghp_...           # optional, enables the GitHub scenario
   crewship seed --nuke --with-memory --with-users --wait-provision
   ```
2. Your shell targets that server. Prefer the env var (scopes to one shell):
   ```bash
   export CREWSHIP_SERVER=<your devN url>
   crewship whoami        # confirm it talks to the right instance
   ```
3. `jq` installed (recommended — JSON assertions fall back to grep without it).

## Run

```bash
cd scripts/test-harness

./run-all.sh                 # memory + notifications + credentials + determinism
WITH_GITHUB=1 ./run-all.sh   # + real-world GitHub scenario
./run-all.sh --quick         # skip the determinism sweep

# individual suites
./test-memory.sh
./test-memory.sh --soak 60   # durability: re-recall every 5 min for 60 min
RUNS=10 ROUTINE=normalize-dates ./test-determinism.sh
ROUTINE=classify-ticket ./test-notifications.sh
```

Override any of: `CREWSHIP` (binary path), `SERVER`/`CREWSHIP_SERVER`,
`ASK_TIMEOUT`, `POLL_TIMEOUT`, `POLL_INTERVAL`.

## What each suite asserts

| Suite | Validates |
|---|---|
| `test-memory.sh` | agent recalls a nonce fact in a **fresh session**; a **crew-tier** fact is readable by a peer in the same crew; it does **not** leak cross-crew; **pins** are always available; `memory search`/`status` corroborate. `--soak N` re-checks durability over N minutes. |
| `test-delegation.sh` | a **lead delegates** a subtask to a peer and reports the result back (corroborated by a new peer chat session); a lead **hires an ephemeral** specialist (or it lands as an approval waitpoint under guided autonomy). |
| `test-notifications.sh` | a routine **run completes** (exit code + records status); the **completion event** is observable via `routine watch --once`; a **notification lands** in the feed; a **failed run** surfaces a `failed_run` inbox item (best-effort). |
| `test-orchestration.sh` | the seeded **cron schedules** are present + enabled; an **agentless** routine runs at **token-zero cost**; a **HITL approval gate** pauses → is approved via CLI → resumes; **cross-tier** eval returns structured results (`EVAL=0` to skip the token-heavy block). |
| `test-credentials.sh` | human **create + assign**; the API never returns the plaintext **value**; an agent **escalates** for a credential and a human grants it; agent **self-service** creation attributed `actor_type=agent` (probe — SKIPs if not wired). |
| `test-determinism.sh` | a pure-transform recipe yields **byte-identical** `@json` output across N runs; prints a latency/cost **baseline**. |
| `test-realworld-github.sh` | an agent uses the in-container **`gh`** CLI against a public repo (read-only); SKIPs if `gh` isn't authenticated. |

## Design notes

- **No `set -e` inside suites** — a failed assertion records and continues, so
  one failure doesn't hide the rest. Each suite exits non-zero if anything failed.
- **Nonce tokens** (`FALCON-7F3A9C`) make memory recall provable: a correct
  answer can only come from persisted memory, not training data or a guess.
- **Fresh sessions**: every `crewship ask` is a new chat with no carried
  history, so cross-session recall genuinely exercises the memory engine.
- **Honest SKIPs**: known gaps (agent credential self-service, code-step
  CodeRunner) SKIP with a note rather than false-failing.

## Known product findings (live dev runs, 2026-06-25)

The harness validated these as PASSING: agent memory recall across sessions,
crew-shared memory + cross-crew isolation, pins, ephemeral hire, routine
completion on the activity rail, recipe determinism, credential create/assign +
value-never-returned, cross-tier eval. The runs also surfaced four items the
suites now handle honestly (xfail/skip with a documented reason) — kept here so
they aren't silently lost:

| # | Finding | Status | Surface |
|---|---|---|---|
| 1 | Routine **completion** is not pushed to the notification *feed* | **By design** — completions live on the activity rail (`pipeline.run.completed`) + `routine records`; the feed is for escalations/approvals/mentions. Test checks the rail; feed is a bonus. | `internal/api/pipelines_exec.go` |
| 2 | `failed_run` **inbox** item is created only for **scheduled** runs | **By design** — ad-hoc/CLI failures surface via exit code + `status=failed` record; the inbox is for unattended runs. Test asserts the record, skips the inbox for manual runs. | `internal/pipeline/schedules.go` (`alertFailedScheduledRun`) |
| 3 | Agentless `cost-spike-probe` (a `type:code` step) **fails to run** | **Known gap** — the production **CodeRunner is not wired** ("convert to type: agent_run"). The token-zero assertion is xfail until it lands. | `internal/pipeline/runner_code.go` (placeholder) |
| 4 | Synchronous `routine run` of an **approval gate** surfaces **no pollable waitpoint** | **Product issue** — the sync run blocks the handler in `WaitFor` (run sits `running`, no queryable waitpoint within ~90s); HITL likely needs an async/202 trigger. Test best-effort, skips + kills the hung run. | `internal/pipeline/runner_wait.go`, `internal/api/pipelines_exec.go` |
