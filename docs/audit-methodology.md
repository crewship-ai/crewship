# Audit methodology — false-positive shapes and how to avoid them

This is a short field guide for anyone running a static + LIVE audit
loop against Crewship. It distils the two recurring failure modes that
surfaced across iterations of the 2026-05 pre-launch audit. The
goal is to prevent the next audit pass from repeating the same
confabulation patterns.

The audit loop produces two kinds of artefact: source-level findings
(grep, AST tools, lint output) and LIVE findings (actual HTTP/JSON-RPC
probes against a running instance). **Each layer carries a distinct
false-positive shape.** A finding is only safe to call closed when both
layers agree.

## Failure mode #1 — citing a commit SHA without verifying it lands in the audited ref

**Symptom.** An auditor reports "fixed on Dev3 via commit `abc1234`"
without checking that `abc1234` is actually an ancestor of the binary
under test.

**Why it happens.** The auditor reads a PR body or earlier commit log,
matches the description to the behaviour they expected, and adopts
the SHA as evidence. The PR's commits may have been force-pushed,
rebased, squash-merged into a different SHA, or never landed at all.

**How to avoid.**

```bash
# Before promoting "fixed on X" to a CLOSED finding, run:
git merge-base --is-ancestor <cited-sha> <audited-ref>

# Or read the source of <audited-ref> directly:
git show <audited-ref>:<path/to/file>
```

Specifically for the deployed Dev3 binary:

```bash
ssh ubuntu@<dev3> 'cd /opt/crewship_3 && git rev-parse HEAD'
# Use that exact SHA as the audited ref.
```

A finding that says "closed on PR #NNN merge" is **conditional** until
PR #NNN merges. Keep it in a separate column from facts about the
current main HEAD.

## Failure mode #2 — concluding "open" from partial source reading

**Symptom.** An auditor greps for the named function, sees the named
guard is missing or returns a permissive value, and reports the
finding as open. The actual flow has an *earlier* guard upstream that
rejects the request before the named function is reached.

**Why it happens.** The auditor scopes the grep too narrowly — typically
to the one function the original report named. They don't trace the
public entry point (the HTTP handler, the MCP dispatcher, the CLI
command) all the way down to the named function.

**Example from the 2026-05 audit.** The memory.write `capForTier`
function returns `0` for the `lessons` tier. A partial-reading audit
concluded that the lessons cap was bypassable: `cap > 0 && len > cap`
short-circuits when `cap == 0`. The full flow has a tombstone in
`handleWrite` that rejects every `tier == "lessons"` write with
`IsError: true` *before* `capForTier` is called. The `0` return is
intentional — capForTier is never reached for lessons in this code
path.

**How to avoid.**

```bash
# For a reject-class finding, trace the entire call chain from public
# entry to first reject. Use multi-line grep with context:
git show <ref>:<file> | awk '/^func.*<EntryPoint>/,/^func [a-z]/'

# Look for early-return / early-reject patterns BEFORE the named
# function is reached. Patterns to recognise:
#   if a.Tier == "lessons" { return ToolResult{IsError: true, ...} }
#   if !canRole(role, "write") { return forbidden }
#   if x == "" { return badRequest }
```

Always sample at least one LIVE probe to confirm the source reading.
A LIVE 401/403 with a sentinel response shape is much harder to
misread than 30 lines of code.

## Three-way state divergence — origin/main vs PR branch vs deployed binary

Crewship deploys can drift across three code states:

1. `origin/main` — the canonical source of truth, the merged baseline.
2. A PR branch — what the maintainer is iterating on.
3. The deployed binary — what is actually running on Dev3 / prod.

Earlier audits assumed these were always in sync. They are not. A PR
may be force-pushed mid-audit. A deployed binary may be built from a
local working tree with uncommitted changes. The maintainer may have
applied a hotfix directly to the deployed instance.

A finding's state must always be qualified with the ref it was
verified against:

| Ref | Verification |
|---|---|
| `origin/main` HEAD | `git merge-base --is-ancestor` + `git show <ref>:<file>` |
| PR branch HEAD | same, against the branch SHA |
| Deployed binary | SSH to the host, `git rev-parse HEAD` on the source tree the binary was built from, plus a LIVE probe |

If those three refs disagree, the report should say so explicitly,
not pick one and treat it as "the state".

## Verified-with-proof vs not-yet-exercised

A finding is one of:

- **VERIFIED-CLOSED-IN-MAIN** — both source grep on `origin/main`
  matches the expected close pattern AND a LIVE probe against a binary
  built from that ref confirms the expected behaviour.
- **SOURCE-VERIFIED-CLOSED** — source grep matches, LIVE probe blocked
  by setup constraints. Acceptable for grep-evident fixes (e.g. a
  static const change) but not for stateful flows.
- **CLAIMED-CLOSED** — a PR is open or merged claiming the fix, but
  neither grep nor LIVE has re-verified on `origin/main`. Useful as a
  signal, not as a fact.
- **OPEN** — finding still present in source AND LIVE.
- **VENDOR-GATED** — close requires an upstream release that has not
  shipped (e.g. Docker SDK v29 is not on the Go proxy at audit time).

The report header should declare which states it commits to and on
what evidence. Reporting an "open" finding as "closed" because a PR
exists is the same anti-pattern as the PRD-retrofit case the audit
loop was created to prevent.

## Sister-site sweep

When a finding cites a specific file:line, the fix should not stop at
that line. Grep for the same pattern across the rest of the package
and the repo:

```bash
git grep -nE '<pattern>' -- '*.go'
```

If the pattern is present elsewhere in code that has the same threat
shape, the fix is incomplete. Either extend the same fix to those
sites in the same PR, or file a tracked follow-up. The PR description
should state explicitly which sister sites were covered and which were
deferred (and why).

Several iterations of the 2026-05 audit demonstrated that the
sister-site sweep becomes habit, not exception, when reviewers ask
for it on every fix PR. The result is fewer follow-up PRs and a
shorter audit loop.

## Cleanup hygiene

LIVE probes create state on the target instance — test users, tokens,
webhooks, pipelines, memory entries. Every LIVE probe must clean up
what it created before the probing agent exits. Audit shims (MCP
servers used to inject adversarial tool returns) are particularly
prone to outlasting their iteration — they persist across reboots
and survive routine `nuke` operations because they live in the
workspace config, not in the data directory.

Run an explicit cleanup pass at the end of every iteration. Verify
with `docker ps -a --filter "name=audit-"` and a workspace probe
listing any audit-tagged resources.

## When to stop

The audit loop stops when:

1. Every HIGH finding is either VERIFIED-CLOSED-IN-MAIN, SOURCE-
   VERIFIED-CLOSED with a documented LIVE gap, or VENDOR-GATED with a
   monitoring plan.
2. No new HIGH finding has surfaced in the last iteration.
3. The maintainer has signalled readiness by landing a STOP-checkpoint
   document (the 2026-05 audit used `PROGRESS.md` at the repo root).

If any of those three are missing, the loop has more work to do.
