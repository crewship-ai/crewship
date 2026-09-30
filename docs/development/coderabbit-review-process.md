<!-- Moved from CONTRIBUTING.md in the 2026-09-28 repository-clarity
     reorganisation; the entrypoint keeps the rule, this file keeps the
     full explanation. Index: docs/development/README.md -->

## CodeRabbit — wait when it is reviewing, not when it is throttled

After `gh pr create`, give CodeRabbit ~2–5 minutes to post its review.
**If a review is coming, do not merge before it does.** Merge first and
the run errors with *"Review failed — PR is closed"*, and any findings
it would have raised are lost for good.

**If it is rate-limited, do not wait for it.** The per-developer limit
runs 30–45 minutes and stacks across a batch, so waiting buys a queue
position rather than a verdict and stalls everything behind it. Review
the PR yourself instead — a red-first test plus a mutation proving the
test has teeth is the standard this repo applies anyway, and it is a
real review where a queue position is not. Then say in the PR what was
machine-reviewed and what was not, and queue a re-review for **your own
PR** (`scripts/review-status.sh --retrigger <PR>`) so it still lands.

Red CI is a separate gate and stays absolute: throttled or not, never
merge on a failing check.

The trap is that the rule does not verify itself. CodeRabbit reports
through a commit **status**, and when it hits the per-developer review
limit that status is:

```
$ gh pr checks 1568
CodeRabbit    pass    0    Review rate limited
```

`pass` — the same word, colour and position as a PR that really was
read, where the description says `Review completed`. On 2026-07-30
eleven of twelve open PRs carried that green and none of them had been
reviewed. Waiting for the check is not the same as waiting for a review.

So ask the thing that cannot lie about it — the posted comments and
reviews themselves:

```bash
scripts/review-status.sh                 # every open PR
scripts/review-status.sh 1568            # one
scripts/review-status.sh --checks        # + skipped-but-green CI checks
```

It reports one state per PR: **reviewed** (a review was submitted, with
its actionable-comment count), **throttled** (a rate-limit notice was
posted instead — not reviewed), **failed**, **pending** (still inside
the window), **absent** (window elapsed, nothing arrived), or
**unknown** when the API call itself failed. Exit code 3 means at least
one PR is not reviewed. It also flags a review that covers an older
commit than the current head: real review, wrong code.

Two of those states arrive as *the same bytes*. A rate-limited
CodeRabbit sometimes still submits an `APPROVED` review with an empty
body and no inline comments; so does a review on the CHILL profile that
read the diff and found nothing. The tie-breaker is the walkthrough
comment, which on a finished review names the range it read —
`between <base> and <head>`. An empty approval counts as reviewed only
when a walkthrough names that same commit; otherwise it stays the
non-event the green check was. So read the headline, not just the tick:
`review approved, 0 actionable comment(s) (empty body; the walkthrough
records a completed review of <sha>)` is a clean review, and any PR
still described as approving "with no content" is not.

Throttling is a queue problem, not something to fight. Re-request the
reviews serially, seeded from the "next review available in N minutes"
the notice itself carries:

```bash
scripts/review-status.sh --retrigger --dry-run   # see the schedule, post nothing
scripts/review-status.sh --retrigger 2227        # re-request that one
scripts/review-status.sh --retrigger --all       # …or every PR above (long-lived; background it)
```

`--retrigger` refuses to run without a target, because it posts and each
post spends the one slot the limit replenishes — an unscoped run spends
other sessions' slots and pushes your own PR to the back of the queue it
just filled (#2231). Firing `@coderabbitai review` at every throttled PR
at once just re-throttles all but the first. And read the answer carefully: a
re-trigger fired while the limit is still in force comes back with

> ✅ **Action performed** — Review finished.

and nothing else. That reply acknowledges the *command*; no review was
submitted, and CodeRabbit will not re-review a commit it has already
seen. `review-status.sh` flags it rather than counting it.

**The same failure shape, other producers.** `--checks` reports them on
the PR's head commit:

- Jobs that concluded `skipped` — green in the checks list without
  having run. (The Go twin of this is why `scripts/skip-budget.sh`
  exists: `go test ./...` prints `ok` for a package whose every test
  called `t.Skip`.)
- Jobs that concluded `neutral`, which `gh pr checks` renders as
  `skipping`. CodeQL reports this way.
- Green jobs carrying **annotations** — CodeQL findings surface in a
  run's annotations, where no check status shows them.

None of this is enforced. CI runs only the script's offline classifier
tests (`scripts/review-status-test.sh`); nothing blocks a merge on a
missing review, because whether a green CodeRabbit status may block a
merge is branch-protection policy and the repo owner's call. The script
is the instrument; the judgement stays with the person merging.
