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

1. A `crewship` binary the harness can find. It looks, in order, at `$CREWSHIP`,
   then `<repo-root>/crewship`, then whatever `crewship` is on `PATH` — so from
   anywhere in the clone, either of these is enough:
   ```bash
   go build -o crewship ./cmd/crewship   # from the repo root
   # ...or just have the installed CLI on PATH
   ```
   `seed` below is client-side from that binary, so make sure the one you
   resolve is current — rebuild it, or `crewship self-update` the installed one.
2. A dev server is up and **seeded** with the release-demo template. Run this
   with the installed CLI on `PATH` (or from the repo root, see below):
   ```bash
   SEED_ANTHROPIC_API_KEY=sk-ant-... \
   SEED_GITHUB_TOKEN=ghp_...           # optional, enables the GitHub scenario
   crewship seed --nuke --with-memory --with-users --wait-provision
   # ...or ./crewship seed ... if you built the binary into the repo root above
   ```
   > `--nuke` **wipes the target workspace**. Pass `--server` explicitly, or be
   > sure `CREWSHIP_SERVER` points where you think it does.
3. Your shell targets that server. Prefer the env var (scopes to one shell):
   ```bash
   export CREWSHIP_SERVER=<your devN url>
   crewship whoami        # confirm it talks to the right instance
   ```
4. `jq` installed (recommended — JSON assertions fall back to grep without it).

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

Override any of: `CREWSHIP` (binary path — absolute, or relative to your cwd),
`SERVER`/`CREWSHIP_SERVER`, `ASK_TIMEOUT`, `POLL_TIMEOUT`, `POLL_INTERVAL`.

## What each suite asserts

| Suite | Validates |
|---|---|
| `test-memory.sh` | agent recalls a nonce fact in a **fresh session**; a **crew-tier** fact is readable by a peer in the same crew; it does **not** leak cross-crew; **pins** are always available; `memory search`/`status` corroborate. `--soak N` re-checks durability over N minutes. |
| `test-delegation.sh` | a **lead delegates** a subtask to a peer and reports the result back (corroborated by a new peer chat session); a lead **hires an ephemeral** specialist (or it lands as an approval waitpoint under guided autonomy). |
| `test-notifications.sh` | a routine **run completes** (exit code + records status); the **completion event** is observable via `routine watch --once`; a **notification lands** in the feed; a **failed run** surfaces a `failed_run` inbox item (best-effort). |
| `test-orchestration.sh` | the seeded **cron schedules** are present + enabled; an **agentless** routine runs at **token-zero cost**; a **HITL approval gate** pauses → is approved via CLI → resumes; **cross-tier** eval returns structured results (`EVAL=0` to skip the token-heavy block). |
| `test-credentials.sh` | human **create + assign**; the API never returns the plaintext **value**; an agent **escalates** for a credential and a human grants it; agent **self-service** creation attributed `actor_type=agent` (probe — SKIPs if not wired). |
| `test-datastore-redis-auth.sh` | **datastores are always password-protected** (Redis case): applying a stock `redis:*` sidecar with **no auth declared** mints an `AUTO_MANAGED` **REDIS_PASSWORD** credential (value never returned), boots the server with `--requirepass`, and proves **auth — not the crew bridge — is the gate**: an unauthenticated `PING` over the (reachable) bridge is refused **NOAUTH**, while `PING` with `$REDIS_PASSWORD` returns **PONG**. **Requires Docker + a provisioned crew — dev-VM only, not in `run-all.sh`.** SKIPs the redis-cli-in-agent checks if the runtime lacks `redis-cli`; host-side `docker exec` confirmation is documented, not run (CLI-only policy). |
| `test-keeper.sh` | Keeper watchdog **governance** via the real `crewship keeper` CLI: `status` reports server + workspace state; `enable`/`disable` **flip the toggle** (round-trips); `threshold N` sets the DENY-notify risk and **rejects out-of-range**; `contact <email>` **targets a named OWNER/ADMIN** and **rejects a non-member**. Control-plane only — a full credential ESCALATE needs the gatekeeper LLM, out of scope here. SKIPs if the installed CLI has no `keeper` command. |
| `test-determinism.sh` | a pure-transform recipe yields **byte-identical** `@json` output across N runs; prints a latency/cost **baseline**. |
| `test-realworld-github.sh` | an agent uses the in-container **`gh`** CLI against a public repo (read-only); SKIPs if `gh` isn't authenticated. |
| `test-orphan-token-reap.sh` | the **#1385 stable-master** remediation lever: `admin reap-orphan-containers` is wired (API↔CLI parity), and a **dry-run sweep against the running server finds ZERO orphans** — proving the fail-safe classifier never false-positives a healthy container — and is **non-mutating + idempotent**. Self-**SKIPs** when the provider isn't docker (503). The restart-invalidation property itself is locked by the Go unit tests. |
| `test-keeper-ingress-fence.sh` | the **internal keeper HTTP surface** rejects every request with no token / forged / zero / spoofed-XFF (fence holds), across a **method matrix** (GET/PUT/DELETE/PATCH/OPTIONS), a **malformed-token fuzz** (empty, 8 KB, CRLF, SQL/shell/path-ish), an **oversized body**, and **other `/internal/*`** routes; asserts **no info leak** in rejections and that the **public API still needs auth**; runs a **constant-time timing probe**; flags whether the **network-origin gate** is defeated behind the proxy (off-host → 403 not 404 ⇒ static `X-Internal-Token` is the sole guard). *The one suite that uses raw `curl` — the internal channel has no CLI by design.* |
| `test-keeper-toctou.sh` | a decision reflects **injection-time** state, not approval-time: `rotate --grace-seconds 0` scrubs the stale value now, **grace-window rotate + rotation-cancel** scrubs early, a **concurrent rotate race** leaves the credential coherent + `ACTIVE`, `unassign`/`reassign` toggles the binding the keeper requires, **delete-while-assigned** revokes cleanly, **peer value** is never exposed; **SKIPs** the container-only deferred race (T2) and the token-only double-execute (T10). |
| `test-keeper-audit-integrity.sh` | decisions leave a **durable, monotonic trace**: lifecycle events grow the `credential audit` timeline, **REVOKE** on delete, a granted escalation resolves off `PENDING` (**approve** path) and a **denied** one is recorded (deny path), `system keeper` exposes scrubber + model; **SKIPs** the load-only fail-silent drop (T6) and the token-only returned-vs-persisted mismatch (T7). |
| `test-keeper-load.sh` | **correctness under load** (the real "perf" tests): read-path **p50/p95/p99** latency baseline at `CONC` concurrency, server stays **healthy through a write burst** (no 5xx / health flap), the **rate-limiter** yields 200/429 never 5xx, **pending-count stays consistent** under concurrent reads, keeper **status reachable under load**; **SKIPs** inbox-flooding advisory-loss (T8) and evaluator-saturation fail-closed (T9). Tunables: `CONC`, `SAMPLES`, `BURST`. |

> **Keeper adversarial suite** (the three `test-keeper-*` above) is opt-in:
> `WITH_KEEPER_SECURITY=1 ./run-all.sh`. Design + the full test catalog (T1–T13,
> which findings each maps to) live in
> `.claude/context/notes/keeper-adversarial-test-suite-2026-07-12.md`.

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
| 3 | Agentless `cost-spike-probe` (a `type:code` step) **fails to run** | **FIXED** — the CEL CodeRunner landed with Routines-Max (PR #715); the token-zero assertion is live again. | `internal/pipeline/` (MultiCodeRunner) |
| 4 | Synchronous `routine run` of an **approval gate** surfaces **no pollable waitpoint** | **FIXED** — the run parks as WAITING with a waitpoint token; the original symptom was a CLI gap (`waitpoints list --format json` printed the human table so jq never saw the token). The suite now FAILS (not skips) if no waitpoint appears. | `cmd/crewship/cmd_routine_waitpoints.go` |
