# Native live acceptance runner

`native-runtime.py` runs the existing Docker-backed
`TestLiveNativeFrozenToolsAndDurableAccounting` contract in
`internal/restricteddispatch`. Successful execution proves the narrow restricted Codex native
tools/Responses loop with durable accounting. It does not prove CLI, HTTP API,
crew execution, or the distinct text-only broker profile. The helper does not enable a workflow, build or publish an image,
install a sandbox policy, or substitute local images.

Supply an explicitly approved, released GHCR reference in the form
`ghcr.io/OWNER/REPOSITORY@sha256:DIGEST`. Tags and other registries are rejected.
The output directory must not already exist. For example, after replacing the
placeholder with the approved reference:

```sh
python3 scripts/ci/native-runtime.py --image "$APPROVED_NATIVE_IMAGE" \
  --output .ci-results/native-runtime
```

Use a Linux amd64 Docker acceptance host with the reviewed native AppArmor
policy already loaded and the required host tools installed. The worker must
support the selected native contract; this runner does not add missing tools
to the image. Missing policy, image content, prerequisites or named evidence
fail the gate.

The runner pulls the exact digest for `linux/amd64`, verifies that reference
against Docker's `RepoDigests`, and records its immutable local image config ID.
`CREWSHIP_RESTRICTED_NATIVE_IMAGE` receives that local ID, because
`NewNative` requires a local config ID rather than a registry manifest digest.

A uniquely named, owned container has no network or host mounts, a read-only
filesystem, UID 1001, all capabilities removed, no new privileges, and explicit
CPU, memory and PID limits. Before starting it, the runner copies `/opt/codex`
to an owned temporary directory and checks its bytes on the host against the
existing reviewed native checksum. Only then does it execute the bounded
`--version` probe. It removes exactly that probe container; it does not prune
Docker resources or remove shared images.

The Go invocation enables `restrictedruntime_live` and the race detector,
selects the single exact named native test, and bypasses cached test results.
Required test and package terminal passes must appear in the correct package. Failures,
skips, races, missing tests or nonzero exit fail acceptance. The Go tool has a
seven-minute timeout and the parent Go invocation an eight-minute bound.
On shared development hosts run the entire helper through `crewship-heavy`.

`image-identity.json` records image mapping, binary checksum/version and final
status. `go-test.jsonl` preserves execution evidence once the Go invocation
starts. The Python regression suite uses test doubles and proves the runner
contract; passing those regressions is not evidence that live native or crew
execution passed. Actual acceptance remains pending until an approved released
image and suitable host are supplied.

On normal completion, failure, timeout, SIGINT or SIGTERM, the runner kills the
child process group and removes only containers and volumes carrying its random
`ai.crewship.native-acceptance.run` label. A temporary Docker CLI proxy adds that
label to every container/volume creation by the selected Go test; runtime owner
labels remain intact. The probe carries the same label. Cleanup lists the exact
run label, removes containers before volumes, and verifies both lists are empty.
Each cleanup call is bounded; any cleanup error fails the run and is retained in
`image-identity.json`. The runner never uses Docker prune or deletes images.
SIGKILL and host power loss cannot execute Python finalizers: use the recorded
run ID to inspect/reconcile only that invocation's labels after such a failure.

Released image selection is a separate maintainer prerequisite. Record the
approved registry digest, independent publication workflow/release URL, source
commit and platform, plus the release's provenance verification receipt with
the acceptance evidence. Docker `RepoDigests` and the Codex checksum authenticate
the selected local image and binary; they do not establish who published it.
A server release image is not a native-worker image. Do not build this worker in
the acceptance job or treat a locally built substitute as release acceptance.
If a suitable released native image or reviewed host policy is unavailable,
leave #2952 open and do not activate a required CI check.

For repeatable synthetic cleanup receipts, preload `alpine:3` and run:

```sh
CREWSHIP_NATIVE_CLEANUP_DOCKER=1 python3 -m unittest discover \
  -s scripts/ci -p test_native_runtime.py -v
```

The two opt-in Docker regressions start isolated, network-disabled Alpine
containers with owned volumes, interrupt a process group via timeout/SIGTERM,
confirm both owned resource kinds were removed, and confirm a separately
labeled volume survived. Each test cleans its own resources in a finalizer.
These receipts exercise cleanup against Docker; they do not execute Codex or
prove the named native live contract. Ordinary runner unit tests use doubles.
