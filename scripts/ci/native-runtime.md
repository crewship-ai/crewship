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

`image-identity.json` records image mapping, binary checksum/version and final
status. `go-test.jsonl` preserves execution evidence once the Go invocation
starts. The Python regression suite uses test doubles and proves the runner
contract; passing those regressions is not evidence that live native or crew
execution passed. Actual acceptance remains pending until an approved released
image and suitable host are supplied.

Activation prerequisite: the Go outer timeout currently terminates its parent
process; descendant processes and runtime-owned Docker containers or volumes
can outlive an interrupted run. Before enabling CI or treating this draft as a
ready acceptance gate, add and verify bounded process-group termination and
cleanup restricted to resources owned by that run, including timeout and
cancellation regressions. The probe container cleanup is already scoped to its
unique created name when Docker returns malformed create output, but it does
not provide cleanup for resources created by the Go live test.
