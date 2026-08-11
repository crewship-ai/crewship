# Release 1.0 readiness — measured status, 2026-08-10

**Status:** current · **Baseline:** `main` at `69a8ceb9` · **Version in tree:**
`1.0.0-rc.1` (`package.json`)
**Measured on:** crewship-dev, clone `crewship_2`, 2026-08-10, by running the
commands in §1. Nothing in this file is quoted from an issue without being
re-measured; where an issue turned out to be stale, that is recorded in §6.

This answers one question: **how much of the release-1.0 quality bar in
`PRD-RELEASE-1-0-QUALITY-AUDIT.md` is actually done, and what is left.**

The companion document `CODEX-WORK-ORDER-RELEASE-1-0.md` turns the "left"
half into executable work packages.

---

## 0. Headline

The **structural** half of the 1.0 audit is finished and gated in CI. Every
public API operation and CLI command is documented, every flag, environment
variable and manifest kind is documented, every documentation symbol resolves
back to code, and all of that is enforced on every pull request rather than
asserted in prose.

The **semantic** half is not. What the documentation says is complete; whether
the running product agrees with it is still measured by a gate that is
deliberately advisory, an E2E suite where one spec of twenty-one is allowed to
fail the build, a runtime harness that does not run in CI at all, and 35 open
code-scanning alerts.

Scored against the eight conditions of the release-1.0 quality bar:

| # | Quality-bar condition | State | Evidence |
|---|---|---|---|
| 1 | Every public API route documented or marked internal | **done, gated** | `docs-inventory -strict` clean; 538/538 operations `documented_exact` |
| 2 | Documented route matches registered route, method, params, auth, response, errors | **partial** | structure gated; live conformance runs **advisory** (`API_CONTRACT_ADVISORY: "1"`, ci.yml) |
| 3 | Every public CLI command has a page + an executable example | **mostly** | 732/732 documented; 14 commands carry **no test signal at all** |
| 4 | Removed/renamed features removed from docs or moved to a migration page | **done for the gated surface** | 0 missing symbols docs→code (3934 commands, 1992 API paths, 401 env vars, 209 manifest kinds, 1817 flags) |
| 5 | Critical security/credential/persistence/backup/orchestration/migration paths have behaviour tests | **partial** | 5 of 8 audit defects closed; 131 API operations have no test signal; runtime harness tier does not run in CI |
| 6 | Tests do not depend on undeclared local services | **not done — worse than recorded** | **five** Go tests fail on crewship-dev on host state alone (DNS, `/crew` tree, a live Ollama, umask); vitest still emits unhandled `ECONNREFUSED 127.0.0.1:3000` / `::1:3000` |
| 7 | Docs distinguish stable / early / experimental / deprecated / roadmap | **not started** | **0 of 263** pages carry any status frontmatter; `RELEASING.md` points at a stability matrix that does not exist |
| 8 | One canonical terminology map | **not started** | no glossary/terminology page exists anywhere under `docs/` |

Against the PRD's five workstreams: **A (inventory) done and rerunnable**,
**B (API/CLI truth audit) done structurally, open semantically**, **C (test gap
audit) partially done**, **D (documentation rewrite) not started for items 7–8**,
**E (release validation) done except the gates that are still advisory**.

---

## 1. How this was measured

Everything below is reproducible from a clean checkout of `69a8ceb9`:

```bash
go test ./... -count=1                       # full Go suite
pnpm vitest run --reporter=dot                # frontend units
pnpm vitest run --coverage                    # frontend coverage
go run ./scripts/docs-inventory -strict       # documentation completeness gate
go run ./scripts/docs-surface-check           # navigation + inline links
go run ./cmd/gen-openapi                      # regenerate the spec
```

plus the live contract gate against a running instance:

```bash
export CREWSHIP_BASE_URL=http://localhost:8082
export CREWSHIP_TOKEN="$(crewship token create audit --quiet)"   # stdout only
export CREWSHIP_WORKSPACE=<workspace id>
bash scripts/api-contract/run.sh positive
```

> **Trap worth recording.** `crewship token create --quiet` writes the token to
> stdout and the advisory *"Token is sensitive — it won't be shown again"* to
> stderr. Capturing with `2>&1 | tail -1` yields the advisory, not the token,
> and the em dash in it then crashes Schemathesis inside urllib3
> (`UnicodeEncodeError: 'latin-1' codec can't encode character '—'`) before
> a single operation is graded — reported as `failure_class: runtime`,
> `graded: 0`. Capture stdout only.

---

## 2. Tests — what is green today

### 2.1 Go — five host-dependent failures

`go test ./... -count=1` is **red on crewship-dev**, in five packages. Not one
of them is a regression in product code. Every one is a test that depends on
something about its host that nobody declared:

| package | test | what the host did |
|---|---|---|
| `cmd/crewship` | `TestCheckDsnReachability/opted_in_+_DSN_unreachable_→_WARN` | a name under `.invalid` **resolved** |
| `internal/api` | `TestConsolidateRun_SkipsCrewsWithUnresolvablePaths` | `/crew/shared/.memory` exists on this host |
| `internal/consolidate` | `TestConsolidateAllCrews_SkipsWhenStorageUnconfigured` | same directory exists |
| `internal/llm` | `TestLive_OllamaAcceptsEveryStoredShape/stored/api/chat` | a **live Ollama** on `localhost:11434` was reachable, so the test ran — and timed out after 120 s |
| `internal/memory` | `TestWriter_TargetFilePerms_MatchesCodeContract` | host umask is `0002` — **the same umask production uses**; see §2.2 |

The DNS one is the clearest example. The test dials
`127.0.0.1.unreachable.invalid`, assuming a name under `.invalid` (RFC 2606) can
never resolve. On a host whose resolver appends a search domain with a wildcard
A record, it does:

```
$ getent hosts 127.0.0.1.unreachable.invalid
192.168.1.200   127.0.0.1.unreachable.invalid.unifylab.cz
$ ss -ltn | grep :443
LISTEN 0 4096 *:443 *:*
```

so the dial succeeds and `checkDsnReachability` correctly reports `PASS`.

The two consolidate tests **detect** the condition and fail deliberately:

```
consolidate_handler_host_path_test.go:125:
    /crew/shared/.memory exists on this host — the handler fell back to the container literal
```

The directory does exist — and it is **residue from the historical bug these
tests exist to catch**, not something this run created:

```
$ stat -c '%n mtime=%y owner=%U:%G mode=%a' /crew/shared/.memory
/crew/shared/.memory mtime=2026-06-30 13:30:05 owner=ubuntu:root mode=755
```

Dated 29–30 June — the consolidator writing to the host filesystem root, fixed
in early August (`HANDOFF-2026-08-02.md` §1). So the assertion is correct in
principle and **cannot distinguish "the bug happened just now" from "the bug
happened six weeks ago and nobody swept up"**. Two things are needed: a
time-scoped or unique-path assertion, and a cleanup of the residue on the dev
boxes.

The Ollama one is the most expensive — `internal/llm` takes **433 s**, 360 s of
it here — but it is **not** an undeclared dependency, and this is a correction to
the obvious first reading. `liveOllamaBase` dials with a 300 ms timeout and
`t.Skip`s when nothing answers; its doc comment says so explicitly: *"It SKIPs
unless one is actually reachable, so CI stays hermetic while a developer (or the
dev VM's nightly run) gets the real thing exercised."* It ran here because an
Ollama **is** running, and it **failed** —
`Post "http://localhost:11434/api/chat": context deadline exceeded`. That makes
it a live finding to diagnose, not a hygiene problem to gate away. Caveat worth
stating: this host was under concurrent load from a parallel Schemathesis run
when it timed out, so re-run it on a quiet box before drawing conclusions.

### 2.2 The permissions test asserts the opposite of the code, and passes on umask alone

The fifth failure is the most instructive thing found in this pass, and my own
first reading of it was wrong.

`internal/memory/writer.go:173-179` creates parent directories **group-writable
on purpose**, with the reasoning in the code:

> `0o775` (not `0o755`): inside agent containers the memory tree is dual-written
> by the agent (uid 1001, dir owner) and the sidecar (uid 1002, via group 1002 +
> setgid inherited from the prepped `.memory` root). A `0o755` subdir created by
> one party would lock the other out of it until the next root perms prep.

`internal/orchestrator/exec_sidecar.go:1001` confirms the other half: the
container entrypoint runs at **umask 0002** precisely so those bits survive.

`internal/memory/writer_caps_test.go:260` — in a test named
`TestWriter_TargetFilePerms_MatchesCodeContract` — asserts the opposite:

```go
if dp&0o022 != 0 {
    t.Errorf("parent dir is group/other writable: got %#o", dp)
}
```

`MkdirAll` honours umask, so:

| umask | resulting mode | test | matches production? |
|---|---|---|---|
| `0002` (production containers, and this host) | `0775` | **fails** | yes |
| `0022` (GitHub runners, typical dev box) | `0755` | passes | no — g+w stripped |
| `0077` (hardened) | `0700` | passes | no — group locked out entirely |

**The test passes only on umasks production never uses, and fails on the one it
does.** It is named for the code contract and pins the ambient umask instead.
This is exactly the failure mode the repository already names in its own process
notes — *"a check that passes for a reason unrelated to what it claims to
verify"* — and it is the one finding here that would not have surfaced without
running the suite on a differently-configured host.

There is a second-order question behind it: the group bit that the sidecar
depends on is at the mercy of an ambient umask rather than set explicitly. On
this evidence the entrypoint does set `0002`, so production is fine — but the
guarantee rests on a setting no test asserts.

CI is green on all five failures because GitHub runners have no wildcard search
domain, no `/crew` tree, no Ollama, and umask `0022`.

**This is quality-bar condition 6, five times over, and its practical cost is
larger than the condition suggests:** `AGENTS.md` makes `go test ./... -count=1`
the verification loop, and it cannot be run to green on the machine the project
says development happens on. An agent that meets five pre-existing failures on
its first run learns to discount failures — which is the one habit this
repository can least afford. And, as the permissions test shows, at least one of
those five is not noise at all: it is a real defect that only a "wrong" host
could reveal.

Statement coverage was **86.6%** at the audit baseline (`5d3fec1d`). Not
re-measured for this report; `make cover` takes ~40 min and no claim here
depends on the number.

### 2.3 Frontend

```
Test Files  370 passed (370)
     Tests  4308 passed (4308)
  Duration  114.72s
```

Up from 3,688 tests in 317 files at the audit baseline.

Coverage, `pnpm vitest run --coverage`:

| metric | measured | CI threshold |
|---|---:|---:|
| statements | 71.82 | 66 |
| branches | 65.96 | 60 |
| functions | 74.78 | 69 |
| lines | 73.09 | 67 |

**Read that number with its scope.** `vitest.config.ts` measures an *allow-list*:
`lib/**`, `hooks/**`, `app/api/**`, `stores/**`, `components/ui/**`. The rest of
`app/` and `components/` — roughly 120k lines of React — is deliberately outside
the measurement. "71.82% frontend coverage" is therefore not a statement about
the frontend; it is a statement about the slice that has opted in. The comment
in the config says so explicitly, and the decision is defensible. It should not
be quoted without the caveat.

Inside the measured slice, the weak areas are:

| area | statements | note |
|---|---:|---|
| `lib/format` | **0.00** | nothing tests it at all |
| `lib/api` | **9.37** | API helper layer |
| `lib/activity` | **21.76** | activity/trace renderers |
| `components/ui` | 34.05 | shadcn primitives — low value, PRD says do not chase |
| `lib/types` | 62.50 (branches 11.11) | |
| `lib/trace` | 68.57 | |
| `hooks` | 71.05 | |

`lib/format` at zero and `lib/api` at 9% are the two that matter.

### 2.4 The test-isolation defect is still open

The audit baseline recorded *"the frontend coverage command also attempted
connections to `localhost:3000` during test setup. This must be classified as
either an intentional fixture dependency or a test-isolation defect before
1.0."*

It still reproduces, unclassified. The run ends with an unhandled rejection
containing both `ECONNREFUSED ::1:3000` and `ECONNREFUSED 127.0.0.1:3000`. The
suite passes anyway, which is why nobody has chased it: it is noise until the
day something listens on :3000 and a test starts passing for the wrong reason.

### 2.5 Suites that exist but do not run

`scripts/test-harness/` holds **33** suites. The nightly splits them into a
control-plane tier and a runtime tier. **The runtime tier does not run** —
`SEED_ANTHROPIC_API_KEY` is not configured in the repository, so `test-memory`,
`test-first-projects`, `test-delegation`, `test-notifications`,
`test-orchestration`, `test-determinism`, `test-credentials`, `test-crew-links`
and the keeper audit/load suites are skipped every night.

Those are precisely the suites that assert the behaviour downstream of a real
agent reply. A green nightly today means the control plane works; it says
nothing about memory, delegation or orchestration. `#1591` states this in its
own body and is currently open with `harness-control-plane=failure`.

This is a **budget decision for the owner**, not an engineering task: running
them nightly costs provider tokens.

---

## 3. Documentation — what is done

### 3.1 The inventory gate is clean and enforced

`go run ./scripts/docs-inventory -strict` exits zero. At `69a8ceb9`:

| surface | count | documentation | test signal |
|---|---:|---|---|
| API operations | 538 | 538 exact, 0 resource-level, 0 missing | **407** have one |
| CLI commands | 732 | 732 exact, 732 root pages, 0 missing | **718** have one |

- 524 of 538 operations have a concrete 2xx schema; **0** use the generic
  object fallback.
- 225 operations take a request body; 221 have concrete JSON schemas, 4 use
  non-JSON media types; **0** generic JSON fallbacks.
- 343 commands define flags; **343** document all of them.
- 100 environment variables discovered, 0 undocumented. 20 manifest kinds, 0
  undocumented.
- Docs → code: 3,934 command references, 1,992 API paths, 401 env vars, 209
  manifest kinds, 1,817 flags — **0 missing symbols in every category**.

`go run ./scripts/docs-surface-check` exits zero: 263/263 page descriptions
pass quality, 0 restate their title, **1,394 internal prose links checked, 0
dead**, 286 navigation pages all resolve.

That is a genuinely strong position, and it is *gated*, not merely true today:
`Go Lint → OpenAPI spec is up to date`, `Go Lint → API and CLI documentation is
complete`, and `Documentation surface` all fail a pull request.

Minor drift worth one line: the **checked-in** copy of
`docs/prd/reports/release-1-0-api-cli-inventory.{json,md}` is one commit behind
the tree (it reports 406 API operations with a test signal, regeneration at
`69a8ceb9` gives 407). The strict gate checks the invariants, not the freshness
of the committed report, so the report can drift without failing anything.

### 3.2 What the inventory does *not* say

`docs-inventory`'s test column is a heuristic — it matches a route or command
name appearing in a test file. It identifies *likely* coverage, and the PRD says
so. The two numbers that survive that caveat and are still worth acting on:

- **131 API operations have no test signal at all** (132 in the stale committed
  report). Concentrated in `workspaces/pipelines*` (49), `crews` (37) and
  `agents` (20). The full list is regenerable; it is reproduced as work package
  **WP-06** in the work order.
- **14 CLI commands have no test signal at all**: `connector` (+`get`,
  `install`, `verify`), `recipe` (+`get`, `install`, `preview`), `recurring`,
  `saved-view`, `self-update`, `today`, `tui`, `logout`.

### 3.3 What has not been started

**Stability labels (quality-bar 7).** Zero of 263 MDX pages carry a status or
stability field. Every page has exactly `title`, `icon`, `description` (plus one
`sidebarTitle` and one `mode`). Meanwhile `RELEASING.md` publishes a three-tier
contract —

> Inside a release, individual features carry one of three labels in their docs:
> **stable** / **beta** / **experimental** … The full matrix is in
> `docs/production-checklist.mdx`.

— and `docs/production-checklist.mdx` is a 123-line conceptual page with no
matrix in it. So the release process documents a labelling scheme that the
documentation does not implement and points readers at a table that does not
exist. That is the single clearest documentation defect found in this pass.

**Terminology map (quality-bar 8).** No glossary or terminology page exists.
`docs/concepts.mdx` is the closest thing and is an orientation page, not a
canonical map of crew / agent / mission / routine / pipeline alias / memory tier
/ workspace.

**Heading anchors.** `docs-surface-check` verifies the *page* an internal link
points at and resolves `#fragment` away without checking it (#1794). The
reasoning for deferring is recorded and sound — it needs a slugger pinned
against Mintlify's rendered output first.

---

## 4. The API contract — the biggest remaining semantic gap

`cmd/gen-openapi` has been fixed five times in the last three weeks (#1819,
#1824→#1850, #1830, #1832, #1850). Two of those fixes are visible in the spec at
`69a8ceb9` and make the corresponding issue text **stale**:

| claim | when written | measured at `69a8ceb9` |
|---|---|---|
| #1583: "documents only 200, every error status undocumented" | | **false now.** 524×`200`, 526×`401`, 500×`403`, 432×`400`, 267×`404`, 259×`500`, 66×`503`, 61×`409`, 51×`201`, 38×`204`, 22×`502`, 9×`429`, 8×`202` |
| #1824: "~700 query parameters, every one `required: false`" | | **stale.** 238 query parameters remain (the phantom ones died with #1832), and **24 are `required: true`** — inference landed in #1850 |
| #1849: "62 of 461 component schemas unreachable" | | **reproduces exactly.** 463 schemas, 401 reachable from the transitive `$ref` closure of `paths`, **62 unreachable** |

What is *not* stale is the gate's status. `.github/workflows/ci.yml` runs the
positive phase with:

```yaml
API_CONTRACT_ADVISORY: "1"
# Remove this env var when #1815 closes.
```

The advisory flag is well built — it passes only on "exit 1 **and** the JUnit
report shows operations were graded", so a server that will not boot or a
Schemathesis crash still fails the job, and `scripts/api-contract-gate-test.sh`
fails the build if that line moves. But until #1815 closes, **the live check that
proves the documentation matches the product does not block a merge.** For a
release whose central claim is "an agent can drive this API from the spec", that
is the gap that matters most.

### 4.1 Re-measured, 2026-08-10 — and 57% of it is one harness defect

Run against dev2 at `69a8ceb9` (`positive` phase, read-only, `--max-examples 10`,
120/m pacing, 28 min):

```
graded 233 operations · 229 reported findings · 267 unique failures
```

| findings | class | CI, 2026-08-06 |
|--:|---|--:|
| 156 | API accepted schema-violating request | 156 |
| 54 | Response violates schema | 53 |
| 50 | Undocumented Content-Type | 50 |
| 6 | Undocumented HTTP status code | 4 |
| 1 | Server error (5xx) | 1 |

**Essentially unchanged.** Three generator fixes landed in between and moved the
total by two. My own prior expectation — that the number would have dropped
substantially — was wrong, and it is worth recording as a caution: the generator
fixes and the conformance findings are largely disjoint problems.

**But the dominant class is not what it looks like.** Breaking down the 156:

| cases | invalid component |
|--:|---|
| **151** | ``Missing `__Secure-authjs.session-token` at cookie`` |
| 4 | `in query - object with unexpected properties` |
| 1 | ``Missing `toolkit` at query`` |

A representative case:

```
- API accepted schema-violating request
    Invalid data should have been rejected
    Expected: 400, 401, 403, 404, 405, 406, 409, 422, 428, 5xx
    Invalid component: Missing `__Secure-authjs.session-token` at cookie
[200] OK: {"data":[]}
```

The spec is **right** here. 525 of 538 operations declare

```json
"security": [{"bearerAuth": []}, {"sessionCookie": []}, {"secureSessionCookie": []}]
```

— three *alternative* requirement objects, which is OR semantics and correctly
describes an API that accepts either a bearer token or a session cookie.

The defect is in the **gate's own configuration**.
`scripts/api-contract/schemathesis.toml` supplies the credential as a raw
header:

```toml
headers = { Authorization = "Bearer ${CREWSHIP_TOKEN}", "X-Workspace-ID" = "${CREWSHIP_WORKSPACE}" }
```

Schemathesis therefore has no idea that header satisfies `bearerAuth`. Its
coverage phase dutifully generates a negative case that omits
`secureSessionCookie`, expects a rejection, and gets a 200 — because the bearer
token it does not know about is still attached to every request.

**So roughly 151 of 267 findings (57%) are harness noise, not product defects.**
The real backlog behind #1815 is on the order of **116 findings**, dominated by
*Response violates schema* (54) and *Undocumented Content-Type* (50) — both
genuine documentation-vs-reality gaps worth fixing.

This is a hypothesis with strong evidence, not a proof. The confirmation is
cheap and must be run before anyone plans work from the number: bind the token
through Schemathesis' auth mechanism (or exclude negative cases derived from
security parameters) and re-run. If the 151 collapse, the diagnosis holds.

---

## 5. Product surfaces still failing

### 5.1 E2E

21 spec files exist. The nightly buckets declare 18 of them:

- **GATE** (hard-fails): 1 — `create-crew-wizard.spec.ts`
- **DRIFT** (reported, never fatal): 15
- **EXCLUDED**: 2 — `visual`, `onboarding-wizard`
- **In no bucket at all: 3** — `command-palette.spec.ts`,
  `crew-image-freshness.spec.ts`, `pr-contract.spec.ts`

That third row is why `#1761` is open with `coverage-guard=failure`: the guard
exists precisely to catch a spec that silently belongs to no nightly bucket, and
it is currently catching three. This is a five-minute fix and it is blocking a
red nightly from ever going green.

The drift bucket itself is **70 failing tests across 15 specs** (#1592, refreshed
by every nightly). Root causes are recorded there and are mostly stale selectors
after the `/crews` canvas redesign — with two exceptions that are product bugs,
not test rot:

1. **`a11y.spec.ts` reports live WCAG 2 A/AA violations** —
   `aria-command-name`, `button-name`, `aria-allowed-attr`,
   `link-in-text-block`, `color-contrast` — on `/`, `/crews`, `/routines`,
   `/credentials`, `/settings`, `/admin` and the chat surface. The spec's own
   header documents every one of these as fixed. Nothing was running the scan,
   so they regressed silently. **Accessibility regressions in a 1.0 are a
   product defect, not test debt.**
2. ~~**`POST /api/v1/feedback` answers 404**~~ — **does not reproduce.** Probed
   live at `69a8ceb9`: 401 without credentials, 400 with them and a bad body,
   never 404. Registration is unconditional, so no configuration lacks it. The
   one way it could occur is recorded in WP-17: `feedback.spec.ts` is the only
   spec that drives the API through the *frontend* origin, and
   `next.config.ts:29` proxies `/api/*` to Go **only when `isDev`** — so it 404s
   against any non-dev Next server. Both nightly jobs point at the Go binary, so
   neither is in that state. Treat the entry in #1592 as stale until re-run.

### 5.2 Security scanning

35 open code-scanning alerts on `main` (#1510 tracks them):

| count | severity | rule |
|---:|---|---|
| 31 | high | `go/path-injection` |
| 1 | **critical** | `go/request-forgery` — `internal/llm/ollama.go:170` |
| 5 | medium/low | mermaid GHSAs via `pnpm-lock.yaml` |

`go/path-injection` clusters in `internal/consolidate/` (14),
`internal/memory/durable_write.go` (5), `internal/api/skills_proposed_handler.go`
(5), `internal/skills/authoring.go` (3), `internal/api/memory_portability_placer.go`
(2), `internal/provider/docker/docker_container.go` (1).

Many are likely guarded by `internal/safepath` already and will resolve to
"false positive, dismissed with a reason" — but **nobody has written the reason
down**, and an alert list nobody has adjudicated is indistinguishable from an
alert list nobody has read. Dependabot *alerts* are at 0; there are **16 open
Dependabot PRs** awaiting merge (#1854–#1868).

### 5.3 The unauthenticated surface is pinned by a sample, and three entries are mislabelled

13 of 538 operations declare `security: []` — no authentication:

```
GET  /api/health                        POST /api/v1/auth/forgot
GET  /api/v1/auth/google/status         POST /api/v1/auth/pair/redeem
GET  /api/v1/oauth/callback             POST /api/v1/auth/reset
GET  /api/v1/system/setup-status        POST /api/v1/auth/signup
GET  /api/v1/system/telemetry           POST /api/v1/bootstrap
POST /api/v1/waitpoint-tokens/{token}   POST /api/v1/webhooks/{token}
POST /api/v1/webhooks/{crewId}/{agentId}/trigger
```

Two things follow.

**The set is not asserted anywhere.**
`internal/api/unauth_reachability_test.go` is a good regression guard and states
its own limit plainly: *"This is a sample, not an exhaustive enumeration: Go's
ServeMux exposes no public API […]"*. The allow-list it guards lives in a
comment quoting the 2026-06 audit, not in data. So a route mounted without the
`authed(...)` wrapper is invisible to it. That constraint has since expired —
`cmd/gen-openapi` enumerates all 538 operations, and the generated document is
now machine-readable ground truth for exactly this question.

**Three of the 13 are authenticated and the document says otherwise.**
`POST /api/v1/webhooks/{crewId}/{agentId}/trigger` is HMAC-SHA256 gated in the
handler (`internal/webhook/hmac.go`, constant-time compare, empty secret or
signature rejected outright); the two `{token}` routes carry their credential in
the path. None of those mechanisms is one of the three declared security
schemes, so the generator emits `security: []` — and an agent reading the spec
correctly concludes no authentication is required. **The routes are safe; the
document is wrong about them**, which is the same family as #1819 / #1824 /
#1830 / #1832.

Tracked as WP-24 in the work order.

---

## 6. Stale claims found in our own documents

The most useful finding of this pass is that a meaningful share of the tracked
work is already done and the tracker does not know it. Anyone planning from the
issue list without re-measuring will do work twice.

| document / issue | claims | reality at `69a8ceb9` |
|---|---|---|
| `docs/prd/iteration-quickwins.md` | "**Status: Ready to implement**", 4 tasks | **all four shipped.** `Orchestrator.MaxConcurrentRuns` exists with a default-8 test; `internal/api/crew_services.go` + three test files exist; the keeper gate on credential files exists in `orchestrator_run.go`; Redis `--requirepass` has both a unit test and `test-datastore-redis-auth.sh` |
| #1583 | every error status undocumented | fixed — 13 distinct statuses now documented across 538 operations |
| #1824 | every query parameter `required: false` | fixed by #1850 — 24 are now required |
| #1785 | blocked by #1771 and #1784 | **unblocked** — both merged 2026-08-06. Only the independent walkthrough remains |
| `RELEASING.md` §"Stability tiers" | "the full matrix is in `docs/production-checklist.mdx`" | that page contains no matrix |
| `docs/prd/HANDOFF-2026-08-02.md` | says `docs/prd/` "is untracked, so it never shows in a diff" | **it is tracked** (`git ls-files docs/prd` lists it) |

The `HANDOFF` document is otherwise the highest-value file in `docs/prd/` and
its §7 already models the right behaviour: an explicit list of claims the
session disproved. That convention should be kept.

---

## 7. What is left, ranked

**Blocking a credible 1.0 — must be decided by the owner, not by an agent:**

1. **#1781** — enforce or remove `max_delegation_depth`,
   `max_parallel_delegates`, `delegation_timeout_s`, `temperature`,
   `max_tokens`. Persisted and unconsumed today.
2. **#1782** — is internal `/spawn` / `/assign` strictly LEAD-only, or is the
   autonomy-policy path the contract? Note `#1810`: `/spawn` is gated and
   `/agent/create` + `/crew/create` are not.
3. **#1783** — deliver `learned-*.md` into the boot prompt, or remove the claim.
   Three sentinel tests pin the broken state deliberately.
4. **#1369** — tamper-evident vs strictly append-only journal semantics.
5. **Runtime harness budget** — configure `SEED_ANTHROPIC_API_KEY` or accept
   that memory/delegation/orchestration have no CI coverage at 1.0.
6. **#1849's naming half** — renaming 246 batch-named schemas (`Core…`,
   `Remaining…`, `Final…`, `Workflow…`) is a breaking change for anyone who has
   generated a client. Do it once, deliberately, or never.

**Blocking, and routine enough to delegate:**

7. Close #1815 and delete `API_CONTRACT_ADVISORY` from `ci.yml`.
8. Bucket the three orphan E2E specs; repair the drift bucket spec by spec and
   promote each to `GATE_SPECS`.
9. Fix the live a11y violations (product bug).
10. Stability labels on all 263 pages + the matrix `RELEASING.md` promises + a
    gate.
11. A terminology map, and one canonical spelling per concept enforced by the
    surface check.
12. Behaviour tests for the 131 untested operations and 14 untested commands.
13. Adjudicate all 35 code-scanning alerts; fix or dismiss with a written
    reason.
14. Make `go test ./...` green on a developer host — five tests depend on host
    DNS, a `/crew` tree, a live Ollama and the umask; classify and fix the
    vitest `:3000` connection.
15. Drop the 62 unreachable schemas at generation time.
16. Land the 16 Dependabot PRs.
17. Walk the First Projects ladder end-to-end on a clean instance, by someone
    who did not write it, and record every point where source had to be read
    (#1785, exit criterion).

**Not blocking:** #1794 heading anchors, #1468 tsformat backlog, #1516
migration-status command, frontend coverage in `lib/format` / `lib/api`.

---

## 8. Recommendation

Do **7, 8, 9 and 14** first: those four turn four permanently-red or
permanently-advisory signals into gates that hold. Every later item is easier to
trust once the gates are honest, and every one of them is measurable rather than
a matter of taste.

Items 10 and 11 are the largest volume of work and the least risky — they touch
no runtime code. They are the natural first delegation.

Items 1–6 should not be delegated. They are product decisions with security and
compatibility consequences, and the tests that pin the current behaviour were
written specifically so that nobody could change them by accident.
