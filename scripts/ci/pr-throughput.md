# Measuring PR throughput

Read-only measurement tool for CI and merge-queue tuning. It calls authenticated
`gh api` and `gh pr list`; it does not post comments, change settings or run CI.
Use Python 3 and GitHub CLI from the repository root:

```bash
python3 scripts/ci/pr-throughput.py --since 2026-10-01 --until 2026-10-05 \
  --limit 10 --output /tmp/throughput.json --raw-output /tmp/throughput-raw.json
```

Outputs include raw API evidence separately from computed metrics. Keep captured
reports outside the public repository. The sample is the latest `--limit`
merged PRs in the inclusive merge-date range, not all opened PRs; abandoned,
closed and still-open PRs are outside this cohort. Compare equivalent cohorts
before and after changing CI. Duration summaries use seconds and nearest-rank
p50/p90, including observed failed and cancelled workflow attempts rather than
selecting successful runs only. Null durations are excluded with explicit
observation counts.

## Definitions

- **Open to merge:** GitHub PR `createdAt` to `mergedAt`, including draft,
  review and human waiting time.
- **Final substantive push to merge:** an actual `commit.pushedDate` or
  `HeadRefForcePushedEvent.createdAt` for the last substantive commit in the PR
  commit order. The tool excludes multi-parent commits only when API ancestry
  comparisons prove a parent belongs to main before the PR's merge commit.
  The anchor is that merge commit's first parent; a retained PR commit at that
  anchor or incomplete parent data leaves provenance unknown and push latency
  null. Custom synchronization headlines work; a headline mentioning main is
  insufficient evidence. The collector requests up to ten parents per commit.
  Ancestry comparisons share the bounded API budget and cache repeated pairs.
  It does not exclude all bot commits: dependency and automated code changes are
  substantive. GitHub Actions and update-branch synchronization are excluded when main
  ancestry is proven, independently of actor. This deliberately retains useful
  automated code changes rather than treating every github-actions commit as
  non-substantive. Squash/rebase synchronization without a multi-parent main
  merge remains substantive; ambiguous rebase anchors remain unknown. Changes removed by force-push cannot always be reconstructed.
  A missing final push timestamp produces null, never an author/committer date
  or workflow creation proxy. Earlier commits with known timestamps cannot
  replace a final commit whose push timestamp is unknown. An observed repush
  of the final substantive SHA counts as its latest evidenced push.
- **Workflow run versus rerun:** different run IDs are separate runs, even at
  the same SHA. `run_attempt > 1` is a rerun; each attempt is fetched separately.
  New pushes and branch updates cause new runs, not rerun attempts. Per-PR
  `ci_runs`/`ci_rerun_attempts` count only CI; `required_workflow_runs` and
  `required_workflow_rerun_attempts` count all three required workflows.
  Three workflows at the same head are not three CI runs. The Actions API
  cannot reliably distinguish human rerun-button use from automation requesting
  a rerun; attempt counts are observed reruns without human attribution. Each row
  retains head SHA, an evidenced automatic-main-sync trigger classification and
  PR automatic-main-sync commit count for interpretation. Unknown historical
  push kinds stay unclassified; separate Security/CodeQL/CI runs at one head
  are not reported as three pushes.
- **Workflow queue:** first executed job start minus workflow creation, for
  attempt 1 only. This includes GitHub dispatch/planning overhead, not only
  runner-capacity waiting. GitHub reuses creation time for reruns, so their
  queue and total elapsed are null instead of inflated by the original run.
- **Execution wall:** first executed job start to last executed job finish,
  including dependency gaps. **Job execution sum** adds job start-to-finish
  durations and includes setup; it is compute occupancy, not billed cost.
  Jobs with no steps do not establish execution. A run with no executed job
  has unknown queue/wall/elapsed, while its conclusion still counts.
- **Failures:** non-success attempt conclusions include failure, cancellation,
  timeout and unfinished runs; job timeout counts are retained separately.
  Sample workflow summaries cover CI, Security and CodeQL PR events only.
  Main publication and merge-group runs are separate populations and are not
  claimed as measured by this collector.

## Bounds and acceptance limits

The default REST pagination cap is three pages per collection; GraphQL commits
and force-push events are capped at 100 each. Hitting any page cap is reported;
truncated commit/timeline evidence makes push latency unavailable. Attempt
retrieval is capped at five per run. A global 150-request budget aborts the
collection instead of silently treating partial data as complete. Override
`--max-pages`, `--max-attempts` and `--max-requests` deliberately for larger
samples. PR listing is a separate bounded CLI call. Workflow pagination starts
at the earliest sampled PR creation, so long-lived PRs can reach the cap even
when the merge-date window is short.

Matching uses GitHub PR associations or a known current/force-pushed head SHA.
Deleted ordinary push history without associations can be unavailable. Run
pagination warnings mean run counts and percentiles are incomplete; do not
use such a sample to assert throughput improved. GitHub commonly returns null
`pushedDate`; the report makes that evidence gap visible. The collector does
not claim to identify who requested every automatic update or reconstruct
unpublished pushes. It also cannot measure true rerun enqueue latency from
the Actions API's reused creation timestamp.

Unit tests run in the existing `scripts/ci/test_*.py` discovery suite. They check
missing timestamps, substantive commits, main-parent ancestry, deceptive/custom headlines, forced pushes,
truncation, rerun timestamp semantics, timeout evidence and percentile handling.
