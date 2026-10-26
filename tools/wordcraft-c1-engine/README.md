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
Preparation produces one candidate K from confirmed P. `stage_commit` builds an
opaque `StagedCommit`: immutable candidate checkpoint bytes and a candidate
receipt, without changing P, participant undo or confirmed operation history.
The checkpoint includes the future confirmed operation receipt so a restart after
an actual SQL commit but before Rust ACK can return a known operation without
engine execution. Its read-only metadata includes document/generation, base and
next revision, engine identity, operation fingerprint and checkpoint digests.
`canonical_checkpoint_digest` identifies the exact last SQL blob, even when a
restore rotates the in-memory generation; the next SQL compare-and-swap must use
that base identity. Digest getters use the `sha256:<lowercase hex>` format.

Only after the host has actually committed those exact checkpoint bytes AND the
receipt in one canonical SQL transaction may it call `confirm_committed`. The
callback rechecks fresh writer admission, original participant/author/epoch,
original candidate and the entire confirmed-base digest before K becomes P.
This trusted callback is not decoded from user JSON; it does not itself perform
or prove SQL. There is no direct `resolve(Committed)` shortcut. Tests explicitly
simulate this callback and must not be presented as SQL durability evidence.

A definitive `reject` discards K while retaining confirmed state and participant
undo. A SQL error/unknown outcome must not invoke success or rejection: retain K,
block successor mutations, and look up the original operation/canonical SQL
checkpoint. Do not persist an unconfirmed K in another journal or blindly replay
it. A failed final ACK likewise retains P AND K because SQL may already have
committed; canonical reconciliation belongs to the product host. The
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


## Private native host channel (default off)

The optional `mooring-wordcraft-command-host` binary requires the existing
`wordcraft-command-experiment` Cargo feature. It accepts only a connected,
inherited AF_UNIX stream at FD 3 (`MOORING_C1_HOST_FD=3`), not a listener or a
public endpoint. Trusted startup arguments select `--engine` (an absolute
artifact path), `--engine-sha256` (`sha256:<hex>`) and `--runtime-ref`. The helper
ELF bytes are checked before each spawn; the engine also verifies the existing
source/patch/catalog identity. Artifact paths belong to the trusted host and
must remain immutable while selected for execution.

Frames are a four-byte big-endian length and UTF-8 JSON, at most 32 MiB. A frame
in progress and writes have bounded total deadlines; malformed/unknown fields
or protocol/correlation syntax close the channel without replay. Requests are a
closed `kind` enum: `init`, `restore`, `stage`, `confirm`, `reject`, `status`,
`projection`, all with `protocol:1` and a bounded `request_id`. No app command is
added. `checkpoint_json`, `receipt_json`, `input_json` and `native_json` are JSON
**strings**; the host must preserve exact checkpoint/receipt bytes.

Only the trusted Go SQL bridge may send admission and confirmation. A retained
opaque `stage_ref` identifies one immutable candidate; possession of that string
by a public caller is never authorization to commit. Confirm is exclusively the
callback after the exact SQL checkpoint+receipt transaction commits. Unknown SQL
outcomes retain K and require SQL reconciliation. Restore accepts canonical SQL
P in a fresh generation, including as the first call, and does not rewrite SQL.
Status and projection are pure DATA: they neither execute engine commands nor
establish delivery rights or Saved. A `no_candidate:true` error is allowed only
when the stage validation was observed before any engine call and K is absent;
engine errors and uncertain outcomes are conservative refusals without that
proof. RPC process tests use actual native engines but simulate SQL callbacks;
real SQL integration must be verified by the product bridge separately.


### Private C1 JSON profiles

The Go native port validates checkpoint JSON with the existing 4 MiB product
limit before canonicalizer allocation. Private RPC frames retain the existing
32 MiB limit so quoting a valid checkpoint does not apply an unrelated HTTP
envelope budget. Both use RFC 8785 JCS with strict UTF8 and duplicate-key
rejection, depth at most 64 and at most 1,048,576 structural delimiters outside
strings (`{`, `[`, `,`, `:`). The canonical checkpoint bytes used for digest,
SQL storage and compare-and-swap are never replaced by the validator's output.
The legacy operation-envelope canonicalization remains unchanged at 1 MiB and
32,768 structural delimiters; app-command input limits are also unchanged.
