# Live API contract gate

This directory contains the deterministic live contract gate for Crewship. It
loads the running instance's generated OpenAPI route catalog from
`/openapi.json` and runs Schemathesis against that instance.

The required PR job builds and starts Crewship from the same commit as the
tests, seeds a disposable database, mints a short-lived CLI token, then runs
the `auth` and read-only `positive` phases. It uploads the sanitized summary,
OpenAPI snapshot, JUnit report, and Schemathesis log as CI evidence. The script
itself remains composable for local or nightly use: it does not start Crewship
or bootstrap an account.

## Prerequisites

- An already-running Crewship instance. For a development checkout,
  `./dev.sh start` serves the Go API at `http://localhost:8080` (the port is
  offset for numbered clone instances).
- Python 3.10+ and `uv`, or another way to run the pinned Schemathesis
  dependency from `requirements.txt`.
- A token from `crewship init` / `crewship login`, and a workspace the token can
  access. Crewship accepts it as `Authorization: Bearer ...`; workspace-scoped
  routes accept `X-Workspace-ID` (a slug is resolved by the server).

The generated document is a route catalog: paths, methods, and path
parameters are exact. Audited domains also provide concrete request and
response schemas; routes that have not been audited yet retain an explicit
generic-object fallback. Treat failures as a useful live smoke/route contract
signal, not as complete payload validation for every route.

## Quick start

```bash
export CREWSHIP_BASE_URL=http://localhost:8080
export CREWSHIP_TOKEN='paste-a-short-lived-cli-token-here'
export CREWSHIP_WORKSPACE=demo       # slug or workspace ID

cd scripts/api-contract
uv run --with-requirements requirements.txt ./run.sh positive
```

The runner also accepts `BASE_URL`, `API_TOKEN`, and `WORKSPACE_ID` as aliases.
The token is read from the environment only; it is never echoed by the runner.

To use an existing virtualenv, install `requirements.txt` and run
`./run.sh positive` directly. `schemathesis.toml` is kept in this directory so
the settings are explicit and do not affect any repository-wide test command.

## Phases

```bash
./run.sh positive   # authenticated coverage checks against safe, read-only methods
./run.sh auth       # unauthenticated and invalid-token GET checks
./run.sh stateful   # Schemathesis stateful/link phase, still read-only
```

- **Positive** runs Schemathesis' `coverage` phase with the configured bearer
  token and workspace header. This generates requests even when the current
  route catalog has no explicit examples. It excludes POST, PUT, PATCH, and
  DELETE.
- **Negative/auth** uses only harmless GET requests to assert that the public
  OpenAPI document is reachable without auth, protected API access rejects no
  auth and a garbage bearer token, and a supplied valid token can list
  workspaces.
- **Stateful** enables Schemathesis' `stateful` phase. Crewship's generated
  schema currently has no hand-authored response links, so this may report
  missing links; it is included as a probe for future links and dependency
  metadata. Mutating methods remain excluded.

The default runs are intentionally bounded (`generation.max-examples = 10`,
one worker, a 10-second request timeout, and a 120-request/minute rate limit).
The rate limit is the one bound that lives in `run.sh` rather than
`schemathesis.toml`, because it is the one that has to differ between targets —
see [Pacing and deadlines](#pacing-and-deadlines).
The runner prints one concise JSON summary line; it includes the selected and
excluded operation counts and classifies failures as `schema` or `runtime`.
When `API_CONTRACT_ARTIFACT_DIR` is set, it also preserves the summary,
OpenAPI snapshot, JUnit report, and sanitized log in that directory. A schema
or response declaration change therefore fails the PR and leaves the exact
operation name in the JUnit/summary evidence.
The schema is fetched into a temporary file only for validation/counting, then
removed. Hypothesis examples and reports are temporary too. No token or raw
response body is included in the summary.

The generated catalog is broader than this safe probe. The runner always
excludes `/api/auth/**` (NextAuth UI/session endpoints) and these non-JSON
operations: backup/file downloads, avatars, pipeline and memory exports,
memory-version content, and the journal stream. These are stable path
exclusions because they return binary or streaming data and are not suitable
for this JSON-focused probe. The exclusions are reported separately from the method deny-list,
so a lower selected count is visible rather than silently looking like route
coverage.

`selected` is the count of operations that survive all three exclusions —
the complement, not the union. Note the buckets **overlap** (a non-JSON
download is also a GET; an `/api/auth` route can also be mutating), so
`catalog - methods - auth_ui - non_json` does not reconstruct it. Until
#1815 the summary reported the union under that name: 536 in the catalog,
305 excluded, printed as `"selected": 305` while Schemathesis reported 231
for the same invocation. It is the complement now, and the shell test pins
it against a fixture whose answer is known by hand.

## Pacing and deadlines

Two environment variables tune how the runner spends time. Both default to
the conservative value, so a run against a live instance behaves the same as
before they existed.

| Variable | Default | What it does |
|---|---|---|
| `API_CONTRACT_RATE_LIMIT` | `120/m` | Client-side pacing passed to Schemathesis. `off` (or `none`) removes it. |
| `API_CONTRACT_TIMEOUT` | unset | Seconds before the runner stops Schemathesis itself and exits with a named `runtime` failure. Needs coreutils `timeout` on `PATH`; if it is missing the runner refuses to start rather than silently ignoring the deadline. |
| `API_CONTRACT_ADVISORY` | unset | Report findings without failing the caller. Findings only — see [Advisory mode](#advisory-mode). |

`120/m` matches the server's shipped `http.api_per_min`, so a run against a
real instance cannot out-run its limiter — which matters, because a 429 is
reported as a contract failure for an operation that is in fact fine.

`off` is correct only against an instance that has no limiter of its own. The
per-PR job's ephemeral server boots with `CREWSHIP_RATELIMIT_DISABLED`
precisely so the gate can use it: 231 selected operations at
`--max-examples 10` generate ~8000 test cases, and Schemathesis paces what it
**generates**, not what it sends — a ~66-minute floor at 120/m, inside a
30-minute job that also builds, boots, seeds, and runs five harness suites
(#1813). Measured in CI on that job, same operations, same `--max-examples`:
the whole step — `auth` plus `positive` — takes **15 seconds** unthrottled,
13.3s of it inside Schemathesis. Throttled, the same phase has never once
completed: two runs were killed at the 30-minute job cap with the step 28
minutes in. The pairing (`off` only where the server's limiter is off)
and the deadline ordering are asserted by
`scripts/api-contract-gate-test.sh`, which runs in CI's `shell` job.

Set the deadline when something else owns a shorter budget than the run does —
CI, mainly. Without it a slow phase is reaped by the job's `timeout-minutes`,
which GitHub reports as `cancelled` (indistinguishable from a human pressing
stop) and which kills the runner before its `EXIT` trap can leave a verdict
behind. With it, the runner loses the race to itself and still writes the
summary.

## Advisory mode

`API_CONTRACT_ADVISORY=1` makes the `positive` phase report its findings
without failing the caller. The per-PR job sets it today, and the reason is
written next to it in `.github/workflows/ci.yml`: when the phase first
became able to finish (#1813) it reported 265 findings across 227 graded
operations, all pre-existing, most already tracked as #1583 and #1489. They
were invisible only because the phase had never once completed, so blocking
on them would fail PRs that neither caused them nor can fix them. **Remove
the flag when #1815 closes.**

What advisory does *not* mean:

- **It is not `continue-on-error`.** The exemption covers findings only.
  Exit 1 **and** a JUnit report showing operations were graded is the only
  shape that passes. A schema that will not load (exit 2), a blown
  deadline, a crash, an unreachable target, and every early `die` still
  fail the caller — those mean the gate did not run, which is not a debt,
  it is a broken gate. `continue-on-error` on the step would excuse all of
  them; `scripts/api-contract-gate-test.sh` fails the build if it appears.
- **It does not reduce the evidence.** The summary, OpenAPI snapshot,
  JUnit report and sanitized log are still written and uploaded.
- **It does not launder the verdict.** The summary still says
  `"status":"failed"`, plus `"advisory":true` and the finding count, so a
  reader of the artifact cannot mistake an excused run for a clean one.
- **It is not silent.** The runner prints the graded/finding counts to
  stderr, and appends a line to `$GITHUB_STEP_SUMMARY` when running in
  Actions, so the number is read every run rather than filed away.
- **It never touches the `auth` phase**, whose checks are reachability and
  authorization, not contract findings.

Failure triage starts with the summary: `schema` means the OpenAPI document,
Schemathesis configuration, or declared response schema could not be used;
`runtime` means the target could not be reached or an executed request failed
(for example a 5xx, status mismatch, or timeout). A schema-shaped failure from
an excluded binary/stream route is therefore not misreported as a product
regression in the JSON API.

Example summary:

```json
{"advisory":false,"failure_class":"none","findings":0,"limits":{"max_examples_per_operation":10,"request_timeout_seconds":10,"workers":1},"operations":{"catalog":412,"excluded":{"auth_ui":8,"methods":253,"non_json":11},"graded":140,"selected":140},"phase":"positive","status":"passed","tool":"crewship-api-contract"}
```

`selected` is what the runner intended to probe (counted from the catalog);
`graded` is what Schemathesis actually reached a verdict on (counted from
its JUnit report). They should agree — a gap between them means the run
stopped early, and that is the difference advisory mode refuses to excuse.

## Mutation safety

There is no mutation-enabled command in the PR gate. The generated schema
contains many POST/PATCH/DELETE operations, but the positive and stateful
commands explicitly exclude all four mutating method families before making
requests. Do not remove those exclusions casually: a live instance may hold
real crews, credentials, agents, or integrations.

The auth phase is also GET-only. Run it against a disposable local workspace
if you are investigating authorization behavior. Do not use `dev.sh nuke` or
any reset/bootstrap command as part of this harness.

## Troubleshooting

- `404` or HTML for `/openapi.json`: the target is probably the Next.js port
  (`:3001`) or an older build without the API spec route; use the Go API port.
- `401` in positive/stateful phases: refresh the CLI token and verify that it
  was issued for this server.
- `400 workspace_id is required` or `403`: check `CREWSHIP_WORKSPACE`; the
  runner sends it as `X-Workspace-ID`.
- `Missing Open API links` in `stateful`: this is expected with the current
  route-catalog-only schema and is not a mutation attempt.

The upstream CLI reference and configuration format are documented at
<https://schemathesis.readthedocs.io/en/stable/reference/cli/> and
<https://schemathesis.readthedocs.io/en/stable/configuration/>.
