# CI/CD implementation — 2026-09-11

Tracking: #2504. Worktree: `/tmp/crewship-3-ci-hardening`.

## Verification and merge

`CI Result` is the stable merge check. The change-plan job reads a real Git diff and fails if the diff cannot be obtained. Documentation-only PRs skip build/test jobs explicitly; frontend-only PRs keep the image, embedded server, browser journeys and frontend tests, while skipping the Go test matrix. Unknown paths, dependency files, scripts, workflow changes and embedded markdown trigger full coverage. Pushes on main run the full suite, giving publication evidence for that exact SHA. `merge_group` is supported by CI, Security and CodeQL.

API, CLI and remaining Go race shards run independently. CI Result directly requires all shards, the shuffle job, and the reusable image build. Browser jobs download the same embedded server from Binary Build and retain independent databases, secrets and processes. Nightly browser and harness matrices likewise build their tools once per workflow run.

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

`Extended Go Checks` runs the otherwise excluded database migration race suite weekly with an explicit long budget, plus bounded mutation fuzzing of four security boundaries. It has a dispatch trigger and retains timing/reproducer artifacts. These do not extend the PR critical path. The first complete long-budget database race result still needs a hosted run; its deadline is an initial operational bound, not a measured performance promise.

Race jobs preserve ordinary Go output and publish JSONL events, slowest-test timings and skipped-event counts. Use those measurements to decide whether API tests need further sharding. Local test speed is not proof of hosted speed.

## Local workflow

Run `bash scripts/verify.sh quick` before pushing: gate regression tests, workflow lint and build/toolchain invariants. `go` adds the full Go suite and vet; `full` adds frontend lint, typecheck, coverage and static export. It prints the checked SHA/dirty state and does not start or reset a live dev instance.

A new persistent self-hosted runner has not been installed. This public repository shares its current development machine with stage and live developer sessions; a pilot must use isolated ephemeral workers and its own capacity limits. During this implementation the default parallel multi-platform release rehearsal approached the host's disk limit; it was stopped and only its identified temporary build directories were removed. Existing caches/data of other sessions were preserved. The standard Go suite, vet, frontend build, lint, 33 nightly browser checks (one intentional skip), CI helper regression tests and real image boot were verified locally; full signed publication is a separate hosted acceptance step.

Nightly retains the existing `nightly-YYYYMMDD-rN` grammar required by installed self-update clients. The numeric suffix is `workflow_run_number * 100 + attempt` (attempts 1–99), bounded for 32-bit clients. The same version is stamped into the server, CLI, frontend and image; snapshot archive/package versions remain digit-leading for dpkg.
