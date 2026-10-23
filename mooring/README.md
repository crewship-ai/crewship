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
