# Trusted CI control inventory

The `Trusted CI Inventory` workflow compares PR control files against a trusted
snapshot from main. It runs on `pull_request_target` or `merge_group`, executes only the trusted main/base guard,
and reads exact-head PR files through the Contents API as bounded UTF-8 data.
It does not check out PR code, install PR dependencies, run PR scripts, consume
PR artifacts or use repository secrets. `GITHUB_TOKEN` has content/PR read and
commit-status write permissions. API failures, missing/binary/oversized files,
malformed workflows and a changed head fail closed. A single recursive Git tree
request verifies that all eight candidate control paths are regular blobs
(`100644` or `100755`), not symlinks or submodules. Contents API responses can
dereference symlinks, so their `type=file` alone is insufficient. The guard
checks each decoded content's Git blob SHA against the tree and Contents
metadata. A truncated tree, duplicate/missing path or mismatched blob fails
closed. Requests are capped at 32 with a 20-second timeout each; the tree is
limited to 5 MiB/30,000 entries, other responses to 512 KiB and files to 256 KiB.

The trusted `scripts/ci/required-inventory.json` records CI/Security/CodeQL
aggregate dependencies and full hashes of their workflow files plus CI routing
and verdict helpers. The guard also compares its own workflow, script and
inventory against trusted main. A PR changing a workflow condition, matrix,
aggregate, routing, verdict or the guard itself therefore cannot silently lower
these controls. Changes to protected files require a separate visible decision,
including harmless edits: this deliberately conservative guard does not infer
whether a change weakens coverage. It is not an immutable inventory of every
product test or every script those workflows invoke.

For PR-target runs the Actions job belongs to the trusted main SHA. The guard explicitly
creates the separate `CI Inventory Guard` commit status on the current PR head,
then links it to its Actions run. It verifies live head identity before reading
and immediately before publishing. Its artifact records trusted/candidate
hashes and job/dependency additions/removals, exact SHAs, the source comparison
link and the administrator decision. None of this replaces execution of the
three required aggregate checks.

After validating the live PR or queue target, each execution first writes a
`pending` status to that exact head. It then inspects controls and validates any
administrator exception. A same-head retry with invalid controls or refused
permission therefore replaces an older green with pending; it cannot reuse the
old successful report. Final success/failure is written only after a second live
identity check. If GitHub is unavailable before target validation or rejects the
pending write, the job cannot revoke an older successful status. That residual
is explicit: the base-SHA Actions job failure alone does not invalidate a
head-SHA commit status. Inspect the linked run; this shared-App operational
guard does not establish the stronger independently controlled status boundary
needed to eliminate every stale-status or spoofing path.

## Deliberate control changes

A control-changing PR must include a refreshed candidate inventory. Use the
**trusted main copy** of the guard to read the candidate worktree as data:

```sh
python3 /path/to/trusted-main/scripts/ci/inventory-guard.py \
  --refresh-inventory /path/to/candidate-worktree
```

This command reads only the five fixed control files and updates the candidate
JSON; it never imports or executes candidate helpers. Reading the fixed plan
and verdict paths rejects symlink parents in `scripts/ci` before writing the
manifest in that same directory; the destination itself must also be a regular
path without a symlink. Do not use the candidate's
edited guard as the source of the refresh command. Review the resulting manifest
diff alongside the control change. A stale candidate manifest is reported by
automatic inspection and cannot be approved by manual dispatch. Without this
check, merging changed controls with stale hashes would break the next trusted
baseline.

After reviewing the exact PR diff and the automatic guard report, a repository
administrator may dispatch the trusted workflow on **main**:

```sh
guard_repo=crewship-ai/crewship
guard_pr_number=1234  # Set the actual reviewed canary/control PR number.
guard_head_sha=$(gh pr view "$guard_pr_number" --repo "$guard_repo" --json headRefOid --jq .headRefOid)
gh pr diff "$guard_pr_number" --repo "$guard_repo"
# Review this exact head and the automatic report before dispatching.
gh workflow run ci-inventory.yml --repo "$guard_repo" --ref main \
  -f pr_number="$guard_pr_number" \
  -f expected_head_sha="$guard_head_sha" \
  -f approve_control_changes=true
```

Dispatch always verifies both the original actor and any rerunning actor through
the repository permission API. Both must currently be administrators; failures
or unavailable permission metadata refuse authorization. `false` runs the same
inspection without approving changes. No label, PR comment or PR-edited manifest
can approve an exception. Approval is scoped to one exact head and is recorded
in a separate Actions run and artifact. It establishes authorized account
intent, not independent human review: agents sharing that account cannot be
distinguished from its owner by GitHub identity.

Wait for the automatic inspection before approving. A subsequent automatic
rerun can overwrite an exception status for the same head; inspect the linked
run and redispatch if necessary. A new head requires a new decision. Older PRs
must update from main to include the guard files. Intentionally changing the
protected controls requires updating the inventory hashes/dependencies in the
same reviewed source change; its approval still comes from the main workflow.
After merging, main supplies that new baseline. Missing or malformed aggregate
jobs/files remain refused even with approval. This workflow becomes usable only
after its source is on main; local tests do not prove a GitHub canary passed.

## Merge queue candidates

On `merge_group`, checkout uses the event's `base_sha`, never its queued
`head_sha`. The guard requires `base_ref` to be main, verifies the generated
`refs/heads/gh-readonly-queue/main/...` branch still points to the exact group
head, and verifies current main still equals that trusted base. Candidate
control files are fetched as data, and the status is posted to the group head
only after a second live ref/base check. A deleted/advanced group or changed
main fails closed; let GitHub rebuild the group rather than approve stale data.

Unchanged controls pass. A control-changing group needs its **own** visible
administrator exception; PR approval is not inherited from labels or artifacts:

```sh
guard_repo=crewship-ai/crewship
# Copy the actual generated branch from the merge_group event, not an invented ref.
guard_queue_ref='refs/heads/gh-readonly-queue/main/REPLACE_WITH_ACTUAL_BRANCH'
guard_head_sha=$(gh api "repos/$guard_repo/git/ref/${guard_queue_ref#refs/}" --jq .object.sha)
guard_base_sha=$(gh api "repos/$guard_repo/git/ref/heads/main" --jq .object.sha)
# Review the exact queue head diff and automatic report before dispatching.
gh workflow run ci-inventory.yml --repo "$guard_repo" --ref main \
  -f queue_ref="$guard_queue_ref" \
  -f expected_head_sha="$guard_head_sha" \
  -f expected_base_sha="$guard_base_sha" \
  -f approve_control_changes=true
```

Leave `pr_number` blank for this target. The exact queue branch and base are
checked through the Git refs API. Dispatch runs from main, not the queue branch.
A current named ref proves that branch's identity, not independent proof of
all intended test execution. Additionally, GitHub selects the `merge_group`
workflow definition from the queued candidate: explicit trusted checkout does
not make a maliciously edited workflow definition unforgeable. The shared-App
limitation below therefore also applies to merge groups.

## Enforcement boundary and rollout

**This is an operational defense against accidental or unreviewed narrowing,
not an unforgeable security boundary.** Required check names and their source
GitHub App do not identify a workflow. A malicious repository PR workflow with status-write permission may
publish the same context through the GitHub Actions App. Fork-token restrictions
reduce that capability for outside contributors; they do not establish a unique
workflow identity. If a check run and a commit status share the required name,
GitHub requires both, so a forged job name alone does not override a failing
trusted commit status. Neither a PR-side
contract test nor this shared-App status alone proves every required test ran.
Hard enforcement requires an independently controlled GitHub App selected as
the required status source, or an organization required workflow controlled
outside the PR repository. Availability of that organization feature and its
permissions has not been established here.

Before requiring this status, merge the source, run actual ordinary-PR,
control-change rejection and exact-head administrator-dispatch canaries, inspect
the status's issuing App and select its source explicitly in policy. Requiring
the source App reduces unrelated issuers; it does not remove the shared-App
limitation above. No live rule or required context is activated by this change.
The current three required aggregate contexts remain unchanged by this source
change. Merge the guard code first, then prove ordinary PR success, negative
control-change rejection and explicit administrator approval on exact PR head
SHAs. After verifying the issuing App, require `CI Inventory Guard` as the fourth
status **before enabling the merge queue**. This avoids an unprotected queue
bootstrap.

On the following day, after the PR baseline is recorded, enable the queue with
all four statuses required. Its first real merge-group canary must prove this
status is emitted on the exact generated queue head SHA. A missing status keeps
the queue blocked; do not remove the requirement to obtain a passing canary.
Do not claim that queue operation is verified until that canary passes. A queue
containing control changes still needs its own explicit exact-head administrator
exception as described above.

The checked-in inventory matches its source baseline when generated. After
merging newer main changes into a control PR, refresh the manifest from a trusted
main helper and rerun validation; an older committed baseline is not evidence
that the inventory matches a subsequently changed main.

GitHub's public-repository `pull_request_target` event policy is currently being
rolled out, with default enforcement documented for November 2, 2026. Verify
this narrowly privileged workflow is allowed by the actual event policy before
activation; a blocked event provides no execution evidence.

Sources: [trusted event and safe handling](https://docs.github.com/en/actions/reference/security/securely-using-pull_request_target),
[check names do not identify workflows](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/troubleshooting-rules),
[required source Apps](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets),
[commit-status API](https://docs.github.com/en/rest/commits/statuses#create-a-commit-status),
[Contents symlink behavior](https://docs.github.com/en/rest/repos/contents#get-repository-content),
[Git tree modes and truncation](https://docs.github.com/en/rest/git/trees#get-a-tree).

## Local verification

```sh
python3 -m unittest discover -s scripts/ci -p test_inventory_guard.py
actionlint .github/workflows/ci-inventory.yml
```
