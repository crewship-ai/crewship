# Work order — Crewship release 1.0

**Audience:** an external coding agent (OpenAI Codex or equivalent) working this
repository unattended, one work package at a time.
**Baseline:** `main` at `69a8ceb9`. Every number below was measured on
2026-08-10; the commands that produced it are given so you can re-measure.
**Companion:** `RELEASE-1-0-READINESS-2026-08-10.md` — read it first. It is the
evidence; this file is the instruction.

---

## 0a. Status board — updated 2026-08-10, end of day

This section is the first thing to distrust. Check it, then check the tracker,
then measure. It was accurate when written and that is all anyone can say.

**Landed on `main`:** WP-03 (#1869), WP-10 (#1875), WP-11 (#1874), WP-13
(superseded by #1868), WP-18 (#1880), WP-21 (#1886), WP-24a (#1879).

**Open, CI green, unmerged:** WP-12 partial — #1895, #1896, #1898.
WP-01 (#1905), WP-02 (#1906), WP-09 part 1 (#1904).

**Closed as "does not reproduce":** WP-17.

**Not started:** WP-04, WP-05, WP-06, WP-07, WP-08, WP-14, WP-15, WP-16, WP-19,
WP-20, WP-22, WP-23, WP-24b, and the rest of WP-09 and WP-12.

**Two review notes carried forward:**

1. **#1898's tests are coupled to the wrong half of their own fix.** They assert
   `strings.Contains(err.Error(), "symlink")`, which is the wording of the
   explicit `Lstat` guard. Removing the *root confinement* (`root.OpenFile` →
   `os.OpenFile`) leaves them **green**; removing the `Lstat` makes them fail
   with a perfectly correct refusal from `os.Root` (`path escapes from parent`).
   So the test pins a redundant guard's phrasing and is blind to the
   load-bearing one. The behavioural assertion (victim file unchanged) is
   correct and should stay — loosen only the message check.
2. **WP-10 labelled 247 of 263 pages `experimental`, 16 `stable`.** That follows
   this order's "when unsure, under-claim" instruction and is not a defect. But
   a 1.0 whose documentation says 94% of the product is a research preview is a
   statement for the owner to make deliberately, not a side effect of a
   labelling pass. Raised, not resolved.

---

## 0. Read this before you touch anything

This repository is not short of code. It is short of *proof*. Almost every
defect that reached production here passed its unit tests first, and the ones
that did the most damage passed a test that asserted the wrong property — a
count instead of a value, a presence instead of a behaviour.

So the standard is not "make the test pass". The standard is:

> **A test you cannot break on purpose is not evidence.**

Everything in §2 and §3 exists to make that operational. If you take one thing
from this document, take that sentence.

The second thing to know is that **the issue tracker is partly stale.** Three of
the findings that motivated the packages below were already fixed by the time
this order was written, and two documents in `docs/prd/` describe work as
pending that shipped a week ago. §4 is a mandatory re-measurement step for
exactly this reason. Doing the work described in a stale issue is worse than
doing nothing: it produces a plausible PR that fixes an imaginary problem.

---

## 1. Ground rules — non-negotiable

These come from `CLAUDE.md`, `AGENTS.md` and `CONTRIBUTING.md`. They are
repeated here because an agent that does not read them will produce work that
cannot be merged.

1. **Claim the issue before your first commit.**
   `scripts/claim-issue.sh <n>` — many sessions push as the same account, so the
   GitHub assignee field is not a lock. `scripts/claim-issue.sh <n> --check`
   first. Release in-thread when you stop, **including when you failed**, so the
   next session starts from your evidence rather than from zero.
   `scripts/claim-issue.sh <n> --release "why you stopped"`.

   **If the package has no issue, open one — that is part of the package, not a
   blocker.** This order was written before the issues existed, and several
   packages describe work that has never been filed. Correcting that was an
   omission in the order, found by an agent who stopped rather than guessing —
   which was the right call under the rule as originally written.

   The packages with a tracking issue today: WP-03 (#1761), WP-04 (#1592),
   WP-08 (#1849), WP-09 (#1815), WP-12 (#1510), WP-16 (#1785), WP-18 (#1852),
   WP-20 (#1740, #1807, #1597, #1464), WP-22 (#1486), WP-23 (#1794).

   The packages with **none** — file one first: **WP-01, WP-02, WP-05, WP-06,
   WP-07, WP-10, WP-11, WP-13, WP-14, WP-15, WP-17, WP-19, WP-21, WP-24.**

   The issue body is the package text: what is wrong, the command that measures
   it, the current number, and what "done" means. Reference this document and
   the package id. Then `--check`, then claim it, then work. Do not open an
   issue per PR when a package produces several — one issue per package, and the
   PRs reference it.

2. **Test first, and the test must be red first.** Go: table-driven `*_test.go`
   beside the package. Frontend: Vitest for units, Playwright for flows. A bug
   fix is red test → fix → green, and **the test must fail on current `main`**
   before merge. Paste the red output in the PR.

3. **Branch from `main`. Never commit to `main`.**

4. **Docs ship with the feature**, in the same PR. Not after.

5. **Every API endpoint gets a CLI command**, and its acceptance test drives the
   CLI *binary*. That contract is what agents consume; a route with no command
   is not finished.

6. **Never merge on red CI.** Diagnose first. Do not assume a flake — this repo
   has real order-dependent tests (`#1740`) and a chronically flaky race job
   (`#1597`), and "probably a flake" is how both survived.

7. **Wait for CodeRabbit** ~2–5 min after `gh pr create`, and never merge before
   it posts — merging first kills the run and the findings are gone for good.
   **A green CodeRabbit check does not mean it reviewed**: when rate-limited it
   posts `pass / Review rate limited`. Check the posted bodies, not the check:
   `scripts/review-status.sh`. When it *is* throttled (30–45 min, stacks across
   a batch), do **not** wait — review the PR yourself to the same standard, say
   in the PR body what was and was not machine-reviewed, and queue
   `scripts/review-status.sh --retrigger`.

8. **Commit messages: no vendor attribution.** No `Co-Authored-By` trailer, no
   "generated with" footer, and do not name the assistant vendor at all — not
   even as a technical noun. Conventional commits (`feat:`, `fix:`, `test:`,
   `docs:`, `chore:`, `sec:`).

9. **Stage explicit paths.** No `git add -A` — it deletes
   `web/out/.placeholder.html` after a build, and every Go build then fails to
   compile.

10. **Never** add API routes under `app/` (static export drops them), never use
    `"sqlite3"` as a driver name (it is `"sqlite"`), never use npm/yarn (pnpm
    only), never change the GCM byte layout in `internal/encryption/`, never
    change sidecar UID 1002 or agent UID 1001.

---

## 2. What "done, validly and honestly" means here

A work package is done when **all six** hold. Five of six is not done; it is a
draft with a confident summary attached, which is the failure mode this section
exists to prevent.

1. **The claim is measured, not asserted.** Every number in your PR body has a
   command next to it that reproduces it.
2. **A test fails without your change.** Show the red output.
3. **A mutation of your change turns the test red.** Show the mutation and the
   failure. See §3.
4. **The scope is the scope.** You fixed what the package asked for. If you
   found something else, you filed it — you did not silently widen the diff.
5. **The documentation moved with the code**, in the same PR.
6. **What you did not do is written down.** Explicitly, in the PR body, in
   plain words. "Covered 12 of the 15 specs; `connectors.spec.ts` needs the
   `NEXT_PUBLIC_LEGACY_MCP_INTEGRATIONS` decision first, so it stays in DRIFT"
   is a good result. Silence about the other three is not.

### The honesty clause

You will sometimes be unable to finish a package. That is expected and it is
fine. What is not fine is a report that reads as finished when it is not.

- If a test is skipped, say it is skipped and why.
- If a gate is green because it did not run, say so. A `skipped` or `neutral`
  check is **not** a pass. `scripts/review-status.sh --checks` shows
  skipped-but-green checks for exactly this reason.
- If you could not reproduce the defect, **say you could not reproduce it and
  stop.** Do not "fix" it anyway. Three of the packages below exist because
  somebody re-measured instead of trusting a write-up.
- If you fixed the test rather than the bug, say that in those words.
- If you are uncertain whether your fix is right, say which part you are
  uncertain about. An honest 70% is worth more than a confident 100% that is
  wrong, because the honest one tells the reviewer where to look.

Never report a package complete on the strength of a green build alone. In this
repository a green build has meant "nothing ran" often enough that the phrase
carries no information by itself.

---

## 3. The evidence protocol

### 3.1 Mutation testing is mandatory

For every package: write the test, confirm it fails **for the right reason**,
implement, then **mutate the production code and confirm the test goes red**,
revert the mutation, and report the mutation verbatim in the PR.

This is not ceremony. It exists because three "assert the shape, not the value"
holes shipped here in a single day:

- a test asserting a script *referenced* a size constant — so "no cap at all"
  passed;
- a test counting flags across a whole prompt — so moving one from recipe A to
  recipe B passed;
- a ulimits test asserting `positive && soft <= hard` — so `fsize: 4 KiB` (every
  write past 4 KiB dies) passed.

**A count is almost never the property you want to assert. Neither is presence.
Assert the value, or assert what the value has to be worth.**

### 3.1a Read the whole failure list, not the tail of it

`go test ./...` prints thousands of lines and the failures are scattered through
them, not collected at the end. Piping it to `tail` gives you the last package's
verdict and hides the rest. This has now caught two people: the first full run
for this audit was tailed at 40 lines and reported **one** failure where there
were five, and the WP-03 report gave three of the same five.

Always reduce, never truncate:

```bash
go test ./... -count=1 > /tmp/gotest.txt 2>&1; echo "exit=$?"
grep -nE '^(FAIL|--- FAIL)' /tmp/gotest.txt
```

The known-red set on crewship-dev is **five** packages (WP-01). If your run
reports a different number, say which — a sixth is news, and a fourth means
something changed.

### 3.2 Two traps in running mutations

1. **A `sed`/`perl` mutation that does not match is indistinguishable from a
   mutation that was not caught.** Always print whether the patch applied before
   believing a green test. This has burned three sessions.
2. **`git checkout <path>` is not a revert for an untracked file.** A mutation
   "reverted" that way stays live and contaminates the next one.

### 3.3 A rebase is exactly when a guard quietly stops running

Re-run the mutation set after every rebase.

### 3.4 The PR body template

```markdown
## What this changes
<one paragraph>

## Red first
<command + the failing output, before the fix>

## Mutation proof
Mutation applied (confirmed applied — `git diff --stat` shown):
<the exact diff>
Result: <test name> FAILED as required.
Reverted: <how, and confirmation the tree is clean>

## Measured
<before/after numbers, each with the command that produced it>

## Not done in this PR
<explicit list, with reasons — write "nothing" only if it is true>

## Review record
CodeRabbit: reviewed | rate-limited (self-reviewed) | absent
<if rate-limited: what you checked by hand>
```

---

## 4. Verify before you fix — mandatory first step of every package

Before writing a line of code:

1. **Re-run the measurement** given in the package. Record what you got.
2. **If it does not reproduce, stop.** Post the measurement on the issue,
   release your claim, and move to the next package. Do not fix it anyway.
3. **If it reproduces differently** — smaller, larger, different shape — the
   package's numbers are stale. Update them on the issue before starting, and
   scope the work to what you measured, not to what was written.

Precedent, all found while writing this order:

| written | measured at `69a8ceb9` |
|---|---|
| #1583: "openapi documents only 200" | false — 13 distinct statuses documented |
| #1824: "~700 query params, all `required:false`" | 238 remain, 24 are required |
| `iteration-quickwins.md`: "Ready to implement", 4 tasks | all four shipped |
| #1785: "blocked by #1771 and #1784" | both merged 2026-08-06 |
| #1849: "62 of 461 schemas unreachable" | reproduces exactly — 62 of 463 |

Four out of five were stale. Assume the fifth kind exists in whatever you pick
up.

---

## 5. Environment

Development happens on **crewship-dev**, not a laptop. Three live clones, one
systemd unit each; the directory name sets the ports.

| clone | API |
|---|---|
| `/srv/crewship/crewship_1` | `:8081` |
| `/srv/crewship/crewship_2` | `:8082` |
| `/srv/crewship/crewship_3` | `:8083` |

```bash
sudo systemctl reload crewship-ws@2     # build what changed + restart (~7s Go, ~55s with frontend)
```

`stage` (`:8084`) is CD-owned. **Never deploy to it by hand.**

Verification loop:

```bash
go test ./... -count=1        # ~25 min on this host; see WP-01, it is red here
go vet ./...
pnpm lint
pnpm vitest run
pnpm build                    # cross-cutting changes only
go run ./scripts/docs-inventory -strict
go run ./scripts/docs-surface-check
```

Driving the product — always through the CLI, never a DB shell, never
`docker exec` into an agent container. Using it for our own ops is how its bugs
get found:

```bash
export CREWSHIP_SERVER=http://localhost:8082
/tmp/crewship-2-dev whoami                 # the binary dev.sh built from THIS clone
./dev.sh seed                              # prints the account it creates
```

> **Token capture.** `crewship token create <name> --quiet` writes the token to
> **stdout** and the advisory *"Token is sensitive — it won't be shown again"*
> to **stderr**. `2>&1 | tail -1` captures the advisory, and the em dash in it
> then crashes Schemathesis inside urllib3 before one operation is graded.
> Capture stdout only: `tok="$(crewship token create x --quiet 2>/dev/null)"`.

---

## 6. Work packages

Priorities: **P0** restores a gate that is currently lying or absent. **P1** is
required for a credible 1.0. **P2** is real work that a 1.0 survives without.

"Unattended" means the package can be completed without a product decision. Any
package marked **ask first** has a fork in it that is not yours to choose.

---

### WP-01 — five Go tests depend on undeclared host state · P0 · unattended

**Why.** `AGENTS.md` makes `go test ./... -count=1` the verification loop. It
cannot be run to green on the machine the project says development happens on.
That is not a small annoyance: an agent that meets five pre-existing failures on
its first run learns to discount failures, which is the one habit this
repository can least afford. It is also quality-bar condition 6 — *"tests do not
depend on undeclared local services"* — five times over.

**Measured.** `go test ./... -count=1` on crewship-dev, five failing packages,
**no product regression among them**:

| package | test | host dependency |
|---|---|---|
| `cmd/crewship` | `TestCheckDsnReachability/opted_in_+_DSN_unreachable_→_WARN` | a name under `.invalid` resolved |
| `internal/api` | `TestConsolidateRun_SkipsCrewsWithUnresolvablePaths` | `/crew/shared/.memory` exists on this host |
| `internal/consolidate` | `TestConsolidateAllCrews_SkipsWhenStorageUnconfigured` | same directory exists |
| `internal/llm` | `TestLive_OllamaAcceptsEveryStoredShape/stored/api/chat` | a live Ollama was reachable, so it ran — and timed out after 120 s |
| `internal/memory` | `TestWriter_TargetFilePerms_MatchesCodeContract` | umask `0002` — **the umask production uses** |

Each is a separate fix; **one PR per test**, in this order.

**1. `TestCheckDsnReachability`.** Diagnosis is done — verify it, do not redo
it. The test dials `127.0.0.1.unreachable.invalid`, assuming a name under
`.invalid` (RFC 2606) never resolves. On a host whose resolver appends a search
domain with a wildcard A record, it does:

```
$ getent hosts 127.0.0.1.unreachable.invalid
192.168.1.200   127.0.0.1.unreachable.invalid.unifylab.cz
$ ss -ltn | grep :443
LISTEN 0 4096 *:443 *:*
```

so the dial succeeds and `checkDsnReachability` correctly reports `PASS`. Make
the unreachable case deterministic regardless of the resolver: make the dialer
injectable and assert against a stub. A reserved-for-documentation address
(TEST-NET-1, `192.0.2.1`) with a short timeout is acceptable if injection is
disproportionate. Do **not** fix it by deleting the subtest or relaxing the
assertion.

**2 & 3. The two consolidate tests.** They *detect* the condition and fail
deliberately —

```
consolidate_handler_host_path_test.go:125:
    /crew/shared/.memory exists on this host — the handler fell back to the container literal
```

That is honest, and better than passing for the wrong reason. But check what is
actually there before you touch the test:

```
$ stat -c '%n mtime=%y owner=%U:%G mode=%a' /crew/shared/.memory
/crew/shared/.memory mtime=2026-06-30 13:30:05 owner=ubuntu:root mode=755
```

It is **residue from the historical bug these tests exist to catch** — the
consolidator writing to the host filesystem root, fixed in early August
(`HANDOFF-2026-08-02.md` §1) — dated six weeks before this run. So the assertion
cannot distinguish "the bug happened just now" from "the bug happened in June
and nobody swept up", which is the actual defect. Make it time-scoped or
unique-path (assert on a directory this test alone could have created), so it
still catches a live regression on a machine carrying old residue. Separately,
sweep the residue off the dev boxes — but **do not** let the sweep be the fix.

**4. `TestLive_OllamaAcceptsEveryStoredShape` — read the test before deciding.**
The obvious reading is "undeclared live dependency, gate it away". That reading
is wrong. `liveOllamaBase` dials with a 300 ms timeout and `t.Skip`s when nothing
answers, and its doc comment states the intent: *"It SKIPs unless one is actually
reachable, so CI stays hermetic while a developer (or the dev VM's nightly run)
gets the real thing exercised."* It ran here because an Ollama **is** running,
and it **failed**: `Post "http://localhost:11434/api/chat": context deadline
exceeded` after 120 s.

**RESOLVED 2026-08-10 — and the instruction that used to stand here was wrong.**
It said: *"Do not 'fix' it by raising the deadline or adding an opt-in env var —
both would delete the signal."* The first half of that was written before anyone
measured, and measurement refuted it.

What the diagnosis found: the failing sub-case **moves between runs**
(`/api/chat` in one, the bare base URL in the next, siblings passing at 94s and
56s). A failure that relocates is a deadline problem, not a product one. All
four stored shapes do reach a live model and return valid judgements. Measured
on this host with qwen2.5:7b — warm idle call ~7s, cold call 20.6s of which
13.4s is `load_duration`, individual calls 56s and 94s under concurrent load —
against a 120s per-call deadline.

Fixed in `fix(llm): the live Ollama probe timed out on load, not on a broken URL
shape`: warm the model once up front (that sub-case went 120s-timeout → 17s) and
raise the deadline to 5 minutes with the measurements recorded beside it.

**The general lesson is worth more than the fix.** Raising a deadline usually
*does* delete a signal, which is why the instruction was written — so the change
is only defensible with a mutation proving otherwise. Disabling
`endpoint.Normalize` in `NewOllama` reintroduces the `.../v1/api/chat` 404 this
test exists to catch:

```
--- FAIL: .../stored/v1       (0.00s) ollama returned 404: 404 page not found
--- FAIL: .../stored/api/chat (0.00s) ollama returned 404: 404 page not found
```

**0.00s** — the failure is immediate and the deadline never enters into it. That
is what makes the looser deadline safe, and it is the evidence to produce
whenever you want to relax a bound.

**5. `TestWriter_TargetFilePerms_MatchesCodeContract` — this one is a real
defect, and it is the most valuable item in this package.**

`internal/memory/writer.go:173-179` creates parent directories group-writable
**on purpose**:

> `0o775` (not `0o755`): inside agent containers the memory tree is dual-written
> by the agent (uid 1001, dir owner) and the sidecar (uid 1002, via group 1002 +
> setgid inherited from the prepped `.memory` root). A `0o755` subdir created by
> one party would lock the other out of it until the next root perms prep.

`internal/orchestrator/exec_sidecar.go:1001` confirms the other half — the
container entrypoint runs at **umask 0002** so those bits survive.

The test asserts the opposite (`writer_caps_test.go:260`):

```go
if dp&0o022 != 0 {
    t.Errorf("parent dir is group/other writable: got %#o", dp)
}
```

`MkdirAll` honours umask, so:

| umask | mode | test | matches production? |
|---|---|---|---|
| `0002` — production containers, and crewship-dev | `0775` | **fails** | yes |
| `0022` — GitHub runners | `0755` | passes | no, g+w stripped |
| `0077` — hardened | `0700` | passes | no, group locked out |

**The test passes only on umasks production never uses.** It is named for the
code contract and pins the ambient umask instead.

Fix it to assert the contract the code states: owner and group rwx present,
other not writable. Then answer the second-order question in the PR — the group
bit the sidecar depends on currently rests on an ambient umask that no test
asserts. If you conclude the writer should set the mode explicitly (an
`os.Chmod` after `MkdirAll`, since `MkdirAll` cannot escape umask), say so and
file it; **do not** change the writer in this PR. Permission bits on the
1001/1002 boundary are called out in `AGENTS.md` as a security boundary.

**Red first, per test.** For each: with your fix in place, mutate the production
code so the property genuinely breaks (e.g. make `checkDsnReachability` return
`PASS` unconditionally; make the writer chmod `0777`) and confirm the test goes
red. The pre-existing failure is **not** your red — it is red for the wrong
reason, which is the whole point.

**Also in scope.** Sweep for the same class: any test that relies on a hostname
not resolving, a path not existing, a port being free, an external service being
reachable, or an ambient umask. Report what you find even if you do not fix it —
the list is the deliverable as much as the fixes.

**Done when.** `go test ./... -count=1` is green on crewship-dev **and** each of
the five mutations turns its test red.

---

### WP-02 — vitest opens a socket to `localhost:3000` · P0 · unattended

**Why.** Quality-bar condition 6 of the release audit: *"Tests do not depend on
undeclared local services such as a running frontend server."* This was flagged
at the audit baseline, never classified, and still reproduces.

**Measured.** `pnpm vitest run` passes 4308/4308 and ends with an unhandled
rejection containing `ECONNREFUSED ::1:3000` **and** `ECONNREFUSED
127.0.0.1:3000`.

**Scope.**

1. **Attribute it.** Run with `--reporter=verbose --no-file-parallelism` or
   bisect by directory until you can name the file. `components/features/logs/__tests__/logs-panel.test.tsx`
   documents this hazard in a comment ("Unmocked, that is a real socket connect
   to localhost:3000") and is the first place to look, but **do not assume it is
   the source** — confirm.
2. **Classify it**, in writing: intentional fixture dependency, or isolation
   defect.
3. If a defect: mock the transport so no socket is opened. If intentional:
   declare it in `vitest.setup.ts` and fail loudly when the dependency is
   absent, rather than swallowing a rejection.

**Red first.** A test that fails when a socket is opened during the suite. The
cheapest honest version: a setup-file guard that records outbound connections
and fails the run if any occur, landed red, then fixed.

**Mutation.** Re-introduce the unmocked call; the guard must go red.

**Done when.** A clean `pnpm vitest run` produces no unhandled connection
rejection, and something in the repository would fail if one came back.

---

### WP-03 — three E2E specs belong to no nightly bucket · P0 · unattended · ~30 min

**Why.** `#1761` (Nightly E2E failed on main) is open with
`coverage-guard=failure`. The guard exists to catch a spec that silently loses
its nightly coverage, and it is currently catching three. Until this is fixed
the nightly can never be green, so nobody reads it.

**Measured.** 21 `e2e/*.spec.ts` on disk; 18 declared across `GATE_SPECS` (1),
`DRIFT_SPECS` (15), `EXCLUDED_SPECS` (2) in
`.github/workflows/nightly-e2e.yml`. Unbucketed:

- `e2e/command-palette.spec.ts`
- `e2e/crew-image-freshness.spec.ts`
- `e2e/pr-contract.spec.ts`

**Scope.** Run each against a freshly seeded instance. A spec that passes goes
to `GATE_SPECS`. A spec that fails goes to `DRIFT_SPECS` **with its
pass/fail counts recorded in the workflow header comment**, matching the format
already used there. `EXCLUDED_SPECS` requires a written reason in the header and
is the last resort.

**Do not** put a failing spec in `GATE_SPECS` "to be fixed later". The gate list
is short on purpose: *"a one-spec gate that is true beats a seven-spec gate that
is red every night for reasons nobody will read."*

**Done when.** `coverage-guard` passes, and the header comment says what you
measured for each of the three.

---

### WP-04 — repair the E2E drift bucket, spec by spec · P1 · unattended (mostly)

**Why.** 70 tests across 15 specs fail against the shipped UI. They are
non-blocking by design so the rot is visible rather than hidden in
`testIgnore` — but 15 specs of accumulated rot is not visibility, it is a second
place where nobody looks.

**Measured (per `#1592`, refresh it before starting):**

| spec | failing | passing |
|---|--:|--:|
| `crews-real-workflow` | 12 | 0 |
| `crews-redesign` | 11 | 5 |
| `connectors` | 10 | 3 |
| `smoke` | 9 | 0 |
| `a11y` | 8 | 1 |
| `crew-privilege-controls` | 6 | 0 |
| `crews-unification` | 4 | 1 |
| `feedback` | 2 | 4 |
| `mobile-crews` | 2 | 1 |
| `crew-journal` | 1 | 3 |
| `crew-provisioning` | 1 | 0 |
| `edge-cases` | 1 | 2 |
| `feedback-ui` | 1 | 1 |
| `full-integration` | 1 | 15 |
| `manual-crews-walkthrough` | 1 | 1 |

**Known root causes** (recorded in `#1592`, verify each):

- the `/crews` selection-driven canvas deleted `/crews/agents/[id]/*` and
  `/crews/new`; several specs still walk those routes;
- `/crews` no longer has `Status:` / `Role:` filter buttons;
- the crew explorer moved out of `aside` into `main`, which kills
  `crew-privilege-controls` and `crew-provisioning` in shared setup;
- the legacy MCP connector catalog on `/integrations` sits behind
  `NEXT_PUBLIC_LEGACY_MCP_INTEGRATIONS` (default off) — this one is
  **ask first**: repairing `connectors.spec.ts` means deciding whether the
  legacy catalog is part of the 1.0 surface.

**Scope.** **One PR per spec.** Update the selectors against the current UI,
prove it green against a freshly seeded instance, move it from `DRIFT_SPECS` to
`GATE_SPECS`, and update the header counts. From then on it hard-fails, which is
the point.

**Do not** batch several specs into one PR. Each promotion is a separate claim
about a separate surface, and a batched PR that half-works cannot be reverted
cleanly.

**Explicitly excluded from this package:** the a11y failures — see WP-05. They
are not stale selectors.

---

### WP-05 — live WCAG 2 A/AA violations on six surfaces · P1 · unattended

**Why.** This is a **product defect**, not test rot, and it is the one item in
the drift bucket that a 1.0 cannot ship with. `e2e/a11y.spec.ts` runs a full
`wcag2a + wcag2aa` scan with **no rules disabled**, and its own header documents
every historic violation as fixed — colour contrast recomputed, icon-only
buttons given `aria-label`, selects and inputs given labels. Nothing was running
the scan, so the fixes regressed silently.

**Measured.** Live violations reported by the nightly drift job:
`aria-command-name`, `button-name`, `aria-allowed-attr`, `link-in-text-block`,
`color-contrast` — on `/`, `/crews`, `/routines`, `/credentials`, `/settings`,
`/admin`, the New Agent dialog, the agent overview and the chat surface.

**Scope.** Fix the violations in the application. Re-run the scan. Then move
`a11y.spec.ts` into `GATE_SPECS` so a regression fails the nightly instead of
being rediscovered in six months.

**Do not** re-add rule exclusions to the spec. The exclusion list was removed
deliberately when the underlying violations were fixed; putting it back converts
a product defect into a permanently silent one.

**Done when.** The scan is clean on all nine surfaces, `a11y.spec.ts` is in
`GATE_SPECS`, and you have re-introduced one violation by hand and shown the
gate catches it.

---

### WP-06 — 131 API operations have no test signal · P1 · unattended · large

**Why.** Quality-bar condition 5. Every one of these is documented, has a
concrete schema and appears in the spec an agent reads — and nothing asserts it
behaves as described.

**Measured.**

```bash
go run ./scripts/docs-inventory        # regenerate first, the committed copy is stale
python3 - <<'EOF'
import json
d = json.load(open('docs/prd/reports/release-1-0-api-cli-inventory.json'))
no = [o for o in d['api'] if not o.get('test_signals')]
print(len(no))
for o in no: print(o['method'], o['path'])
EOF
```

131 at `69a8ceb9`, concentrated in:

| prefix | count |
|---|--:|
| `/api/v1/workspaces/**` (almost all `pipelines*`) | 49 |
| `/api/v1/crews/**` (mostly `issues/{identifier}/*`) | 37 |
| `/api/v1/agents/**` | 20 |
| `/api/v1/admin/**` | 6 |
| everything else | 19 |

**Scope.** Behaviour tests, not smoke tests. Model them on the existing
adversarial suites — `internal/api/cross_workspace_fence_matrix_test.go`,
`internal/api/ingress_matrix_test.go`, `assignee_write_invariant_test.go` —
which seed two tenants, mint credentials, drive `Router.ServeHTTP`, and assert
the foreign marker cannot be read or persisted.

For each operation assert at least: the success shape matches the documented
schema; the documented error statuses are produced by the conditions that
document them; **and the workspace fence holds** (a caller from workspace B
gets 403/404, never data).

**Sequence.** Do it in prefix batches, one PR per batch, largest first. Do not
open a 131-operation PR.

**Note on the metric.** `docs-inventory`'s test column is a *heuristic* — it
matches a route appearing in a test file. Driving the number to zero is not the
goal and can be gamed by naming a route in a comment. The goal is the assertions
above; the counter is how you pick what to work on next.

**Do not** raise the count by writing tests that assert HTTP 200 and nothing
else. That is the exact failure mode §3.1 exists to prevent.

---

### WP-07 — 14 CLI commands have no test signal · P1 · unattended · small

**Why.** `CLAUDE.md`: *"Every API endpoint gets a CLI command, and its
acceptance test drives the CLI binary — that is the contract agents use."*
Fourteen commands are outside that contract.

**Measured.** `connector`, `connector get`, `connector install`,
`connector verify`, `recipe`, `recipe get`, `recipe install`, `recipe preview`,
`recurring`, `saved-view`, `self-update`, `today`, `tui`, `logout`.

**Scope.** An acceptance test per command that drives the **built binary**, not
the Cobra command object. `tui` and `self-update` need judgement —
a TUI needs a scripted terminal or a headless mode, and `self-update` must never
actually update in a test; assert the version-resolution and the refusal paths
instead, and say in the PR what you could not cover.

**Done when.** Each command has a test that fails if the command is removed, and
the two hard cases carry a written explanation of what is and is not asserted.

---

### WP-08 — 62 unreachable OpenAPI component schemas · P1 · part unattended, part ask first · #1849

**Why.** The generated document is what an agent discovers the API from and what
a client generator compiles. A generated SDK currently emits 62 dead types, and
a caller reaching for the obvious name (`CrewCreateRequest`) gets a type the API
never accepts.

**Measured — reproduces exactly.**

```bash
go run ./cmd/gen-openapi
python3 - <<'EOF'
import json, re
d = json.load(open('internal/api/openapi.gen.json'))
sch = d['components']['schemas']
seen, frontier = set(), set(re.findall(r'#/components/schemas/([\w.-]+)', json.dumps(d['paths'])))
while frontier:
    n = frontier.pop()
    if n in seen or n not in sch: continue
    seen.add(n)
    frontier |= set(re.findall(r'#/components/schemas/([\w.-]+)', json.dumps(sch[n])))
print(len(sch), len(seen), len(sch) - len(seen))
EOF
# → 463 401 62
```

**Part A — unattended.** Emit only the reachable closure at generation time in
`cmd/gen-openapi`. It already knows which schemas each operation references, so
this is a filter, and it makes "unreachable" impossible to reintroduce.

Red first: a test asserting the generated document contains no schema unreachable
from `paths`. It must fail on current `main` (it will — by 62).

Mutation: add a dead schema to the generator's tables; the test must go red.

**Part B — ask first, do not start.** 22 of the 62 are superseded pairs where
the *dead* name is the better one (`CrewCreateRequest` vs
`CoreCrewCreateRequestV2`), and 246 of 463 schemas are prefixed `Core…`,
`Remaining…`, `Final…`, `Workflow…` — which track the
`cmd/gen-openapi/schemas_*.go` file a schema was authored in, not anything about
the API. `RemainingworkspacesV1` (note the casing) is the success response of 40
operations. Renaming is a **breaking change for anyone who has generated a
client**; it wants doing once, deliberately, with the owner's sign-off. Part A
does not depend on it.

---

### WP-09 — close #1815 and delete `API_CONTRACT_ADVISORY` · P1 · unattended · large

**Why.** This is the single most important remaining item. The live gate that
proves the documentation matches the running product **does not block a merge**:

```yaml
# .github/workflows/ci.yml
API_CONTRACT_ADVISORY: "1"
# Remove this env var when #1815 closes.
```

For a release whose central claim is "an agent can drive this API from its
spec", an advisory conformance gate is the gap that matters most.

**Measured 2026-08-10 against dev2 at `69a8ceb9`** — 28 min, `positive` phase,
read-only, `--max-examples 10`:

```
graded 233 operations · 229 reported findings · 267 unique failures
```

| findings | class | CI 2026-08-06 |
|--:|---|--:|
| 156 | API accepted schema-violating request | 156 |
| 54 | Response violates schema | 53 |
| 50 | Undocumented Content-Type | 50 |
| 6 | Undocumented HTTP status code | 4 |
| 1 | Server error (5xx) | 1 |

Essentially unchanged from CI four days earlier, despite three generator fixes
landing in between — the generator defects and the conformance findings turn out
to be largely disjoint problems.

**Start here, because it reorders everything below: 151 of the 156 in the
dominant class are one harness defect, not 151 product defects.**

| cases | invalid component |
|--:|---|
| **151** | ``Missing `__Secure-authjs.session-token` at cookie`` |
| 4 | `in query - object with unexpected properties` |
| 1 | ``Missing `toolkit` at query`` |

The spec is correct: 525 of 538 operations declare
`security: [{bearerAuth: []}, {sessionCookie: []}, {secureSessionCookie: []}]` —
three *alternative* requirement objects, i.e. OR, which correctly describes an
API accepting either a bearer token or a session cookie.

The defect is in the gate's own config —
`scripts/api-contract/schemathesis.toml` passes the credential as a raw header:

```toml
headers = { Authorization = "Bearer ${CREWSHIP_TOKEN}", ... }
```

so Schemathesis does not know that header satisfies `bearerAuth`. Its coverage
phase generates a negative case omitting `secureSessionCookie`, expects a
rejection, and gets 200 — because the bearer token it is unaware of is still
attached.

**Your first task in this package is to confirm or refute that**, before any
other work: bind the token through Schemathesis' auth mechanism (or exclude
negative cases derived from security parameters) and re-run. If the 151 collapse,
the real backlog is ~116 findings and the plan below applies to those. If they do
not collapse, say so — the diagnosis was mine and it may be wrong.

**Re-measure like this** (read-only; the positive phase excludes POST/PUT/PATCH/
DELETE):

```bash
python3 -m venv /tmp/api-contract-venv
/tmp/api-contract-venv/bin/pip install -r scripts/api-contract/requirements.txt
export PATH=/tmp/api-contract-venv/bin:$PATH
export CREWSHIP_BASE_URL=http://localhost:8082
export CREWSHIP_TOKEN="$(./crewship token create audit --quiet 2>/dev/null)"   # stdout only!
export CREWSHIP_WORKSPACE=<workspace id>
export API_CONTRACT_ARTIFACT_DIR=/tmp/api-contract-artifacts
export API_CONTRACT_TIMEOUT=3000 API_CONTRACT_ADVISORY=1
bash scripts/api-contract/run.sh positive
```

Then read `positive-summary.json` and the JUnit report. If `graded: 0`, nothing
was measured — do not report the run as a result.

**Scope, in order.**

1. Confirm or refute the security-scheme diagnosis above, and publish the result
   plus the current class histogram on #1815. Everything else waits on this —
   planning against a number that is 57% noise wastes the whole package.
2. Fix the one that is not a documentation gap:
   `GET /api/v1/admin/memory/versions/{id}/content` returns 5xx because
   `run.sh`'s non-JSON exclusion list has
   `^/api/v1/memory/versions/[^/]+/content` but not the `admin/`-prefixed
   sibling. Decide whether the exclusion or the handler is wrong — a 5xx is not
   a schema-declaration problem either way.
3. Fix the evidence defect: the summary artifact reports `"selected": 305`
   (counted by `jq` from the catalog) while Schemathesis reports
   `231 selected / 536 total` for the same invocation. The artifact a reviewer
   trusts overstates what was probed by 74 operations.
4. Work the remaining classes down, largest first, one PR per class.
5. **Only when the phase is green:** delete `API_CONTRACT_ADVISORY` from
   `ci.yml`, and confirm `scripts/api-contract-gate-test.sh` still passes — it
   fails the build if the advisory line moves, so it must be updated in the same
   PR.

**Do not** widen the advisory exemption to make a run pass. It is deliberately
narrow: it passes only on "exit 1 **and** the JUnit report shows operations were
graded", so a server that will not boot, a schema that will not load, a
Schemathesis crash and a blown deadline all still fail. Widening it — or
replacing it with `continue-on-error` — turns a gate into decoration.

---

### WP-10 — stability labels on every documentation page · P1 · unattended · large, low risk

**Why.** Quality-bar condition 7. And there is a concrete broken promise:
`RELEASING.md` publishes a three-tier contract —

> Inside a release, individual features carry one of three labels in their docs:
> **stable** / **beta** / **experimental**. … The full matrix is in
> `docs/production-checklist.mdx`.

— and `docs/production-checklist.mdx` is a 123-line conceptual page containing
no matrix. The release process documents a scheme the documentation does not
implement and points readers at a table that does not exist.

**Measured.** 263 MDX pages. Frontmatter keys in use across all of them:
`title` (263), `icon` (263), `description` (263), `sidebarTitle` (1), `mode`
(1). **Zero** carry a status or stability field.

**Scope.**

1. **Settle the vocabulary first.** `RELEASING.md` says stable/beta/
   experimental; the release-1.0 quality bar says stable/early/experimental/
   deprecated/roadmap. Pick one set, write it down in `RELEASING.md`, and use it
   everywhere. This is a naming decision with no security consequence — make it,
   state it, and move on; do not stall the package on it.
2. Add the field to every page's frontmatter and render it (Mintlify supports a
   badge/callout; match whatever `docs/` already does for emphasis).
3. Build the matrix `RELEASING.md` promises, and put it where `RELEASING.md`
   says it is — or fix the pointer. Do not leave the pointer dead.
4. **Gate it.** Extend `scripts/docs-surface-check` with a pass that fails when
   a page has no status, or has one outside the vocabulary. Without the gate,
   263 labels decay to 200 within a quarter.

**Red first.** The new pass, landed against the current tree, must report 263
pages missing a status. Then fill them, then land it green.

**Mutation.** Delete the status from one page; the gate must name that page.

**Judgement required.** A label is a claim about stability, and a wrong one is
worse than none. Where you are unsure, mark it `experimental` and list the page
in the PR under "labels I was not confident about" — under-claiming is safe,
over-claiming is not.

---

### WP-11 — one canonical terminology map · P1 · unattended

**Why.** Quality-bar condition 8. No glossary or terminology page exists
anywhere under `docs/`. `docs/concepts.mdx` is an orientation page, not a
canonical map.

**Scope.**

1. Extract the actual vocabulary from the code, not from the docs: crew, agent,
   mission, routine, pipeline, pipeline alias, memory tier, workspace, project,
   skill, recipe, connector, keeper, harbormaster, paymaster, journal,
   waitpoint, crew template, workflow template, triage rule, saved view, hook.
   `docs/manifest/` lists 20 manifest kinds and is a good starting inventory.
2. For each: one sentence of definition, the surfaces it appears on (UI / CLI /
   API / manifest), and — the part that earns the page — **the terms it is
   commonly confused with, and why they differ.** Mission vs routine vs
   pipeline is the pair that costs the most.
3. Record deprecated spellings explicitly: a term we no longer use, what
   replaced it, and when.
4. **Gate it.** A `docs-surface-check` pass that fails when a page uses a
   deprecated spelling. Start the deny-list small and true.

**Do not** write the glossary from the existing prose. Half the point is
finding where the prose disagrees with itself; deriving it from the prose
launders exactly the inconsistency you are looking for.

---

### WP-12 — adjudicate all 35 code-scanning alerts · P1 · unattended, one item ask first · #1510

**Why.** An alert list nobody has adjudicated is indistinguishable from an alert
list nobody has read.

**Measured.**

```bash
gh api repos/:owner/:repo/code-scanning/alerts --paginate \
  -q '[.[] | select(.state=="open")] | group_by(.rule.id)[] | "\(length)\t\(.[0].rule.id)"'
```

| count | severity | rule |
|--:|---|---|
| 31 | high | `go/path-injection` |
| 1 | **critical** | `go/request-forgery` — `internal/llm/ollama.go:170` |
| 5 | medium/low | mermaid GHSAs via `pnpm-lock.yaml` |

`go/path-injection` by file: `internal/consolidate/consolidator.go` (9),
`internal/memory/durable_write.go` (5),
`internal/api/skills_proposed_handler.go` (5), `internal/consolidate/proposed.go`
(3), `internal/skills/authoring.go` (3), `internal/consolidate/dedup.go` (2),
`internal/api/memory_portability_placer.go` (2),
`internal/provider/docker/docker_container.go` (1),
`internal/consolidate/skill_promote.go` (1).

**Scope.** One verdict per alert, written down. For each:

- **Real** → fix it, with a test that constructs the traversal and asserts it is
  refused. This repo has `internal/safepath` and `internal/httpsafe` for exactly
  these two rule classes; prefer routing through them over hand-rolling a check.
- **False positive** → dismiss it in GitHub **with a reason naming the guard
  that makes it safe**, and add a regression test that fails if the guard is
  removed. A dismissal with no test is a promise, not a control.

The `go/request-forgery` in `internal/llm/ollama.go:170` is **critical and
ask first**: the Ollama base URL is operator-configured, so "a user-provided
value controls the request URL" may be the intended contract. Determine who can
set it and at what privilege, and put the answer on the issue **before** you
change behaviour — an over-eager fix here breaks every self-hosted Ollama
install.

**Do not** bulk-dismiss. Thirty-one identical rule ids in seven packages are not
one finding.

---

### WP-13 — ~~land the 16 open Dependabot PRs~~ · **DONE 2026-08-10, not by this order**

**Do not start this package.** #1868 (*"land every open Dependabot bump, and keep
OTLP export alive across otel 1.45"*) merged at 12:19 UTC on 2026-08-10 and took
the whole backlog with it. `gh pr list` now shows zero Dependabot PRs and zero
open Dependabot alerts.

Kept here rather than deleted, because it is the cleanest live demonstration of
§4: this package was written at 11:00 with a measured count of 16, and was
obsolete by 12:19 the same day. **Re-measure before you start anything, however
recently the number was taken.**

Follow-up, if you want one in this area: #1871 is open and proposes grouping
Dependabot minor+patch bumps per ecosystem so the backlog cannot rebuild. That
is a review, not a package.

---

### WP-14 — retire the stale claims in our own documents · P2 · unattended · small

**Why.** Four of five findings sampled while writing this order were stale.
Every stale claim costs the next agent a re-derivation, and one of them cost a
full day already (recorded in `HANDOFF-2026-08-02.md` §8).

**Scope.**

1. `docs/prd/iteration-quickwins.md` — header says **"Status: Ready to
   implement"**; all four tasks shipped. Verify each against the tree
   (`Orchestrator.MaxConcurrentRuns` + its default-8 test;
   `internal/api/crew_services.go` + `crew_services_{,corpus_,lifecycle_}test.go`;
   the keeper gate in `orchestrator_run.go`; Redis `--requirepass` with
   `sidecar_requirepass_argv_test.go` and `test-datastore-redis-auth.sh`), then
   mark the file **done** with the shipping evidence. Do not delete it — the
   reasoning is worth keeping.
2. `docs/prd/HANDOFF-2026-08-02.md` §8 says `docs/prd/` "is untracked, so it
   never shows in a diff". It **is** tracked (`git ls-files docs/prd`). Correct
   it in place, in the style §7 of that file already uses.
3. Post the measured refutations on **#1583** and **#1824** and close them, or
   rewrite them down to whatever genuinely remains. Evidence is in
   `RELEASE-1-0-READINESS-2026-08-10.md` §4.
4. `RELEASING.md` — the dead pointer to a stability matrix. Fix here or in
   WP-10, but not in neither.

---

### WP-15 — the committed inventory report can drift silently · P2 · unattended · small

**Why.** `docs-inventory -strict` gates the *invariants*, not the freshness of
the committed report. The checked-in copy is already one commit behind (it says
406 operations have a test signal; regeneration at `69a8ceb9` gives 407).
Reviewers read the committed file.

**Scope.** Either gate freshness the way `openapi.gen.json` is gated
(regenerate in CI, fail if the tree differs), or stop committing the report and
publish it as a CI artifact. **Ask first is not needed — pick the one that
matches how `openapi.gen.json` is handled** and say why in the PR.

---

### WP-16 — walk the First Projects ladder as a stranger · P1 · unattended · #1785

**Why.** This is a **release-1.0 exit criterion** and the only one that cannot
be self-graded:

> A fresh user can complete the supported quickstart and the golden workflow
> from the documentation without reading source code.

#1785 named two blockers, **both merged on 2026-08-06** (#1771 ladder + gates,
#1784 harness PR subset). What remains is step 2 verbatim: *"Run the ladder
once, end to end, on a clean instance by someone who did not write it, and
record where they had to read source."*

**You are that someone.** This is the one package where being an outsider is the
qualification.

**Scope.**

1. Start from a clean instance (`./dev.sh nuke` on a scratch clone, or a fresh
   container — **not** dev1/dev2/dev3 with existing data, and never `stage`).
2. Work `docs/guides/first-projects.mdx` and the quickstart **strictly from the
   page**. Execute what is printed, verbatim.
3. Every time you have to open a source file, a test, or an issue to proceed:
   **stop and record it** — the step, the page, what was missing, and what you
   had to read. That list is the deliverable.
4. Fix the documentation gaps you found, in the same PR, and re-walk.

**Do not** "fix" the ladder by making the page describe what you had to
discover. Where the *product* is what made the source necessary, file it as a
product issue and say so — the criterion is about the documentation, but the
answer is sometimes a missing command.

---

### WP-17 — ~~`POST /api/v1/feedback` returns 404~~ · **DOES NOT REPRODUCE, 2026-08-10**

**Do not fix this.** Probed against a live seeded instance at `69a8ceb9`:

```
POST /api/v1/feedback  (no credentials)     -> 401 {"error":"no_credentials"}
POST /api/v1/feedback  (bearer + workspace) -> 400 {"error":"message_id required"}
GET  /api/v1/feedback  (bearer + workspace) -> 400 {"error":"message_id or trace_id required"}
```

Never 404. Registration is unconditional
(`internal/api/router_orchestration.go:483-485`) — not inside a wiring guard the
way the agent-webhook route is — so there is no configuration in which the mux
lacks it.

**The one way it could happen, recorded because it is a real trap.**
`e2e/feedback.spec.ts` calls `${baseURL}/api/v1/feedback` directly, and it is the
only spec that drives the API through the *frontend* origin. `next.config.ts:29`
proxies `/api/:path*` to the Go server **only when `isDev`**:

```js
async rewrites() {
  if (!isDev) return []
  return [{ source: "/api/:path*", destination: `http://localhost:${goPort}/api/:path*` }]
}
```

A static export cannot serve `/api/*` at all. So the spec 404s against any
non-dev Next server, and passes against `pnpm dev` or against the Go binary.
Both nightly jobs set `PLAYWRIGHT_BASE_URL=http://localhost:8080` — the Go
binary — so neither is in that state today.

**Verdict:** the finding in #1592 is stale, misattributed, or was captured while
`baseURL` fell back to `localhost:3001` (`playwright.shared.ts:19`) with a
non-dev server. Post this measurement on #1592 and re-run the spec before
treating it as a product bug. If it now fails for a different reason, that
reason is the finding.

---

### WP-18 — a test reports a category unwired when it has a producer · P2 · unattended · #1852

**Why.** `TestObservationalCategoriesHaveAProducer` builds its "produced" set
from `journalCategories` plus a **hardcoded mirror** of `categoryByKind`. But
`inbox.Item` carries a `Category` override, and `internal/keeper/health/alarm.go`
uses it to mint `system.health` items directly — a producer the test cannot see.
So `system.health` sat in `knownUnwired` with a reason that was false.

An allow-list of known gaps is only useful while every entry in it is true. One
wrong entry teaches readers to skim the rest.

**Scope.** Make the test *discover* producers rather than mirror a map by hand,
including the `inbox.Item.Category` override path
(`internal/notifyroute/journal_bridge_test.go`).

**Red first:** a category produced **only** via the `Category` override must not
be reported as unwired. That must fail against the current test.

---

### WP-19 — `lib/format` at 0% and `lib/api` at 9% · P2 · unattended

**Measured** (`pnpm vitest run --coverage`, allow-list slice):

| area | statements |
|---|--:|
| `lib/format` | **0.00** |
| `lib/api` | **9.37** |
| `lib/activity` | 21.76 |
| `lib/types` | 62.50 (branches 11.11) |

Headline: statements 71.82 / branches 65.96 / functions 74.78 / lines 73.09
against CI thresholds of 66 / 60 / 69 / 67.

**Scope.** `lib/format` and `lib/api` only. Formatting helpers are pure
functions and cheap to test properly; the API helper layer is where a silent
error-handling change does real damage.

**Do not** chase `components/ui` (34%) — those are shadcn primitives and the
audit explicitly excludes raising coverage on trivial UI primitives. **Do not**
raise the thresholds in `vitest.config.ts` in the same PR that adds the tests;
raise them in a follow-up, from a **CI** number, never a local one (the config
comment records that local runs read ~1.2pp high and that thresholds set from a
local run failed on their first PR).

---

### WP-20 — the flaky backlog · P2 · unattended, one at a time

Four tracked, each its own PR:

- **#1740** — `TestPipelineScheduler_FireOne_NoMissedOccurrences_NoEvent` is
  order-dependent under `-shuffle`. Reproduce with a fixed shuffle seed and
  report the seed.
- **#1807** — `pins.md` is created empty before it is written;
  `TestPostRunTrigger_WritesIntoTheCrewBindSource` races that window.
- **#1597** — Go Race is chronically flaky: three re-runs, three different
  unrelated tests. Treat as an infrastructure question (parallelism, shared
  temp dirs, a global) before treating it as N test bugs.
- **#1464** — `TestWatch_EmitsLifecycleEvents` times out on macos-arm64 (a 3s
  wall-clock deadline on an fsnotify event). A wall-clock deadline in a test is
  the bug; make it event-driven.

**Do not** fix a flake by adding a retry or raising a timeout without saying, in
the PR, what the underlying race is. If you cannot name it, say you could not.

---

### WP-21 — triage 132 `TODO`/`FIXME` in Go · P2 · unattended · mechanical

```bash
grep -rn "TODO\|FIXME" --include='*.go' internal cmd | wc -l   # 132
```

**Scope.** One pass, three buckets: (a) already done — delete the comment;
(b) real and worth doing — file an issue and reference it from the comment;
(c) not worth doing — delete the comment and say why in the PR.

Deliver as **one** PR with a table. This is the rare package where batching is
correct: the value is the complete triage, and 132 separate PRs would be worse
than the comments.

---

### WP-22 — an explicit bypass test per security invariant · P1 · ask first on scope · #1486

**Why.** Recorded on the issue by the author who needed it: working on #1484
they twice wrote a change that opened a hole in the journal's tamper-evidence,
and **both times an existing test rejected it** — a test they had not written. A
third hole they found by inspection, not by a test. *"That ratio is the
problem: the coverage that saved me was there by good instinct, not by
design."*

**Scope.** Write down each security invariant and give it an explicit
*attempt-to-bypass* test:

- fail-closed wake gate
- tamper-evidence (hash chain, append-only keeper decisions)
- egress fence
- keeper SECRET gating
- credential lease TTL

Model on `test-keeper-toctou.sh`, `test-keeper-ingress-fence.sh`,
`TestVerifyChain_SilentPriorityFlipDetected`.

**The addition that matters most:** *every recovery / repair / acknowledge path
needs its own bypass test.* Any mechanism that undoes or forgives a failed check
is a security surface in its own right. Test shape: given a mechanism that turns
a red into a green, can an attacker reach that state deliberately? If yes, gate
it; if no, prove it with a test that tries.

**Ask first** on which invariants are in scope for 1.0 — the list above is the
issue's list, not necessarily the complete one, and enumerating invariants is a
security-design act.

---

### WP-23 — verify heading anchors, not just page paths · P2 · unattended · hard · #1794

**Why.** `docs-surface-check` gates the page an internal link points at and
resolves `#fragment` away without verifying it. A renamed heading ships a link
that lands on the right page and the wrong place.

**Read the deferral reasoning before starting** — it is recorded in
`docs/prd/documentation-contract-testing.md` ("Fragments are resolved away, not
verified") and in #1794, and it is correct. The current tree fails in **both**
directions with a GitHub-style slugger: the heading
`` ## `crewship routine result <run_id>` `` publishes as
`#crewship-routine-result-run_id`, so a slugger that drops the underscore
reports that *working* link as dead **and** blesses the genuinely dead
`#crewship-routine-result-run-id` on another page. A naive prototype flagged 27
anchors, a mix of real breakage and slugger artefacts.

**Scope, in this order.**

1. Pin the slug algorithm against Mintlify's **rendered output** — confirm on a
   deployed page, not by reading a slugger's source.
2. Extract the anchor namespace a page actually publishes: headings, explicit
   `[#id]` overrides, generated API operation anchors (e.g.
   `#get-apiv1adminmemoryconfig`), component titles (`<Accordion title=…>`,
   `<a id=…>`).
3. Fix the real dead anchors **in the same PR**, so the gate lands green.
4. Prove teeth both ways: break an anchor, show the failure names page *and*
   fragment; mutate the slugger, confirm red.

**Do not** land this gate red with an ambiguous list. That is how gates get
turned off.

---

---

## 6b. Second wave — added 2026-08-10 after the first packages were dispatched

Four packages moved here from the author's own queue because they turned out to
be mechanical once specified, plus one new finding. Take these after your first
package, in the order given.

---

### WP-24 — the spec publishes `security: []` for routes that are authenticated · P1 · part unattended, part ask first · NEW

**Why.** Two separate defects, found together.

**(a) The unauthenticated allow-list is pinned by a sample, not by the set.**
`internal/api/unauth_reachability_test.go` is a good regression guard and says
so honestly in its own header:

> This is a sample, not an exhaustive enumeration: Go's ServeMux exposes no
> public API […]

The allow-list it is guarding lives in a *comment* quoting the 2026-06 security
audit — "health, setup-status, telemetry, bootstrap, auth/\*, oauth/callback,
webhooks/\*, /exposed/\*" — not in data anything checks. A new route mounted
without the `authed(...)` wrapper is invisible to it.

That constraint is no longer true. `cmd/gen-openapi` enumerates all 538
operations, and the generated document records exactly which declare no auth:

```bash
python3 - <<'EOF'
import json
d = json.load(open('internal/api/openapi.gen.json'))
for p, it in d['paths'].items():
    for m, op in it.items():
        if isinstance(op, dict) and op.get('security') == []:
            print(m.upper(), p)
EOF
```

**13 operations at `69a8ceb9`:**

```
GET  /api/health                        POST /api/v1/auth/forgot
GET  /api/v1/auth/google/status         POST /api/v1/auth/pair/redeem
GET  /api/v1/oauth/callback             POST /api/v1/auth/reset
GET  /api/v1/system/setup-status        POST /api/v1/auth/signup
GET  /api/v1/system/telemetry           POST /api/v1/bootstrap
POST /api/v1/waitpoint-tokens/{token}   POST /api/v1/webhooks/{token}
POST /api/v1/webhooks/{crewId}/{agentId}/trigger
```

Pin that set as data, with **a written reason per entry**, asserted against the
generated spec. Exhaustive by construction: a route mounted without `authed(...)`
appears in the spec with `security: []` and fails the test by name. This does not
replace the reachability sweep — the sweep proves 401 behaviour, this proves the
*set*. Keep both.

**(b) Three of the 13 are authenticated, and the document says they are not.**
This is the more interesting half, and it is the same family of defect as
#1819/#1824/#1830/#1832: the document misdescribes the route.

`POST /api/v1/webhooks/{crewId}/{agentId}/trigger` is **not** open. It is
HMAC-SHA256 gated in the handler — `internal/webhook/hmac.go`, with fail-safe
defaults (*"An empty secret or signature is rejected outright"*) and a constant-
time compare. `POST /api/v1/webhooks/{token}` and
`POST /api/v1/waitpoint-tokens/{token}` carry their credential in the path.

None of those three is one of the declared schemes (`bearerAuth`,
`sessionCookie`, `secureSessionCookie`), so the generator emits `security: []` —
which an agent reading the spec correctly interprets as "no authentication
required". Verify each of the three before changing anything; the HMAC finding
is confirmed, the two path-token ones are not.

**Ask first** on the modelling: adding a `webhookSignature` security scheme
(`apiKey`, `in: header`) and a path-token scheme is a public contract change.
Propose the shape on the issue, get sign-off, then implement. Part (a) does not
depend on it and should land first.

**Red first.** Part (a): the allow-list test with one entry deliberately omitted
— it must name the missing operation. Then complete it and land green.

**Mutation.** Remove `authed(...)` from any one registered route, regenerate the
spec, and confirm the test names that route. Revert. Confirm the revert with a
count, not with `git checkout`.

---

### WP-14 · P2 · unattended · small — **moved here from the author's queue**

Retire the stale claims in our own documents. Full specification is at WP-14 in
§6; nothing about it changed except who does it.

### WP-15 · P2 · unattended · small — **moved here**

The committed inventory report can drift silently. Specification at WP-15 in §6.

### WP-18 · P2 · unattended — **moved here**

`TestObservationalCategoriesHaveAProducer` (#1852). Specification at WP-18 in
§6. Take this one before WP-19: its red-first case is already written down for
you, which makes it a good calibration of the evidence protocol.

### WP-19 · P2 · unattended — **moved here**

`lib/format` at 0% and `lib/api` at 9%. Specification at WP-19 in §6. Note the
two "do nots" there — they are the ones most likely to be skipped.

---

### Order for the second wave

**WP-24(a) → WP-18 → WP-14 → WP-15 → WP-19 → WP-24(b) once signed off.**

WP-24(a) first because it is the only one of the five that closes a security
hole in the *checking*, and because a route mounted without auth between now and
1.0 is the failure this whole audit exists to make impossible.

---

## 7. Explicitly not yours

Do not decide, implement, or "clean up" any of the following. They are product
decisions with security or compatibility consequences, and several are pinned by
sentinel tests written specifically so nobody changes them by accident.

| # | Decision | Why it is not delegable |
|---|---|---|
| **#1781** | enforce or remove `max_delegation_depth`, `max_parallel_delegates`, `delegation_timeout_s`, `temperature`, `max_tokens` | persisted and unconsumed; enforcing changes runtime behaviour, removing changes the public schema |
| **#1782** | strict LEAD-only `/spawn` and `/assign`, or the current autonomy path | a security-policy change. See also **#1810**: `/spawn` is gated, `/agent/create` and `/crew/create` are not |
| **#1783** | deliver `learned-*.md` into the boot prompt, or drop the claim | three sentinel tests assert the broken state **on purpose**; read them before touching anything |
| **#1369** | tamper-evident vs strictly append-only journal | |
| — | configuring `SEED_ANTHROPIC_API_KEY` so the runtime harness tier runs nightly | a budget decision |
| **#1849** part B | renaming 246 batch-named schemas | breaking change for generated clients |
| **#1836/#1842/#1843** | the composition substrate epic | in flight on open PRs |

If a package you are working leads into one of these, **stop, write down what
you found, and say which decision blocks you.** Do not choose the fork because
choosing it lets you finish.

Also off-limits: deploying to `stage` (`:8084`, CD-owned), force-pushing shared
branches, and rewriting published history.

---

## 8. Suggested order

Priority is not the same as sequence. This order front-loads the packages that
turn a lying signal into a true one, because everything after them is easier to
trust:

1. **WP-03** (30 min, unblocks a permanently red nightly)
2. **WP-01** (the verification loop must be runnable)
3. **WP-02** (the last open item of quality-bar 6)
4. **WP-05** (a11y — the one product defect in the drift bucket)
5. **WP-09** (re-measure first; the largest and most valuable)
6. **WP-10**, **WP-11** (largest volume, lowest risk, touch no runtime code)
7. **WP-16** (the exit criterion — do it while the docs are fresh in mind)
8. **WP-06**, **WP-07**, **WP-08A**, **WP-12**
9. **WP-04** (one spec per PR, in parallel with the above)
10. **the second wave in §6b** — WP-24(a) → WP-18 → WP-14 → WP-15 → WP-19
11. everything else

---

## 9. When you finish a package

Post on the issue:

- the measurement before and after, each with its command;
- the mutation you ran and the failure it produced;
- what you did **not** do, and why;
- whether CodeRabbit reviewed or was throttled;
- your release of the claim (`scripts/claim-issue.sh <n> --release "…"`),
  including if you failed.

Then stop and pick the next package. Do not chain packages inside one branch.

One last time, because it is the whole document in one line: **a test you cannot
break on purpose is not evidence, and a report that hides what you skipped is
worse than no report.**
