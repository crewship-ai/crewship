# PRD: Release 1.0 quality, test, and documentation audit

**Status:** proposed  
**Owner:** Crewship maintainers  
**Created:** 2026-08-04  
**Scope:** release-1.0 readiness

> **Progress against this PRD is measured in
> [`RELEASE-1-0-READINESS-2026-08-10.md`](RELEASE-1-0-READINESS-2026-08-10.md)**
> — every claim there re-measured at `69a8ceb9`, including the ones that turned
> out to be stale. The remaining routine work is broken into executable
> packages in [`CODEX-WORK-ORDER-RELEASE-1-0.md`](CODEX-WORK-ORDER-RELEASE-1-0.md).

## Purpose

Prepare Crewship for a credible 1.0 release by establishing a trustworthy
relationship between the implementation, tests, public API/CLI, and user
documentation.

The goal is not to increase documentation volume. The goal is that a human or
AI agent can reliably answer:

- What does Crewship currently support?
- How do I use it from the UI, CLI, and API?
- Which behavior is stable, experimental, deprecated, or removed?
- Is the documented behavior protected by an automated test?

## Audit baseline

Baseline was taken from `main` at commit `5d3fec1d`.

### Tests

- Go: all packages passed `go test ./... -count=1`.
- Go statement coverage: **86.6%**.
- Frontend: **3,688 tests in 317 files passed**.
- Frontend coverage:
  - statements: **69.02%**
  - branches: **63.28%**
  - functions: **72.32%**
  - lines: **70.14%**

The number of tests is strong, but coverage is uneven. The main frontend gaps
are routines/pipelines hooks, waitpoints and webhook hooks, trace/activity
renderers, notification hooks, API helpers, and parts of the UI shell. Go is
strong overall, with weaker or absent coverage in interactive CLI paths,
OpenAPI generation, glue/router paths, and selected new API helpers.

The frontend coverage command also attempted connections to `localhost:3000`
during test setup. This must be classified as either an intentional fixture
dependency or a test-isolation defect before 1.0.

### Documentation

- 257 documentation files under `docs/`.
- 228 MDX files and 25 Markdown files.
- 45 API reference pages.
- 97 CLI reference pages.
- 67 guide pages.
- 21 manifest reference pages.
- 8 security pages.
- Approximately 466,000 words.
- 251 navigation entries in `docs/docs.json`; all have matching files.
- Two files are intentionally outside the public navigation:
  `docs/audit-methodology.md` and `docs/prd/memory-retrieval-layer.md`.

The documentation is large and actively changed, but it has not yet had a
systematic implementation-to-documentation audit. The main risks are stale
routes, duplicate terminology, undocumented behavior, removed features that
remain described, and examples that no longer match the current CLI or API.

## Release-1.0 quality bar

Release 1.0 should meet these conditions:

1. Every public API route is present in the API reference or explicitly marked
   internal/unsupported.
2. Every documented API route matches the registered route, HTTP method,
   parameters, authorization requirements, response shape, and error behavior.
3. Every public CLI command has a current reference page and at least one
   executable smoke or integration example.
4. Removed or renamed features are removed from current documentation or moved
   to an explicit migration/deprecation page.
5. Critical security, credential, persistence, backup, restore, orchestration,
   and migration paths have behavior-level tests.
6. Tests do not depend on undeclared local services such as a running frontend
   server.
7. Documentation distinguishes stable, early, experimental, deprecated, and
   roadmap behavior consistently.
8. The release documentation has one canonical terminology map for concepts
   such as crew, agent, mission, routine, pipeline alias, memory tier, and
   workspace.

Coverage percentages are signals, not the sole gate. A lower-level utility with
no meaningful risk may remain below target; an authentication or credential
flow may not be accepted merely because its line coverage is high.

## Non-goals

- Rewriting all documentation before knowing which pages are inaccurate.
- Adding screenshots before workflows and wording are stable.
- Raising coverage by testing generated code or trivial UI primitives solely to
  improve a percentage.
- Expanding the product surface during this audit unless a missing behavior is
  required to make an existing documented workflow functional.

## Workstreams

### A. Machine-readable inventory

Create a repeatable report that maps:

```text
source route/command
  -> OpenAPI or Cobra registration
  -> API/CLI documentation page
  -> frontend caller, if applicable
  -> unit/integration/E2E tests
  -> status: documented | missing | stale | deprecated | internal
```

The report should be generated from the repository and be rerunnable after
changes. It should identify missing documentation rather than rely only on
searching for keywords.

### B. API and CLI truth audit

For every public API resource and CLI command, verify the actual behavior from
source and tests. Record discrepancies before editing prose. Prioritize
authentication, workspace scoping, credentials, backups, memory, missions,
routines, approvals, and provider/runtime setup.

### C. Test gap audit

Use coverage plus risk classification to identify missing tests. Prioritize
negative paths, authorization boundaries, tenant/workspace isolation,
migrations, restart/recovery, idempotency, queue behavior, and real CLI/API
contracts. Add tests only after the expected behavior is written down.

### D. Documentation rewrite

After the truth audit:

- remove obsolete pages and claims,
- consolidate duplicate concepts,
- make examples executable,
- standardize terminology and status labels,
- add cross-links between guide, API, CLI, and security pages,
- add screenshots only to stable high-value workflows.

### E. Release validation

Add CI checks for:

- OpenAPI freshness,
- navigation targets,
- documentation links,
- API/CLI inventory drift,
- required examples or smoke checks,
- test and coverage thresholds by risk area.

## Recommended first implementation step

Build **the documentation truth inventory**, not a prose rewrite.

The first deliverable should be a checked-in or CI-generated report containing
one row per public API operation and CLI command. Each row should include its
source location, public path/name, documentation target, test target, and an
initial status.

Suggested first milestone:

1. Extract public API operations from the registered Go routes/OpenAPI output.
2. Extract CLI command names from Cobra registration or generated help output.
3. Resolve API reference and CLI reference pages from `docs/docs.json`.
4. Flag missing pages, duplicate pages, and undocumented operations.
5. Manually validate the first high-risk slice: auth, crews, agents,
   credentials, missions, routines, backups, and memory.

The first inventory implementation is available as:

```bash
make docs-inventory
```

It writes the full machine-readable report to
`docs/prd/reports/release-1-0-api-cli-inventory.json` and a review-oriented
summary to the adjacent `.md` file. The test-column signals are intentionally
heuristic in this first iteration; they identify likely coverage, not proof of
behavioral completeness.

This produces a finite backlog and prevents spending weeks polishing pages for
features that were renamed, removed, or never fully implemented.

## Exit criteria for this PRD

- The inventory has no unexplained public API or CLI entries without an owner.
- All high-risk discrepancies are resolved or explicitly tracked.
- Critical workflows have tests and executable documentation examples.
- Documentation status labels and terminology are consistent.
- CI detects new route/command documentation drift.
- A fresh user can complete the supported quickstart and the golden workflow
  from the documentation without reading source code.
