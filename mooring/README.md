# Mooring Rust workspace

Application-independent collaboration code. This workspace is currently unused
by Crewship, so it does not start a service or change the deployed application.
The Go host remains the authority for permissions, fencing, durable writes and
receipts. Draining a Rust batch never means the changes have been saved.

`mooring_core::adapter::PrepareAdapter` is a pure application boundary: validate
an opaque checkpoint and prepare one normalized immutable atomic change from a
restored/forked authoritative state. Its candidate owns a full checkpoint and
application state. There is no installation, publication, receipt or outcome
ledger API. The caller bounds input before restore and output after serialization,
then waits for verified host confirmation. An adapter's failed member must pass
`PreparedFailure::qualify`; foreign/ambiguous members and uncertain outcomes
remain nonterminal errors instead of guessed persisted rejections.

`mooring-core` begins with bounded batches of atomic changes: 100 ms from the
first operation, 256 operations, or 256 KiB, whichever comes first. These are
initial defaults, configurable by the runtime. Oversized atomic changes return
to the caller intact. The runtime must schedule a timer at `deadline()` and send
batches returned by `push` or `take_due` immediately. Transport envelope bytes
must be reserved when configuring the batch byte limit. This primitive does not
claim end-to-end latency or durable storage guarantees.

From the repository root:

```sh
crewship-heavy cargo test --manifest-path mooring/Cargo.toml --config mooring/.cargo/config.toml --locked --offline
crewship-heavy cargo clippy --manifest-path mooring/Cargo.toml --config mooring/.cargo/config.toml --all-targets --locked --offline -- -D warnings
crewship-heavy cargo build --manifest-path mooring/Cargo.toml --target wasm32-unknown-unknown --config mooring/.cargo/config.toml --locked --offline
```

Registry dependencies are pinned by the lockfile and vendored with license
texts; see the dependency inventory below.

`mooring_core::lease::DeliveryGate` is a fail-closed primitive for delivery
checks. A verified host response grants time only from the renewal request's
send timestamp. Revocation permanently closes the gate; late responses cannot
reopen it. Native runtimes must obtain timestamps from `lease::boottime()`;
this uses 64-bit Linux `CLOCK_BOOTTIME` and returns an unsupported error elsewhere.
Tests advance a simulated suspend-aware clock; they do not suspend the OS or
process. They simulate response delay, but do not prove every runtime
content path checks the gate. The caller must bind each gate and renewal reply
to one authenticated participant and session.

`mooring-text` pins Yrs to exactly 0.28.0 (MIT). All registry dependencies,
including target-specific and proc-macro crates, are vendored unchanged with
Cargo checksum manifests. `licenses/inventory.json` records every locked
package's SPDX expression and license text hashes. The Yrs crate omits its
license file; the identical upstream release's MIT text is supplied separately
with its pinned commit provenance. r-efi's full permission text is in `AUTHORS`;
we select its MIT option. No proprietary service is required.

Run from the repository root with the vendored source configuration:

```sh
crewship-heavy cargo test --manifest-path mooring/Cargo.toml --config mooring/.cargo/config.toml --locked --offline
python3 mooring/scripts/license-inventory.py --check
```

The `--check` path verifies both local file hashes and every vendored Git-index
blob, so ignored upstream build sources cannot disappear from a clean checkout.
When updating dependencies, use `cargo vendor --locked --versioned-dirs` with
the workspace manifest, regenerate the inventory, and stage every checksum-listed
source before running `--check`. Repository ignore rules may require exact
`git add -f -- PATH` for legitimate upstream build/test files; never alter their
Cargo checksums to omit them. Unexpected indexed vendor files also fail.

Text input accepts one bounded Y-sync envelope; readers cannot send SyncStep2
or Update. Raw Yjs auth, custom messages and awareness are rejected; presence
must be bound to verified session identity separately. This is an input filter,
not a durable commit endpoint. Rejection recovery constructs a fresh document
from a caller-verified, host-authenticated and size-bounded authoritative
snapshot and retains pending text as unsaved drafts.
It does not infer Yjs dependencies, automatically replay intents, or implement
the WordCraft adapter. UTF-16 offsets match browser Yjs indexing.

`mooring-core::prepare::ConfirmedContext` qualifies the complete host target
context against the application's restored checkpoint inventory before classifying
read failures. It validates host prospective labels in immutable operation order;
it never allocates revisions. Its working view first checks the original confirmed
read set, then applies a complete validated effect set atomically using those
labels. Malformed host context is nonterminal. An unknown read target is
`invalid_request`, a retained tombstone is `target_deleted`, and a mismatched live
counter is `precondition_failed`, attributed to the actual operation. This initial
`agent_confirmed_current` capability rejects causal stale and provisional reads;
the shared context fixtures qualify only this pure intersection, not host commits
or the remaining broader collaboration modes.

The separate pure `causal::CausalContext` API consumes compact host-qualified
original reads and checks every earlier atomic member effect against immutable
dependencies. It does not change `agent_confirmed_current` or enable a transport
profile. The trusted SQL host must qualify the entire retained history, including
zero-effect dependency connectors and every intervening target effect, before
constructing these non-deserializable values. Missing history is nonterminal.
The shared causal vectors distinguish host SQL cases, pure Rust cases and the
optional-null wire negative; they are not live authorization/convergence proof.

The native WordCraft causal runtime composition uses the application's registered
`0.4.0 / 0.2.0 / 0.10.0` tuple and its bounded model-v2 checkpoint decoder.
It offers strict-current or causal preparation only when that exact private
profile is explicitly selected on the inherited host channel. Original reads
remain immutable, and returned candidates remain uninstalled. This composition
adds no human-delta capability, participant endpoint, ACL or Saved authority.

`mooring-core::session::Session` is the wire-independent state for a future
single-owner actor. It owns the confirmed application document separately from
queued and outstanding batches, with aggregate operation/encoded-byte limits
and a bounded outstanding-batch count. Another batch can start before a durable
acknowledgement. Only ordered, externally verified host responses can replace
the document; an uncertain transport outcome closes further sends/installs and
returns every original body for status-based recovery. This state primitive
does not verify receipts, restore checkpoints, drive timers, authenticate gates
or advertise a live session transport capability.

The unused native `actor::Actor` composes that state with the reviewed scheduler.
It retains at most four outstanding 256-operation/256KiB batches plus the
scheduler's bounded 256-operation/256KiB unsent queue. A conservative envelope
reserve reduces each batch payload allowance; the actual wire encoder must
verify its output against the charge before I/O. Native timer and acknowledgement
waits remain separate. This composition still has no FD dispatcher, participant
admission, owner/read gates or mixed-author host submission authority.

The live-session composition carries the qualified atomic/dependency resolver,
including complete SCC membership and first-submitted actual cycle roots,
admission-failure precedence and original per-operation rejection roots. Its
public thirteen-case shared fixture consumer compares planner decisions only:
`EligibleForHost` is not a durable receipt or authorization. The later Unicode
scalar identifier policy remains enforced; no application/store authority moves
into the planner.

The inactive native-session composition includes the host-grant binding helper
and stateful read-lease controller: `grant.rs`, `delivery.rs` and their original
fixtures/tests retain the previously reviewed source. A request keeps its first
send clock across retries; nonce ownership and the authenticated host identity
remain bound, and late replies cannot reopen a revoked/expired gate. Actual
integration still must provide trusted channel/admission provenance, keep one
controller per fresh registered gate and check it immediately before every
participant frame/chunk. No production caller or session profile selection is
introduced by this composition.

`session_startup` composes the inactive session transport with an actual owned
WordCraft document. Startup consumes one host admission per fresh runtime ref,
retains the first-send owner request across retries, binds OwnerGrant to the
pending SessionOpen, and restores only the exact checkpoint/inventory matching
its scope, generation, revision, application tuple and checkpoint format.
Restoration checks the trusted clock before and after work; each startup reply
checks ownership again after encoding and writer wait. Tests use real Unix
frames and the real application document with an injected deterministic clock.
No CLI selects this profile, no participant join or mutation is admitted, and
caller-trusted transport metadata still needs the actual inherited-FD factory.

### WordCraft native serialization qualification

The application adapter alone links `wordcraft-doc` and `wordcraft-geom` from
public WordCraft revision `668d7ce3a4b1dcf241a9c50a20754a85647a02c4`.
The crates are vendored unchanged and the inventory records that immutable Git
source, every source-file checksum and the selected MIT license text. They do
not enter the generic core, protocol or minimal text client dependency graph.
The WordCraft application crates require Rust 1.95 or later; the current
qualification toolchain is 1.99.1.

Writer qualification compares the entire original native value with serialization
through that exact typed document model. The disposable typed value runs the
same paragraph normalization and nonempty-document default as native JSON open;
any resulting change refuses writer qualification. Unknown fields, inserted defaults,
non-exact f32 conversions and integer type mismatches make the whole document
read-only. The predicate never replaces the original native value or checkpoint.
Actual WordCraft command validation remains separate: typed serde equality alone
does not prove a UI command's effects or establish host writer authority.

`d1-native.json` remains the original raw-source fixture. Its three known f32
values are not rewritten on restore; it is now explicitly read-only.
`d1-engine-canonical.json` is a separate fixture produced by opening that source
with the qualified actual engine, and initializes writable connector tests.
Existing projected checkpoints that no longer qualify retain their actual CRDT
roots and stable IDs read-only, subject to full shape and native equality checks.
There is no silent migration or numeric tolerance.
