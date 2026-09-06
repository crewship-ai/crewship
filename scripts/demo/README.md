# Demo use cases

One script per use case, each runnable on its own against a seeded dev
server, narrated for an audience and verified by assertions. The second
version of the demo scripts: `../walkthrough.sh` was one `set -e` checklist
that stopped at its first failing line; this is twelve units you can test
one at a time.

```bash
export CREWSHIP_PROFILE=dev2          # or CREWSHIP_SERVER / SERVER
export CREWSHIP=/tmp/crewship-2-dev   # the binary dev.sh built (default: ./crewship at the repo root)

scripts/demo/run.sh                   # list
scripts/demo/run.sh memory-recall     # one, by slug …
scripts/demo/run.sh 6 7               # … or by number, several in order
scripts/demo/run.sh --all             # all twelve, one summary table
scripts/demo/run.sh --all --needs none   # only the token-zero ones
```

| # | Use case | Needs | Shows |
|---|---|---|---|
| 1 | `memory-recall` | model | a fact told in one session, recalled in a fresh one, found by `memory search` |
| 2 | `delegation` | model | the engineering lead hands a task to a peer and reports the peer's answer |
| 3 | `ephemeral-hire` | — | `crewship hire`, the guided crew's approval gate, `hire approve` |
| 4 | `credential-escalation` | model | an agent asks for a credential; a human answers with `escalation supply` |
| 5 | `approval-gate` | model | a routine parks on `wait(approval)`; `routine waitpoints approve` resumes it |
| 6 | `routine-notification` | — | a token-zero routine completes and its notification carries the run id |
| 7 | `schedule-wake-gate` | — | a wake-gated schedule holds on a real scheduler tick |
| 8 | `eval-tiers` | model | the same recipe on the fast and the smart tier |
| 9 | `github-injection` | github, model | a bound token reaches the container; `gh auth status` inside it |
| 10 | `pack-ci-watch` | github | the nightly CI watch pack through `seed verify --pack` |
| 11 | `pack-docs-drift` | github | the docs-drift pack through `seed verify --pack` |
| 12 | `pack-site-replica` | — | the site-replica pack through `seed verify --pack` |

**Needs** is what the use case cannot do without: `model` is an ACTIVE
model-provider credential in the workspace, `github` is `SEED_GITHUB_TOKEN`
at seed time (or a bound GITHUB credential). A use case whose need is
missing exits 3 and the runner prints **SKIP** with the reason — never a
green row for something that did not run.

## Contract

Each `uc-NN-<slug>.sh`:

- sources `lib.sh`, which sits on top of `../test-harness/lib.sh` (`cs`,
  `ask_agent`, `assert_*`, `poll_until`, `nonce`, `preflight`, `finish`);
- carries a four-line header the runner lists from (`uc`, `title`, `needs`,
  `minutes`);
- exits **0** PASS, **1** FAIL (the failing assertions are listed in its
  summary), **3** SKIP (its last line names the reason);
- cleans up what it created (`demo_cleanup`), so it can be run again;
- has no `set -e`: a failed step is a red line, not an abort — the audience
  still sees the steps after it.

`lib.sh` adds the demo layer: `demo_step`, `demo_say`, `demo_show` (runs a
command, shows its output, records pass/fail), `demo_ui` (where to look in
the web UI), `demo_need`/`demo_skip_all`, `run_routine`/`run_receipt`
(`routine run` with the receipt parsed into `RUN_ID`/`RUN_STATUS`),
`run_status`, `inbox_has`.

## Knobs

| Variable | Default | Meaning |
|---|---|---|
| `DEMO_UC_TIMEOUT` | 1800 | seconds per use case before the runner kills it |
| `RUN_WAIT` | 15m | `routine run --wait` bound inside a use case |
| `ASK_TIMEOUT` | 180 | seconds per agent reply |
| `DEMO_WEB_URL` | `$SERVER` | the UI the audience has open, for the 👀 lines |
| `WITH_REPORT` | 0 | packs: also run the agent report routine and reconcile it |

## What is tested without a server

`scripts/demo-test.sh` (CI, `shell` job) ShellChecks every script here,
checks the headers, and drives `run.sh` against stub use cases: exit 3 is
SKIP, every failure counts and not only the last one's, a failure does not
stop `--all`, `--needs` filters, a hung use case is killed and reported.

## Known limits

- `crewship routine schedules now` force-fires the *target* routine and
  bypasses the wake gate, so use case 7 waits for a real tick (≤ 90 s).
- Use cases 2 and 4 depend on an agent doing what it was asked; each retries
  once and then SKIPs with the reason rather than reporting the model's
  phrasing as a product failure.
- Use case 12 reports the pack as verified with `NOT BUILT` until somebody
  asks Alex to copy the site; that is the pack's own semantics.
