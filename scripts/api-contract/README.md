# Live API contract prototype

This directory is a deliberately small, opt-in live contract-test harness for
Crewship. It loads the running instance's generated OpenAPI route catalog from
`/openapi.json` and runs Schemathesis against that instance.

It does not start Crewship, bootstrap an account, select a workspace, or write
reports. Start and seed a local instance separately, then provide a CLI token
and a workspace slug or ID.

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
The runner prints one concise JSON summary line; it includes the selected and
excluded operation counts and classifies failures as `schema` or `runtime`.
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

Failure triage starts with the summary: `schema` means the OpenAPI document,
Schemathesis configuration, or declared response schema could not be used;
`runtime` means the target could not be reached or an executed request failed
(for example a 5xx, status mismatch, or timeout). A schema-shaped failure from
an excluded binary/stream route is therefore not misreported as a product
regression in the JSON API.

Example summary:

```json
{"failure_class":"none","operations":{"catalog":412,"excluded":{"auth_ui":8,"methods":253,"non_json":11},"selected":140},"phase":"positive","status":"passed","tool":"crewship-api-contract"}
```

## Mutation safety

There is no mutation-enabled command in this prototype. The generated schema
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
