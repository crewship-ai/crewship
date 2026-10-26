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

The candidate command and parameter inventory comes from the generated WordCraft
catalog, copied verbatim into `src/capabilities.json`. Refresh it from the reviewed
catalog tree and verify both copies of the bounded schema validator:

```sh
crewship-heavy python3 tools/wordcraft-c1-engine/catalog_snapshot.py --source-root "$CATALOG_SOURCE"
crewship-heavy python3 tools/wordcraft-c1-engine/catalog_snapshot.py --check --source-root "$CATALOG_SOURCE"
```

The helper preparation already copies `src/`, so no build-time external checkout
is required. Offline `--check` verifies snapshot/vector hashes and host/helper
validator equality. The canonical registry generator still owns command discovery;
this tool never writes IDs or schemas by hand. Provenance records the canonical
catalog commit and hashes. Engine identity includes catalog version, registry
digest and copied catalog digest so checkpoints cannot cross catalog versions.

Only closed, exact schemas for document candidates in the four C-E groups can be
probed. Process-global, unknown, unresolved and non-document commands are denied.
Objects, arrays, numbers, integers, booleans, null, enum and anyOf use a bounded
schema subset; unknown keywords and patterns fail closed. Three known anchored
pattern forms (hex color, bounded printable ASCII and base64) are evaluated
without adding a regex/runtime dependency. All 44 source profiles have positive
and negative examples exercised in both host and actual helper unit tests.

This is **candidate admission for the default-off isolated qualification host**,
not qualification or a product permission grant. The catalog's product flags stay
unqualified. Production integration must require the qualified catalog and trusted
participant permissions before dispatching; this experiment remains unsuitable
as a standalone authorization boundary. The only special command branches are
host controls (`init`/read/participant undo) and reviewed targeting/effect guards.
Single-block text insertion retains its structural guard. Targets still use
stable host IDs and UTF8 byte positions at grapheme boundaries. Existing Enter,
backspace/delete stable-ID mappings, unknown-structure refusal, P/K, admission and
participant undo are unchanged. No pending formatting rebase is introduced.

Each candidate loads P and performs layout; timings measure the cold M2 reference
path. Catalog admission alone cannot establish semantic effects, P restoration,
participant-only undo, storage or browser behavior. The complete actual matrix
must prove those properties per fixture before engine qualification is recorded.

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
