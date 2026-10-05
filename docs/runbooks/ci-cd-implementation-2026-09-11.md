# CI/CD implementation — 2026-09-11

Tracking: #2504. Worktree: `/tmp/crewship-3-ci-hardening`.

## Verification and merge

`CI Result` is the stable merge check. The change-plan job reads a real Git diff and fails if the diff cannot be obtained. Documentation-only PRs skip build/test jobs explicitly; frontend-only PRs keep the image, embedded server, browser journeys and frontend tests, while skipping the Go test matrix. Unknown paths, dependency files, scripts, workflow changes and embedded markdown trigger full coverage. Pushes on main run the full suite, giving publication evidence for that exact SHA. `merge_group` is supported by CI, Security and CodeQL.

API, CLI and remaining Go race shards run independently. CI Result directly requires all shards, the shuffle job, and the reusable image build. Browser jobs download the same embedded server from Binary Build and retain independent databases, secrets and processes. Nightly browser and harness matrices likewise build their tools once per workflow run.

The API race suite now enumerates race-built top-level tests into four sorted,
interleaved partitions, keeping every parent's subtests together. Each successful
worker records its selected inventory and verifies actual terminal test events.
The stable `Go Race (internal/api)` check verifies four distinct, exhaustive,
non-overlapping inventories and sums their package durations for the existing
2300-second baseline / 1.6x erosion alarm. Package setup runs in each worker,
so the sum is conservative; the 4600-second hang deadline remains unchanged.
Artifacts stay in separate directories. `CI Result` requires workers and the
aggregate, and no failed worker cancels the others. Four partitions also keep
each test-selection argument under Linux's per-argument size limit; unexpected
growth fails clearly instead of truncating coverage.

The other Go race packages run in two groups balanced by committed historical
costs. Fresh `go list` output determines membership; timings only choose the
group and execution order. New packages receive a default cost automatically,
and stale exclusions fail. CLI race, shuffle and platform checks remain intact.
Run `bash scripts/verify.sh quick` to exercise the partition and workflow
contracts. The first hosted execution measures the actual improvement; summed
historical test costs are placement estimates, not predicted wall-clock times.

`Security Result` and `CodeQL Result` distinguish planned exclusions from unexpected skipped/cancelled/failed jobs. CodeQL success proves analysis executed, not that all historic SARIF alerts were remediated; the existing alert tracking policy remains. The deterministic API shape checks still block; the broader Schemathesis finding exemption remains explicit and needs its separate API remediation work.

Recommended required contexts are `CI Result`, `Security Result`, `CodeQL Result`, with a pull-request requirement and conversation resolution. Roll out the rules only after these checks exist on the merged workflow. Enabling them while older branches lack the jobs requires those branches to update from main. The required automated review still needs to be read: a throttled green status is not a review. Do not remove the repository's claim/review process.

## Image and publication

The root Dockerfile uses native Node and Go build stages (`BUILDPLATFORM`); the Go compiler targets `TARGETOS`/`TARGETARCH`. Only the small target runtime stage and the ARM smoke execution need emulation. Corepack is pinned; OCI metadata records the source/version/commit/date.

The image now contains the target-platform sidecar and entrypoint script, with writable default storage paths for the existing non-root user. This was found by the new actual-boot test: the old image passed `version` but failed to start because its sidecar was absent. `smoke-image.sh` verifies commit identity, process survival, health and the embedded UI against the specified image; it does not bypass the sidecar requirement.

PR Image Build is a reusable job inside the CI DAG, with no independent path filter and no registry push. `scripts/pr-image-build-paths.sh` verifies this caller/result contract; its legacy path-parser regression cases remain available. This prevents frontend inputs hidden behind `COPY . .` from escaping image validation.

Nightly is triggered by successful CI on main, a daily schedule, or dispatch on main. Before building, it requires the latest trusted main-push success for CI, Security and CodeQL at the selected full SHA. `workflow_run` is checked for source event, source branch and source repository; checkout/build metadata use that verified SHA, not the later default-branch head. The polling gate fails closed on API errors, missing checks, newer failures and timeouts.

The Docker job first pushes a candidate tag, scans the resulting image, boots both platforms and signs the digest. The binary job prepares checksummed/signed archives and boots the Linux archive. Publication depends on both jobs. It creates a per-run/attempt immutable prerelease and a JSON manifest naming exact SHA, release tag, image and digest; `main-SHA` is then promoted. The rolling `nightly` tag moves only if main still points at that SHA. If main advanced before publication, the run retains candidates and an explicit unpublished manifest; neither binary self-update nor rolling image tags can advance to superseded source. Smoke treats this as an explicit planned skip. The publishing concurrency group never interrupts a running publication. Retention removes only the recognized nightly date/run format, preserving at least twenty releases and fourteen days; stable, rolling and draft releases are excluded.

Nightly Smoke consumes that run's manifest, verifies the exact release and image digest, validates the keyless image signature against the Nightly workflow identity, and boots the image. Manual smoke runs require an explicit successful Nightly run ID; they do not silently pick the latest release. Tag releases also require trusted main ancestry/checks. GoReleaser builds and signs the final version once with publishing disabled; those same archives are scanned, booted and uploaded through a draft release that is published only after upload succeeds. Image and binary candidates build concurrently, with a separate publication job requiring both; stable/version image aliases and the atomic Homebrew formula update run only after the publication job succeeds. Homebrew is a separate retryable job and never advances stable formulas to a prerelease.

GoReleaser configuration uses supported archive-format fields and `go mod tidy -diff`, so release hooks cannot quietly mutate the module lockfiles. Release builds use bounded build parallelism. Production signing and registry promotion are intentionally verified on hosted runners with the real GitHub OIDC context, not simulated with locally copied credentials.

## Nightly coverage and maintenance

Nightly E2E inventory is checked on PRs as well as nightly. New credential reveal and pool specs are classified as covered by their dedicated blocking PR configs. Provider-login UI and routine reliability editor join the nightly gate. Existing gate specs were updated to exercise current radio controls, the actual run-to-Activity handoff and the schedule editor's current navigation/advanced controls; no failing spec was demoted into drift.

Scheduled provider runtime tests fail explicitly when `SEED_ANTHROPIC_API_KEY` is missing. The owner must configure this secret in GitHub; never place provider credentials in source, logs, or chat. A successful control-plane run alone is not full agent runtime validation. The real provider tier still requires its first credentialed execution and may reveal previously dormant failures.

`Extended Go Checks` runs the otherwise excluded database migration race suite weekly across four exhaustive partitions with an explicit long budget, plus bounded mutation fuzzing of four security boundaries. It has a dispatch trigger and retains timing/reproducer artifacts. These do not extend the PR critical path. The first complete long-budget database race result still needs a hosted run; its deadline is an initial operational bound, not a measured performance promise.

Race jobs preserve ordinary Go output and publish JSONL events, slowest-test timings and skipped-event counts. Use those measurements to decide whether API tests need further sharding. Local test speed is not proof of hosted speed.

## Local workflow

Run `bash scripts/verify.sh quick` before pushing: gate regression tests, workflow lint and build/toolchain invariants. `go` adds the full Go suite and vet; `full` adds frontend lint, typecheck, coverage and static export. It prints the checked SHA/dirty state and does not start or reset a live dev instance.

A new persistent self-hosted runner has not been installed. This public repository shares its current development machine with stage and live developer sessions; a pilot must use isolated ephemeral workers and its own capacity limits. During this implementation the default parallel multi-platform release rehearsal approached the host's disk limit; it was stopped and only its identified temporary build directories were removed. Existing caches/data of other sessions were preserved. The standard Go suite, vet, frontend build, lint, 33 nightly browser checks (one intentional skip), CI helper regression tests and real image boot were verified locally; full signed publication is a separate hosted acceptance step.

Nightly retains the existing `nightly-YYYYMMDD-rN` grammar required by installed self-update clients. The numeric suffix is `workflow_run_number * 100 + attempt` (attempts 1–99), bounded for 32-bit clients. The same version is stamped into the server, CLI, frontend and image; snapshot archive/package versions remain digit-leading for dpkg.


## Completion follow-up: report integrity and review identity

The full hosted CI for `efdccf88` passed, including all three race groups, image boot and release packaging rehearsal. The next follow-up strengthens two evidence boundaries discovered during acceptance:

- `review-status.sh` requires a real review covering the current head. A genuine review of an older commit remains visible as evidence, but cannot produce the `reviewed` verdict or suppress a scoped retrigger.
- Nightly drift reports validate complete file/test inventory and distinguish intentional skips from unexecuted serial tests. Global errors, empty reports and unknown outcomes fail closed. Recovery cannot close #1592 on an incomplete report.
- `scripts/ci/e2e-drift-baseline.json` records individual outcomes from hosted run 34620139212 at source 81984158: 50 passed, 70 failed, 6 deliberate skips, 47 not run. New failures, flakes, skips, unexecuted tests and missing tests fail the regression gate. Existing failures remain reported; the baseline is not a claim of full E2E health. Test fixes pass without expanding allowances. Promote repaired specs into the gate and remove their baseline entries together in review; never regenerate a baseline merely to make a failing run green.

The stage implementation being developed independently now freezes source SHA and checks trusted main-push runs of the same three GitHub workflows. Its isolated source-build artifact remains distinct from a registry image digest. No stage files or live services were changed by this CI/CD work.


The first hosted run of the drift regression gate correctly rejected two changed command-palette outcomes, even though the aggregate failure count fell. The palette spec now waits for real toolbar interactivity before testing the keyboard shortcut, owns its project fixture, and checks the current agent overview. All twelve cases passed three consecutive executions locally (36 passed, zero retries/skips). The spec is promoted to the blocking gate and its twelve baseline entries are removed; missing seeded rows now fail explicitly. The remaining measured drift baseline contains 161 outcomes (41 passed, 67 failed, 6 deliberate skips, 47 not run). No new failure allowance was added.


ARM image acceptance also passed on hosted run 34632938365 (`e8067d85`): a native frontend/Go build targeting `linux/arm64`, full commit identity check and actual emulated boot with health and UI. `PR Image Build` supports a manual `platform=linux/arm64` dispatch; ordinary PR calls retain native amd64 and their existing cache. GitHub now enforces full-SHA action pinning at repository level, after verifying every workflow/composite action on current main already complies. Default workflow token permissions remain read-only and workflow-created PR approvals remain disabled. Main branch protection rollout is still pending merge.

The first complete local database race run exposed a measurement mismatch: the populated-journal production budget also timed race instrumentation (82 seconds against a 60-second production ceiling). Both migration scenarios still execute under the detector, while their unchanged wall-time ceilings apply to normal CI builds. The non-race acceptance passed both scenarios in 20.86 seconds combined. The full race suite retains its overall timeout and reports instrumented timings; no migration SQL or data-integrity check was relaxed.

The unpartitioned local race trial still had substantial pending coverage after 85 minutes, so the weekly job now enumerates all current top-level tests/examples/fuzz seeds and distributes their sorted names over four independent runners. Each parent and all its subtests execute in exactly one shard; enumeration errors and empty shards fail closed. Failure in one partition does not cancel the others. Every partition uploads distinct timing artifacts and retains the 90-minute test timeout. Regression tests verify exhaustive/disjoint assignment, Unicode names, failed enumeration, empty selection and preservation of test failures.

## Publication acceptance and runtime package refresh (#2508)

PR #2505 merged as `c50890d9`; trusted-main CI, Security and CodeQL passed. Hosted Extended Go Checks run 34646130309 passed all 316 database race tests across four disjoint partitions and all four fuzz targets. Main now requires the three verdict checks on an up-to-date PR, stale-review dismissal and resolved discussions; release `v*` tags cannot be updated or deleted.

The first actual Nightly run 34649402647 passed binary packaging/signing/scanning but blocked image promotion: Alpine's base still contained OpenSSL 3.5.7-r0, while the v3.24 repository provides 3.5.8-r0 security fixes. The runtime stage now upgrades inherited packages before installing application dependencies. PR, nightly and release builds pull current base images and bypass only the runtime-stage cache, so retrying the same source can obtain OS fixes while compile stages retain their caches. PR images now run the same blocking Critical scan as publication. No vulnerability exception or severity downgrade is introduced. Publication remains unverified until a subsequent complete nightly and its smoke workflow pass.

## Registry platform acceptance (#2510)

After #2509, the actual Nightly scan passed but its second platform pull failed: classic Docker image stores cannot bind both platform images to the same index digest (`cannot overwrite digest`, run 34656533357). The smoke helper now resolves the requested platform to exactly one child manifest in the immutable index before pulling it; missing/ambiguous platform entries fail closed. Publication and signature verification continue to identify the original index, and each boot still verifies the full embedded source SHA. Local PR image tags and single-platform manifest digests remain supported.

The release/nightly legal checker (`scripts/check-release-artifacts.sh`) also
resolves each platform child before creating its extraction container. It logs
both the child and original index, checks the shipped project legal files and
every Go/npm manifest text by SHA-256, and reports only platforms that passed.
Descriptor inspection or resolution failures remain UNVERIFIED and fail under
`--strict`; they cannot bypass either platform's legal/hash checks. Local image
tags retain their existing behavior.

The manual `PR Image Build` workflow, with `registry_image` and `expected_sha` inputs, replays both architecture boots against one supplied registry digest and expected SHA, without rebuilding or publishing. It can validate a candidate before another publication attempt; success does not promote that candidate. This specifically exercises sequential pulls on the same runner, which independent AMD64/ARM build jobs do not cover.

## Review and apply repository policy (#2932)

`python3 scripts/ci/repository-policy.py --output /tmp/crewship-policy-plan.json`
is read-only by default. The plan records the existing policy fingerprint and
proposes a one-PR merge queue: ALLGREEN, SQUASH, one build/group entry, no batch
wait and a 120-minute check deadline. It adds CodeQL High/Critical security
merge protection while preserving stronger existing CodeQL thresholds and all
other tools, rules, bypass actors, review settings and strict required checks.
Unexpected targets, incomplete protections and duplicate rules fail closed.
`allow_auto_merge` is reported separately; this helper does not change it.

Review the saved before/proposed payload. Merge the contributor queue procedure
and helper documentation into main before activation. Then an authorized
operator can apply that reviewed plan, naming the full documentation SHA:

```bash
python3 scripts/ci/repository-policy.py --apply \
  --plan /tmp/crewship-policy-plan.json --documentation-commit FULL_MAIN_SHA
```

The helper verifies main ancestry, reads the current policy again and refuses a
stale or modified plan before PUT. It checks the returned policy after PUT.
GitHub does not provide a documented atomic compare-and-swap for this endpoint;
a concurrent change between the last read and PUT remains possible. Coordinate
policy edits and inspect the live ruleset afterwards. Regenerate/review a stale
plan rather than replacing somebody else's change. The SHA is an operator
attestation that the named commit contains the reviewed procedure; ancestry
alone does not inspect its documentation content. Enable repository auto-merge
separately only when the queue procedure and required checks are ready.

**Code scanning limitation:** GitHub's native code-scanning merge protection
**does not apply to merge queue groups**. It checks eligible PR-diff findings,
not every historical alert or a findings-based queue candidate gate. `CodeQL
Result` on `merge_group` still proves analysis execution only. A queue-specific
findings gate would require separate implementation and validation; do not
claim this policy supplies one. Existing alerts remain remediation work; this
helper neither dismisses alerts nor establishes that the baseline is clean.

Schema: [GitHub repository rules REST API](https://docs.github.com/en/rest/repos/rules).
Scope and exclusions: [GitHub code scanning merge protection](https://docs.github.com/en/code-security/concepts/code-scanning/merge-protection).

CodeQL merge protection can be activated independently before the queue process
is merged. This scope preserves any existing queue without changing it and does
not require a queue documentation SHA:

```bash
python3 scripts/ci/repository-policy.py --code-scanning-only --output /tmp/codeql-policy.json
# Review the before/proposed policy; only the CodeQL rule should change.
python3 scripts/ci/repository-policy.py --apply --plan /tmp/codeql-policy.json
```

The CodeQL workflow now plans both Go and JavaScript/TypeScript on every PR,
including documentation-only and single-language changes. This intentional
latency tradeoff supplies results for the current PR revision before enabling a native
required-tool rule: a green aggregate over skipped analyses cannot establish
that evidence. It removes the previous language/path optimization, preserves
analysis permissions and query exclusions, and leaves all main/merge-group
analyses running. Run an actual documentation-only and single-language PR
canary and inspect both analyses before activating the native rule. Native
merge-queue-group exclusions still apply; this change does not add a
findings-based queue verdict.
