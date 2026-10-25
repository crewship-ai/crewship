# Isolated WordCraft C1 experiment

This opt-in reference host is compiled only with the Linux
`mooring-collab/wordcraft-command-experiment` feature. Default product builds do
not include the host or build this helper. It does not replace Yjs, expose an
endpoint, persist a journal, commit SQL, or claim a Saved state.

The helper uses the public WordCraft revision and reviewed author/clock patch
in [source.json](source.json). Preparation verifies the original and patched
source hashes, the patch digest and the upstream MIT notice. The engine and its
dependencies live in a separate workspace, outside the product vendor tree.

Requires Git, Python3, Rust1.95+ and the pinned engine's platform build tools.
Choose new owned output/target directories and run from the repository root:

```sh
crewship-heavy python3 tools/wordcraft-c1-engine/prepare.py --output /absolute/new-prepared
CARGO_TARGET_DIR=/absolute/engine-target crewship-heavy cargo build --release --locked --manifest-path /absolute/new-prepared/helper/Cargo.toml
CARGO_TARGET_DIR=/absolute/engine-target crewship-heavy cargo test --release --locked --manifest-path /absolute/new-prepared/source/Cargo.toml -p wordcraft-engine --lib c1_host_context_tests
MOORING_C1_ENGINE_BINARY=/absolute/engine-target/release/wordcraft-c1-engine CARGO_TARGET_DIR=/absolute/product-target crewship-heavy cargo test --manifest-path mooring/Cargo.toml -p mooring-collab --features wordcraft-command-experiment command_experiment -- --nocapture
```

The final command's filter selects the process unit test. Run the actual host
suite explicitly as well:

```sh
MOORING_C1_ENGINE_BINARY=/absolute/engine-target/release/wordcraft-c1-engine CARGO_TARGET_DIR=/absolute/product-target crewship-heavy cargo test --manifest-path mooring/Cargo.toml -p mooring-collab --features wordcraft-command-experiment --test command_experiment -- --nocapture
```

A missing helper is a failure, never a skip. C0 remains an unchanged failing
baseline for the unpatched engine.

`Admission` is a trusted Rust input, not user JSON. The JSON boundary refuses
client authority, duplicate/unknown fields, stale revisions, unsupported targets
and commands. The host stamps author/time before the actual engine command.
Preparation produces one candidate K from confirmed P; a separate trusted
completion requires fresh writer admission and the original permission epoch.
Rejection discards K while retaining confirmed state and participant undo. The
ephemeral monotonic issued-ID counter burns IDs without changing the confirmed
checkpoint. Rejected operation receipts are bounded, live-host tombstones outside
that checkpoint; committed receipts survive restoration. Requests explicitly carry
an original generation, bound into their `generation:local-op-id` identity and
fingerprint. After restart, known committed exact retries return their receipt;
unknown old-generation operations fail closed with `stale_generation`, without
creating a candidate. They are never executed again, even though ephemeral
rejected receipts are not restored. Changing only the generation while preserving
the operation identity is refused. A new generation and a new operation identity
are an explicit new operation, not a retry. Completion is a host experiment result, not SQL
persistence. A fresh trusted generation is required after checkpoint restoration.

The ten commands are text insert/delete/backspace/newParagraph, bold/font/size,
paragraph alignment, document read and participant undo. Targets use stable host
block IDs and UTF8 byte positions at grapheme boundaries. Undo uses bounded host
preimages and refuses overlap; it never invokes engine global undo. Exact base
revision matching deliberately provides no rebase or preservation of pending
formatting. Each candidate loads P and performs layout: timings measure this
cold M2 reference path, not an interactive performance promise.

Bounds:64KiB user input,4MiB native/frame,4096 blocks, one candidate,32 undo
records with a16MiB aggregate cap,256 operation IDs per host,32 generations and
32MiB checkpoint input. Operation records are not silently evicted. The private
helper has a2GiB address-space and60CPU-second cap; socket read/write limits are
5s/1s. A transport/protocol error kills and reaps it and permanently refuses
reuse; a fully framed engine refusal is recoverable. The FD becomes CLOEXEC in
the child. It has no listener and receives a cleared environment. These are
bounds and process separation, not a filesystem/network sandbox. RSS and command
p95 are measured;300MB/300ms are informational goals only.

The selected engine MIT notice is retained. Dependency license declarations and
sources are reproducible with Cargo metadata. Their presence does not establish
a complete license audit or an attribution bundle for distribution; that remains
separate qualification. Production authority/SQL integration, peer projection,
UI, deployment, crash recovery and GateC are outside this experiment.
