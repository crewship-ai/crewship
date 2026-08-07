# Crewship CLI integration harness

Runtime, end-to-end tests that drive the **real `crewship` CLI** against a
running dev server — the way a real operator (and a real agent) uses it. These
complement the Go/Vitest unit suites: they validate behaviour that only exists
at runtime and can't be pre-seeded — agent **memory** recall, **crew-shared**
memory, a routine run's outcome being **observable** afterwards, recipe
**determinism**, and credential **self-service vs. escalation**.

> Per project policy, all operations go through the local CLI pointed at your
> clone's dev target — never a DB shell or hand-rolled `curl`. Dogfooding the
> CLI is the QA budget.

## Which of these run in CI

The CI workflow now has two distinct harness protections. The shell job runs
`scripts/test-harness-integrity.sh` (ShellCheck plus helper-call resolution)
and its negative test. The `harness-pr` job builds the binary from the same
commit, starts one disposable seeded server, and runs the explicit
no-provider subset below. The full matrix remains nightly.

`nightly-harness.yml` (nightly 03:10 UTC + `workflow_dispatch`) now boots an
ephemeral instance per matrix square — `crewship start` → `crewship seed
--skip-issues` → one suite — and files/refreshes a single deduplicated
`nightly-harness-alert` issue when a square goes red (same containment pattern
as `security.yml`'s `scheduled-report`: bot author + label + marker, and a
green night closes the bot's own issue).

**Hard gate today — control-plane suites, no provider credential required:**

| Suite | Verified |
|---|---|
| `test-keeper.sh` | 18 assertions, 0 fail |
| `test-keeper-ingress-fence.sh` | 53 assertions, 0 fail (5 non-fatal findings) |
| `test-keeper-toctou.sh` | 15 assertions, 0 fail |
| `test-orphan-token-reap.sh` | 1 assertion + 1 self-SKIP (no docker provider) |
| `test-attack-surface.sh` | 4 assertions (Tier A), Tier B SKIPs by design |

All five were driven end-to-end against a clean-DB bootstrap of exactly that
sequence before the workflow was written.

**PR subset:** `test-keeper.sh`, `test-keeper-config.sh`,
`test-keeper-aux.sh`, `test-inbox.sh`, and `test-orphan-token-reap.sh`.
These are the deterministic control-plane suites selected for ordinary PRs;
provider-backed, external-service, Docker-dependent, and destructive suites
remain nightly/dev-only for the reasons listed above and in the workflow.

**Not gated yet — the runtime suites.** `test-memory.sh`,
`test-delegation.sh`, `test-crew-links.sh`, `test-notifications.sh`,
`test-orchestration.sh`, `test-determinism.sh`, `test-credentials.sh`,
`test-keeper-audit-integrity.sh` and `test-keeper-load.sh` all reach their
assertions only *after* a real agent reply or a real routine run. Without a
provider credential the server answers

```
LLMRunner: provider: no active Anthropic credential in workspace
```

and everything downstream fails. There is **no Anthropic secret in this
repository's Actions secrets**, so the `harness-runtime` job is skipped and
the alert issue says so in as many words. Configure
`SEED_ANTHROPIC_API_KEY` as a repository secret and that job becomes a hard
gate with no workflow edit. Do not read a green nightly as "memory and
delegation still work" until then.

**Never run in CI, on purpose:** `test-realworld-github.sh`,
`test-secretless-github.sh`, `test-notifications-shoutrrr.sh`,
`test-ollama-local.sh`, `test-datastore-redis-auth.sh`,
`test-gitlink-realworld.sh` and `test-redteam-insider.sh` — each needs a
secret, an external service, a provisioned crew, or a shared dev slot it is
allowed to mutate. The reasons are spelled out per suite in the workflow
header.

> `test-gitlink-realworld.sh` is *safe* in CI — with no forge token it SKIPs
> and exits 0 before it contacts the server or any forge — but it would prove
> nothing there, so it belongs in `EXCLUDED_SUITES` rather than in a matrix.
> It is claimed in `nightly-harness.yml` — both in `EXCLUDED_SUITES` and in
> the "Suites deliberately NOT run here" header block — so
> `suite-coverage-guard`, which fails the nightly on any `test-*.sh` claimed
> by no tier, is satisfied. The exclusion is for honesty, not safety: a suite
> that can only ever skip in CI would report green while proving nothing
> about the forges it exists to check.

### Adding a suite: the tiers are enforced, not documented

Deliberately no total is quoted here or in the workflow. The first draft of that
header said "19 suites" and was wrong within days — `test-crew-links.sh` and
`test-secretless-github.sh` landed in merged PRs while it was being written. A
number in prose cannot be enforced, so the invariant is stated as set membership
instead:

> every `scripts/test-harness/test-*.sh` on disk must be claimed by exactly one
> of the two matrices in `nightly-harness.yml` or by its `EXCLUDED_SUITES` list

and the `suite-coverage-guard` job **fails the workflow** on any suite in no
tier, any tier naming a suite that no longer exists, and any suite claimed
twice. It reads the two run lists out of the matrices themselves — there is no
second copy of them to drift — so adding a suite to a matrix is all the
bookkeeping there is. A new `test-*.sh` that nobody wired up turns the nightly
red instead of silently getting zero coverage.

So: add the file, then put it in a matrix (with a per-suite timeout) or in
`EXCLUDED_SUITES` with the reason in the header block. There is no third option
the guard will accept.

### A green run states what it did not verify

`nightly-harness.yml` has a `coverage-summary` job that writes a tier table to
the run's summary page — on the **success** path too, not only on failure, and
on manual dispatches as well as the schedule. Until a provider credential
exists, that table carries a warning banner saying in as many words that the
green tick covers the control-plane tier only and that the run does **not**
show memory, delegation, notifications, orchestration, determinism or credential
escalation still working. The banner disappears by itself the moment
`SEED_ANTHROPIC_API_KEY` is configured and the runtime tier starts executing.
The point is that the run page never claims more coverage than the run had.

`run-all.sh` is not used by CI: it is a flat sequential loop with no per-suite
timeout, so one wedged `ask` consumes the whole job budget and the summary
cannot say which subsystem broke. The workflow gives every suite its own
runner, its own timeout, and its own red square.

Two CI-specific notes worth keeping:

- **`crewship seed` needs retrying from cold.** Crew creation validates each
  runtime image against `mcr.microsoft.com`, and the first call reproducibly
  exceeds the CLI's client timeout (`invalid runtime_image: … context deadline
  exceeded`). The workflow retries up to 3×; locally it took 3 attempts from
  cold and then succeeded.
- **`test-attack-surface.sh` must be handed its token.** It resolves a bearer
  token by matching `$SERVER` against a `cli-config` profile, which cannot
  resolve a loopback address — so without `CREWSHIP_ATTACK_TOKEN` +
  `CREWSHIP_ATTACK_WS` it SKIPs its entire Tier A block and reports green
  having proved nothing. The workflow mints both via `crewship token create`
  and `crewship whoami --format json`.

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
4. `jq` installed (recommended — JSON assertions fall back to grep without it;
   `test-secretless-github.sh` requires it outright and SKIPs itself without).
5. Only for `test-secretless-github.sh`, and only for the sections that need
   them — put these in the clone's `.env.local` and export them into the shell
   that runs the suite. **Never** into the repo, a script, or a commit; use a
   throwaway GitHub account, a **private** test repo, and tokens scoped to that
   repo alone:
   ```bash
   SEED_GITHUB_TOKEN=github_pat_...      # fine-grained PAT, Contents: R+W
   SEED_GITHUB_TOKEN_CLASSIC=ghp_...     # optional, classic `repo` scope
   SEED_GITHUB_SSH_KEY=~/.ssh/id_ed25519 # key, or a path to one
   SEED_GITHUB_TEST_REPO=owner/secretless-test
   ```
   Every section SKIPs cleanly when its own inputs are absent, so a run with
   none of them still reports a useful result.
6. Only for `test-gitlink-realworld.sh`, and one per forge — each enables that
   provider's half and nothing else:
   ```bash
   GITLINK_GITHUB_TOKEN=ghp_...    # read-only GitHub PAT; `public_repo` (classic)
                                   # or a fine-grained token with no repo access
   GITLINK_GITLAB_TOKEN=glpat-...  # gitlab.com PAT with `read_api`
   ```
   The objects it reads are public and long-lived, and it only ever `GET`s
   them — nothing is written to either forge. With neither variable set the
   whole file SKIPs and exits 0 without contacting anything.

   Two preconditions, both worth knowing before you read a run as coverage —
   and they do **not** behave the same way when unmet.

   The resolved CLI must be new enough to have `credential create
   --account-label` and `issue link` (#1758). Without either, the suite SKIPs.

   The workspace must not already hold an **older** ACTIVE credential for that
   provider labelled with the forge host — that one wins credential resolution,
   and the suite then **FAILS**, it does not skip. `crewship credential
   list/get` do not expose `account_label`, so this condition is stated rather
   than checked; the good-path failure message names it as the likely cause.
   Exposing `account_label` on those commands would let the suite check its own
   precondition, and would answer "which credential will this link resolve to?"
   for operators too — worth doing, not done here.

## Run

### Pull-request gate

Every pull request runs the explicit control-plane subset below against a
fresh server and database built from the same commit:

| Suite | Why it is in the PR gate |
|---|---|
| `test-keeper.sh` | CLI governance toggles and validation |
| `test-keeper-config.sh` | configuration round-trips and fail-closed setup |
| `test-keeper-aux.sh` | auxiliary keeper configuration contracts |
| `test-inbox.sh` | waitpoint approval and inbox resolution contract |
| `test-orphan-token-reap.sh` | non-mutating orphan classifier and CLI parity |

The provider-dependent suites (`test-memory.sh`, `test-delegation.sh`,
`test-crew-links.sh`, `test-notifications.sh`, `test-orchestration.sh`,
`test-determinism.sh`, `test-credentials.sh`, `test-keeper-audit-integrity.sh`,
and `test-keeper-load.sh`) remain nightly because they require an LLM/provider
credential. GitHub, Ollama, Redis, Shoutrrr, and insider suites remain nightly
or opt-in because they require external services, Docker, or destructive/shared
fixture access. The nightly workflow's coverage guard keeps those exclusions
explicit; none are silently dropped.

```bash
cd scripts/test-harness

./run-all.sh                 # memory + notifications + credentials + determinism
WITH_GITHUB=1 ./run-all.sh   # + real-world GitHub scenario
WITH_GITLINK=0 ./run-all.sh  # - the real-forge git-link suite (it is in by default;
                             #   without GITLINK_*_TOKEN it skips and costs nothing)
WITH_SECRETLESS=1 ./run-all.sh   # + the secretless-GitHub proof (T-H1…T-H9)
./run-all.sh --quick         # skip the determinism sweep

# individual suites
./test-memory.sh
./test-memory.sh --soak 60   # durability: re-recall every 5 min for 60 min
RUNS=10 ROUTINE=normalize-dates ./test-determinism.sh
ROUTINE=classify-ticket ./test-notifications.sh
```

Override any of: `CREWSHIP` (binary path — absolute, or relative to your cwd),
`SERVER`/`CREWSHIP_SERVER`, `CREWSHIP_PROFILE`, `CREWSHIP_WORKSPACE`,
`ASK_TIMEOUT`, `POLL_TIMEOUT`, `POLL_INTERVAL`.

> **`CREWSHIP_PROFILE` is not optional as often as it looks.** The CLI refuses
> to send a token to a host it was not issued for, so pointing `CREWSHIP_SERVER`
> at a slot whose credential lives under a differently-named profile fails with
> a mismatch error, not a 401. Profile names do not have to match slot names — a
> `dev3` profile can point at dev1. Name the profile explicitly, and use
> `CREWSHIP_WORKSPACE` when a reseed has moved the workspace id out from under
> the profile:
>
> ```bash
> CREWSHIP_SERVER=https://crewship-dev3.unifylab.cz \
> CREWSHIP_PROFILE=dev3-fresh CREWSHIP_WORKSPACE=<workspace id> ./run-all.sh
> ```

## What each suite asserts

| Suite | Validates |
|---|---|
| `test-first-projects.sh` | Executes every command printed by `docs/guides/first-projects.mdx`: creates a crew and agent, writes shared output, assigns a credential, recalls crew memory, scaffolds and validates a routine, and reads the Journal. Requires a seeded instance and a provider credential for the agent-backed rungs. |
| `test-memory.sh` | agent recalls a nonce fact in a **fresh session**; a **crew-tier** fact is readable by a peer in the same crew; it does **not** leak cross-crew; **pins** are always available; `memory search`/`status` corroborate. `--soak N` re-checks durability over N minutes. |
| `test-delegation.sh` | a **lead delegates** a subtask to a peer and reports the result back (corroborated by a new peer chat session); a lead **hires an ephemeral** specialist (or it lands as an approval waitpoint under guided autonomy). |
| `test-crew-links.sh` | a **crew link** is real: the graph lists no links to deleted crews, a lead's sidecar reports the crews it can reach, delegation **across** a live link lands in the other crew, and the same delegation is **refused** once the link is removed (then restored). |
| `test-notifications.sh` | a routine **run completes** (exit code + records status); the **completion event** is observable via `routine watch --once`; a **failed run** surfaces a `failed_run` inbox item (best-effort). |
| `test-inbox.sh` | the **inbox as a decision surface**: fires `approval-gate-demo` to mint a REAL waitpoint, asserts the row carries the fields the UI renders (`timeout_at` for the countdown, `pipeline_run_id` for run progress, `blocking`, `target_role`), approves it through the **source** endpoint and proves the row is `resolved` server-side — the cascade the UI depends on and must never duplicate. Also dismisses a message and checks `resolved_action` + `resolved_by_user_id` are recorded, and that the kind vocabulary the UI codes against is the one in the data. |
| `test-notifications-shoutrrr.sh` | **#1412 category preference matrix**: a fake local webhook receiver gets **exactly ONE** delivery from a personal channel with `chat.replies=immediate`, and **ZERO** from a channel muted via the `*` category, for the SAME triggering event (an `ask` reply); the delivery-log entry shows `status=sent` (admin-only, best-effort). Opt-in: `WITH_NOTIFICATIONS_SHOUTRRR=1 ./run-all.sh` — see the script header for why it uses `chat.replies` rather than `runs.failed`, and the network-reachability note when `SERVER` is a remote devN. |
| `test-orchestration.sh` | the seeded **cron schedules** are present + enabled; an **agentless** routine runs at **token-zero cost**; a **HITL approval gate** pauses → is approved via CLI → resumes; **cross-tier** eval returns structured results (`EVAL=0` to skip the token-heavy block). |
| `test-credentials.sh` | human **create + assign**; the API never returns the plaintext **value**; an agent **escalates** for a credential and a human grants it; agent **self-service** creation attributed `actor_type=agent` (probe — SKIPs if not wired); revocation removes the `/secrets` file; Keeper ON **withholds** SECRET files; **credential leases** — capture a lease, wait past its TTL, assert it is refused (not merely labelled) and that `lease_source` records the provenance; the workspace **auto-lease** toggle round-trips and rejects a sub-minute TTL. Takes ~90s longer than the other suites because it waits out a real lease TTL. |
| `test-datastore-redis-auth.sh` | **datastores are always password-protected** (Redis case): applying a stock `redis:*` sidecar with **no auth declared** mints an `AUTO_MANAGED` **REDIS_PASSWORD** credential (value never returned), boots the server with `--requirepass`, and proves **auth — not the crew bridge — is the gate**: an unauthenticated `PING` over the (reachable) bridge is refused **NOAUTH**, while `PING` with `$REDIS_PASSWORD` returns **PONG**. **Requires Docker + a provisioned crew — dev-VM only, not in `run-all.sh`.** SKIPs the redis-cli-in-agent checks if the runtime lacks `redis-cli`; host-side `docker exec` confirmation is documented, not run (CLI-only policy). |
| `test-keeper.sh` | Keeper watchdog **governance** via the real `crewship keeper` CLI: `status` reports server + workspace state; `enable`/`disable` **flip the toggle** (round-trips); `threshold N` sets the DENY-notify risk and **rejects out-of-range**; `contact <email>` **targets a named OWNER/ADMIN** and **rejects a non-member**. Control-plane only — a full credential ESCALATE needs the gatekeeper LLM, out of scope here. SKIPs if the installed CLI has no `keeper` command. |
| `test-keeper-config.sh` | the **instance judge configuration** via the real `crewship keeper config` CLI: `get` reports the effective judge **with provenance** (`instance` / `env` / `default`); `set` is a **partial update** — an unmentioned field keeps both its value and its provenance; an override reads back as `source=instance`, so the change is genuinely in force **without a restart**; clearing a field **returns it to the server's `KEEPER_*` value**; enabling Keeper with **no judge is refused** (fail-closed — it would DENY every credential request); `reset` drops every override. Captures the starting configuration and restores it **as it was found**, so an inherited field goes back to inheriting rather than to a pinned copy. SKIPs if the installed CLI has no `keeper config` command, if `jq` is missing, or if the caller is not OWNER/ADMIN. Whether the configured judge actually answers is `keeper judge test`, a separate script — it needs a real model on the target. |
| `test-determinism.sh` | a pure-transform recipe yields **byte-identical** `@json` output across N runs; prints a latency/cost **baseline**. |
| `test-realworld-github.sh` | an agent uses the in-container **`gh`** CLI against a public repo (read-only); SKIPs if `gh` isn't authenticated. |
| `test-gitlink-realworld.sh` | **the link-first Git integration against a REAL forge** — the one thing `internal/gitlink`'s fixtures cannot do. Attaches real, public, long-lived objects to a throwaway issue via `crewship issue link` and asserts the **parse**, not the 200: a **MERGED** PR/MR reads MERGED (on GitHub only a non-null `merged_at` distinguishes it — `state` says `closed`), a **closed-not-merged** object from the same repo reads CLOSED (the GitLab default has a **null `closed_at`**, so CLOSED can only have come from `state`), and **author / source branch / target branch** carry through — i.e. `user.login`+`head.ref`+`base.ref` and `author.username`+`source_branch`+`target_branch`, the exact keys an upstream rename would break. Then the two failures fixtures cannot honestly simulate: a **repository that does not exist** (real 404) and a **credential the provider rejects** (real 401/403), both asserted on Crewship's own **RFC 7807 `type` URI + `code`** rather than the status, because three different remedies share 502. Also: `relink` re-reads the provider and clears `last_sync_error`. **Read-only against the forge**; everything it writes is one Crewship issue + up to two credentials per provider, nonce-named and removed on exit (including on Ctrl-C). Enabled per provider by `GITLINK_GITHUB_TOKEN` / `GITLINK_GITLAB_TOKEN`; with neither it SKIPs before contacting anything. |
| `test-secretless-github.sh` | **the secretless claim, end to end** (PRD-CREDENTIALS-V2 §4.3, T-H1…T-H9): a credential assigned to a **crew** reaches the crew's agent (proved by a synthetic **canary** whose fingerprint the agent reports back) and makes **`gh auth status`** work with **no step inside the container**; `gh auth login` **never ran** (no `hosts.yml`, nothing in shell history); **zero-disk** — the canary value appears in **no file** under `$HOME`, `/home/agent`, `/secrets`, `/tmp`; private-repo **clone + commit + push** over HTTPS leaves **no `.git-credentials`**; **`docker login ghcr.io`** with the same PAT; **git over SSH** from an `SSH_KEY` credential; **revocation** — after `credential delete` the agent's next `gh` exec **fails** (the proof no dried copy survived); **cross-crew isolation** — crew B's agent has neither the value nor a file. Filesystem facts come from a token-zero `script` routine running in the crew container (the `test-redteam-insider.sh` pattern), never from the model. **Every section SKIPs on its own missing input** — with none of `SEED_GITHUB_TOKEN` / `SEED_GITHUB_TOKEN_CLASSIC` / `SEED_GITHUB_SSH_KEY` / `SEED_GITHUB_TEST_REPO` you still get the fanout, zero-disk and isolation legs. Opt-in: `WITH_SECRETLESS=1 ./run-all.sh`. **Dev slots only.** |
| `test-orphan-token-reap.sh` | the **#1385 stable-master** remediation lever: `admin reap-orphan-containers` is wired (API↔CLI parity), and a **dry-run sweep against the running server finds ZERO orphans** — proving the fail-safe classifier never false-positives a healthy container — and is **non-mutating + idempotent**. Self-**SKIPs** when the provider isn't docker (503). The restart-invalidation property itself is locked by the Go unit tests. |
| `test-keeper-ingress-fence.sh` | the **internal keeper HTTP surface** rejects every request with no token / forged / zero / spoofed-XFF (fence holds), across a **method matrix** (GET/PUT/DELETE/PATCH/OPTIONS), a **malformed-token fuzz** (empty, 8 KB, CRLF, SQL/shell/path-ish), an **oversized body**, and **other `/internal/*`** routes; asserts **no info leak** in rejections and that the **public API still needs auth**; runs a **constant-time timing probe**; flags whether the **network-origin gate** is defeated behind the proxy (off-host → 403 not 404 ⇒ static `X-Internal-Token` is the sole guard). *The one suite that uses raw `curl` — the internal channel has no CLI by design.* |
| `test-keeper-toctou.sh` | a decision reflects **injection-time** state, not approval-time: `rotate --grace-seconds 0` scrubs the stale value now, **grace-window rotate + rotation-cancel** scrubs early, a **concurrent rotate race** leaves the credential coherent + `ACTIVE`, `unassign`/`reassign` toggles the binding the keeper requires, **delete-while-assigned** revokes cleanly, **peer value** is never exposed; **SKIPs** the container-only deferred race (T2) and the token-only double-execute (T10). |
| `test-keeper-audit-integrity.sh` | decisions leave a **durable, monotonic trace**: lifecycle events grow the `credential audit` timeline, **REVOKE** on delete, a granted escalation resolves off `PENDING` (**approve** path) and a **denied** one is recorded (deny path), `system keeper` exposes scrubber + model; the journal **hash-chain** verifies clean and detects an out-of-band row mutation; keeper decisions are **append-only** — `keeper history` shows every transition, 1-based and gap-free, starting at `PENDING`, tail matching the current decision, each with an actor; an **authorised priority edit does not break the chain** (pin → verify → revert → verify). **SKIPs** the load-only fail-silent drop (T6) and the token-only returned-vs-persisted mismatch (T7); the DB-trigger and raw-flip legs print `sqlite3` commands to run on dev2 with `CREWSHIP_DB` set. |
| `test-keeper-load.sh` | **correctness under load** (the real "perf" tests): read-path **p50/p95/p99** latency baseline at `CONC` concurrency, server stays **healthy through a write burst** (no 5xx / health flap), the **rate-limiter** yields 200/429 never 5xx, **pending-count stays consistent** under concurrent reads, keeper **status reachable under load**; **SKIPs** inbox-flooding advisory-loss (T8) and evaluator-saturation fail-closed (T9). Tunables: `CONC`, `SAMPLES`, `BURST`. |

| `test-attack-surface.sh` | **Tier A perimeter** — drives the server as an *external* attacker: protected + admin routes reject no-auth and a garbage token, every `/api/v1/internal/*` route is **404 from the edge** (no token / user JWT / guessed static token), a spoofed `X-Forwarded-For: 127.0.0.1` does **not** fake a private origin (#1020), a **wrong method** on a real internal route is 404 rather than a route-confirming 405 (#1501), and a non-member workspace answers 403. Read-only — it creates nothing. Tier B (insider) attacks are listed as SKIPs carrying the exact agent-context command, each tagged with its issue and whether that issue is fixed, partial, or open. Cross-workspace checks SKIP unless `CREWSHIP_ATTACK_OTHER_WS` names a workspace that **exists** and the token holder is not a member of — a guessed id answers 404 and would prove nothing. *Uses raw `curl` by necessity: its job is to send requests the CLI would never construct.* Opt-in: `WITH_ATTACK_SURFACE=1 ./run-all.sh`. |
| `test-redteam-insider.sh` | **Tier B insider** — the self-attacking routine. Delivers `redteam-probe.sh` into the crew's shared dir, saves a one-step `script` routine, runs it **inside the crew container**, and asserts containment from the report: the internal API does not accept an unauthenticated agent-context request, no cleartext files under `/secrets` (#1364 regression check), raw non-proxied egress dies at L3 (**xfail: #1368**), and a restricted crew cannot reach a non-allowlisted host (found by this suite as #1473, fixed — the xfail branch now only fires against a server older than the fix). The routine is soft-deleted on exit; the probe file stays (`crew files` has no delete verb) and is inert + overwritten each run. **Dev slots only — never point it at prod.** Opt-in: `WITH_REDTEAM_INSIDER=1 ./run-all.sh`. |

> **Adversarial suites** (`test-attack-surface.sh`, `test-redteam-insider.sh`)
> are opt-in and deliberately outside the default set — the insider one mutates
> a shared dev slot and carries `xfail` assertions for open issues that must stay
> visible rather than become noise in an unrelated run. Scenario catalog, safety
> rails, and how to turn each Tier B attack into a scheduled red-team routine:
> `ATTACK-SCENARIOS.md`.

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
| 4a | `crewship agent credentials <agent>` **does not show crew-scoped credentials** — `GET /api/v1/agents/{id}/credentials` still selects from `agent_credentials` alone, the one credential reader the crew-fanout work did not move onto `loadDeliveredCredentials`. The agent demonstrably receives the credential; the only CLI surface that answers "what does this agent get?" says it does not. | **OPEN** — `test-secretless-github.sh` §1 records it as a SKIP labelled `FINDING:` rather than failing. | `internal/api/agent_credentials.go` |
| 4b | A **crew-scoped** credential is delivered under its **NAME**, not its `--env-var-name` (`SELECT c.name AS env_var_name` on the crew half of the delivery query), so `--env-var-name` is silently ignored for `--crews` rows. | **BY DESIGN today** — PRD-CREDENTIALS-V2 P3 (decouple name from env var) is what changes it. The suite names credentials accordingly and says so inline. | `internal/api/credential_delivery.go` |
| 4c | **No CLI path ingests a multi-line secret.** `credential create --value-stdin` reads a single line (`bufio.Scanner` + one `Scan`), there is no `--value-file`, and manifests carry credential *slots*, not values — so an `SSH_KEY` PEM can only be passed via `--value`, which puts it in the process argv. | **OPEN** — `test-secretless-github.sh` §6 SKIPs the git-over-SSH leg with that reason instead of leaking the key; the leg goes live unchanged the moment the CLI can take a multi-line value. | `cmd/crewship/cmd_credential_mutate.go` |
| 4d | **No CLI can exec a command in a crew container.** The PRD's §4.4 CI gate is written as `docker exec … + grep`, which the CLI cannot express. The harness works around it with a token-zero `script` routine step (deterministic, in-container, CLI-driven); anything that must run *as the agent* (i.e. with credential env) still goes through `crewship ask`, so it is model-mediated. | **OPEN** (workaround in place) | `scripts/test-harness/test-secretless-github.sh` |
| 4 | Synchronous `routine run` of an **approval gate** surfaces **no pollable waitpoint** | **FIXED** — the run parks as WAITING with a waitpoint token; the original symptom was a CLI gap (`waitpoints list --format json` printed the human table so jq never saw the token). The suite now FAILS (not skips) if no waitpoint appears. | `cmd/crewship/cmd_routine_waitpoints.go` |
| 5 | **`account_label` is write-only from the CLI.** `credential create --account-label` exists (it is how a git link is matched to a self-hosted forge, #1758), but neither `credential list --format json` nor `credential get --format json` returns the field — so no operator or agent can answer "which credential will this `github.com` link resolve to?" from the CLI. Found while writing `test-gitlink-realworld.sh`, which therefore cannot verify its own precondition. | **OPEN** — the suite states the precondition instead, and its good-path failure message names it as the likely cause. | `internal/api/credentials.go` (the API response carries it) ↔ `cmd/crewship/cmd_credential.go` (the list/get structs drop it) |
