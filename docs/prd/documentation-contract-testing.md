# Design — documentation contract-testing system

**Status:** proposed  
**Owner:** Crewship maintainers  
**Created:** 2026-08-04  
**Companion:** `docs/prd/PRD-RELEASE-1-0-QUALITY-AUDIT.md`

## Decision

Build one contract-testing system with three deliberately different layers:

1. **Schemathesis live API layer** exercises the running server from the
   generated OpenAPI document, using authenticated, workspace-scoped fixtures.
2. **Deterministic documentation layer** validates MDX examples, OpenAPI
   freshness/shape, CLI inventory, and documented API/CLI mappings without a
   server or network.
3. **CLI runtime smoke layer** runs the built `crewship` binary against a
   disposable live server and proves the golden user workflow end to end.

No layer substitutes for another. The deterministic layer catches drift cheaply;
Schemathesis catches HTTP/schema and authorization faults; runtime smoke catches
packaging, transport, authentication, and CLI wiring faults.

## Contract and ownership

The implementation is authoritative for route registration, response behavior,
and Cobra command behavior. `internal/api/openapi.gen.json` is the machine
readable public API boundary and must remain generated from registered routes.
Public documentation is the user-facing contract: every public operation and
command is mapped to an API-reference or CLI page, and runnable examples are
either deterministic or explicitly marked live.

Each failure names the contract owner and source location:

| Failure | Primary owner | Typical response |
| --- | --- | --- |
| Route absent/stale in OpenAPI | API/router | regenerate or correct route visibility |
| Schema/status/auth mismatch | API handler | fix handler/spec or update deliberate contract |
| MDX or navigation drift | Docs | repair page/example/link |
| CLI request/output mismatch | CLI + API owners | update the side that changed intentionally |
| Live runtime/package failure | Release/runtime | inspect binary, server, credentials, or environment |

## Layer A: Schemathesis live API

Run Schemathesis against `GET /openapi.json` served by a built Crewship
instance. The test harness owns the base URL and obtains a short-lived test
token; it does not use a developer token or a production database.

The generated spec must be checked before the live run. Schemathesis should:

- cover every public operation, with a stable seed and bounded examples/time;
- send the test auth header and workspace context required by the route;
- assert declared status codes, content types, and response schemas;
- exercise malformed inputs, missing auth, invalid workspace/resource IDs, and
  cross-workspace access where the operation permits those cases;
- use stateful sequences only for an explicitly allowlisted resource family;
- record the operation, case seed, request metadata, response status, and
  correlation/run ID on failure.

The OpenAPI document currently describes schemas minimally in some areas. Until
schemas are strengthened, the live gate must still enforce method/path/status,
auth, content type, and documented invariants, while labeling weak-schema
passes as coverage debt rather than false confidence.

Live API is PR-blocking for the deterministic seed and critical route set. Full
bounded fuzzing, stateful sequences, and broader negative-case matrices run
nightly and before release promotion.

## Layer B: deterministic MDX/OpenAPI/CLI contracts

This layer is hermetic, fast, and reproducible. It must not start a server,
contact an external provider, invoke an LLM, or depend on local environment
variables.

It validates:

- generated OpenAPI freshness and valid JSON/OpenAPI structure;
- one inventory row per public API operation and CLI command, with exactly one
  current documentation target or an explicit internal/unsupported status;
- `docs/docs.json` navigation targets and local links;
- MDX parsing/buildability and fenced examples with the repository's supported
  syntax;
- documented HTTP methods, paths, CLI names, flags, status labels, and
  terminology against generated inventories;
- deterministic request/response examples against checked-in fixtures, with
  secrets and machine-specific paths rejected;
- CLI help snapshots or parsed command metadata for public commands, without
  asserting unstable formatting or timestamps.

Examples that need a live server are tagged as runtime examples and referenced
by the smoke matrix; they are not silently treated as deterministic tests.
The existing docs inventory is the initial index, not proof that an example is
behaviorally correct.

## Layer C: CLI runtime smoke

Build the CLI and server from the same commit, start an isolated local server,
and run a small golden workflow through the real binary. The minimum workflow
is:

1. authenticate with a generated test credential;
2. inspect the current workspace and list a seeded crew/agent;
3. create or apply one disposable resource;
4. read it back in table and JSON forms;
5. exercise one documented mutation and verify its resulting API state;
6. remove the disposable resource and assert cleanup;
7. confirm a second workspace cannot see the resource.

The matrix should cover representative commands, output formats, non-zero
errors, and one auth failure. It must remain small enough for every release
candidate; provider-backed agent execution and paid upstream CLIs remain
nightly/manual smoke tests, using the existing fixture-replay strategy for
per-PR parser coverage.

## Auth and workspace fixtures

Fixtures are created by a single harness and expose named identities, not raw
secrets in test code:

- `admin-A`: authenticated owner of workspace A;
- `member-A`: least-privilege member of workspace A;
- `admin-B`: owner of unrelated workspace B;
- `anonymous`: no credentials;
- seeded stable IDs for one crew, agent, and harmless reference objects per
  workspace.

The harness creates an ephemeral database/storage root, signs short-lived
credentials from CI-only random material, and exports a redacted fixture
manifest. Every test receives the base URL, token, workspace ID, and cleanup
scope explicitly. Tests must not discover or reuse a developer config, global
`CREWSHIP_*` setting, home directory, or shared SQLite file.

## Safe mutation policy

The default contract case is read-only. A mutating case is permitted only when
all of the following hold:

- it targets an ephemeral fixture workspace and an ID created by that test;
- the operation is on an explicit allowlist with a documented cleanup action;
- the request is idempotent or has a bounded retry and unique case prefix;
- cleanup runs in a deferred/finally path and the harness verifies absence;
- cross-workspace, credential, billing, destructive, provider, and host/runtime
  operations are deny-by-default;
- no real notification, webhook, model call, credential, backup destination,
  filesystem path, container, or external network target can be reached.

Destructive endpoints are tested through authorization and validation failures,
or against disposable data in a separately named nightly job. A failed cleanup
is itself a hard failure and must preserve enough metadata to locate the
ephemeral run without printing tokens or payload secrets.

## CI and release gates

| Gate | Pull request | Nightly | Release candidate/tag |
| --- | --- | --- | --- |
| Deterministic MDX/OpenAPI/CLI | required | required | required |
| Schemathesis critical deterministic cases | required | required | required |
| Schemathesis bounded full/stateful set | optional, report | required | required |
| CLI runtime golden smoke | required when CLI/API/runtime changes | required | required on built artifact |
| Provider-backed/paid runtime smoke | no | manual/nightly allowlist | manual sign-off if advertised |
| Fixture cleanup and secret scan | required | required | required |

The release gate runs against the exact packaged binary/container, not only a
development server. A release cannot promote on a green job that was skipped,
rate-limited, or unable to start its fixture; the result must distinguish
`passed`, `failed`, `blocked`, and `not-run`. Docs-only changes may skip live
layers only when the changed paths do not touch API/CLI examples or contracts;
the deterministic layer still runs.

### What is wired today

The table above is the target state. As of the release-1.0 contract audit, the
deterministic layer is the part that is actually enforced:

| Gate | Wired as | Fails the PR |
| --- | --- | --- |
| OpenAPI spec freshness | `Go Lint` → *OpenAPI spec is up to date* | yes |
| Documentation completeness | `Go Lint` → *API and CLI documentation is complete* (`go run ./scripts/docs-inventory -strict`) | yes |
| Navigation targets and inline links | `Documentation surface` → *Verify the contextual surface and navigation* (`go run ./scripts/docs-surface-check`) | yes |
| Schemathesis live layers | `ci.yml` → *Run deterministic API contract gate*; the same ephemeral seeded server/build as the PR harness | yes |
| CLI runtime golden smoke | `ci.yml` → Harness PR subset + CLI command breadth smoke | yes |

`-strict` enforces six invariants and names the offending rows rather than only
counting them: operations with no documentation, operations missing structural
contract evidence (auth/request/response/statuses), generic response schemas,
generic JSON request schemas, CLI commands with no page, and CLI commands with
undocumented flags. `make docs-inventory` remains the non-failing form for
regenerating the reports locally; `make docs-inventory:strict` is what CI runs.

`docs-surface-check` runs three hermetic passes over the tree: the contextual
surface and description quality, the page ids `docs/docs.json` declares, and —
since #1774 — every internal link written *inside* a page body, in both the
Markdown `](/guides/routines)` and the JSX `href="/guides/routines"` form. Links
inside fenced blocks are transcripts, not navigation, and are skipped. A dead
link names the page and the target it points at, so the fix does not start with
a search.

**Fragments are resolved away, not verified.** `/guides/routines#cross-run-state`
is checked as `/guides/routines`; whether that heading still exists on the page
is deliberately out of scope for now. Verifying it needs a slugger that agrees
with Mintlify's character for character, and the current tree shows both failure
directions at once: the heading `crewship routine result <run_id>` publishes as
`#crewship-routine-result-run_id`, so a slugger that drops the underscore
reports the *working* link and blesses the dead
`#crewship-routine-result-run-id` written on another page. On top of that,
API-reference anchors are generated by Mintlify rather than written as
headings, so heading extraction alone does not describe the target's anchor
namespace. Path-level enforcement lands green and is worth having today;
anchor verification is tracked in #1794, and needs the slugger settled first,
with the false-negative direction proven rather than assumed.

The PR gate runs the authentication checks and bounded read-only Schemathesis
coverage against the exact ephemeral server built in that job. Mutating and
stateful scenarios remain nightly/manual because they need a disposable data
policy beyond the control-plane fixture. Do not read a green `Go Lint` as
evidence that the documented behaviour was *exercised* — it is evidence that
the contract is described and internally consistent. The live gate supplies
that semantic check for the safe deterministic layer.

## Artifacts and triage

Every layer emits a small, retention-limited artifact bundle:

- JUnit test results and a machine-readable summary with layer, commit, seed,
  fixture ID, operation/command, and classification;
- Schemathesis cassette or minimized case (with auth, cookies, and secrets
  redacted), OpenAPI snapshot hash, and server logs;
- MDX/inventory report listing the exact page, anchor, source registration, and
  drift type;
- CLI stdout/stderr, exit status, version/build metadata, and workspace cleanup
  report;
- a scrubbed environment/runtime fact sheet (OS, database mode, container
  runtime, feature flags), never the environment dump itself.

Triage order is: fixture/startup failure → harness/auth/workspace failure →
contract mismatch → product regression → flaky/resource failure. Re-run a
failed deterministic case with its recorded seed; do not accept a rerun-only
green result without classifying the first failure. Minimized live cases become
stable fixtures or regression tests when the bug is fixed. Flakes are tracked
with retry counts and quarantined only with an owner and expiry date.

## Acceptance criteria and rollout

The design is complete when every public route and command has an inventory row,
every documented live example has a layer assignment, the golden workflow runs
against a packaged binary, and failures produce the artifacts above without
secret leakage. Rollout is:

1. deterministic inventory/MDX/OpenAPI checks;
2. auth/workspace fixture harness and critical Schemathesis cases;
3. packaged CLI smoke and artifact triage;
4. nightly full/stateful fuzzing and release promotion enforcement.

Initial implementation should be delivered as separate CI/test changes after
this design; this document intentionally adds no scripts, fixtures, or
production behavior.
