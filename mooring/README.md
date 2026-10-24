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
snapshot and retains pending text as unsaved drafts. Full snapshot recovery also
checks that every encoded allocation/GC and deletion range is covered by the
restored frontier; an apparently successful upstream apply cannot silently
drop a missing same-origin prefix. Failure retains all original drafts. Scope,
authority and bounded-input qualification remain the caller's responsibility.
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
These startup tests alone admit no participant mutation; the production
selection described below still requires the inherited-FD host factory.

The native `session_driver::Driver` composes the single-reader lane
with owned-document startup. A separate preparation worker binds every request
to the exact opened workspace/artifact and application tuple, so a host prepare
callback can progress while actor restoration is withheld. Preparation creates
independent owned candidates and never installs them into the retained live Doc.
Startup polls the send-bound owner lease while waiting for restore, checks it
around restoration and both acknowledgements, and closes/joins the worker on
failure. Worker unwind also closes the channel. The driver retains the original
checkpoint wrapper and actual document. The driver handles startup/ownership;
its consuming actor handles participant controls. The injected clock and host
transport assertion must come from verified native integration. Tests use real
Unix socket pairs plus an injected clock; they do not qualify suspend behavior,
production owner renewal, author admission, durable settlement or distribution.

`session_startup_peer` is an explicit integration example, not the production
`mooring-collab` entry point. It selects only the offered session profile,
retains FD3 close-on-exec, uses native CLOCK_BOOTTIME, restores an actual owned
WordCraft Doc and serves scoped stateless prepare callbacks. It has no
participant, submission or author-permission implementation. Its logical host
mode is fixed to `crewship`/node solely for this isolated host-transport proof.
Production session selection is described below; this example remains a test
fixture. The composition includes the core-metadata readonly repair and native
compatibility regressions; broader typed native-engine losslessness remains a
separate activation requirement. No human draft APIs or Cargo changes are added.

Owned sessions retain lease-only interior metadata independently of the
immutable confirmed application document. The owner controller generates a
monotonic nonce sequence in its fresh runtime's namespace, keeps at most one
PendingGrant, preserves its original send time on retries, and permanently
closes on expiry, clock regression or nonce exhaustion. Accepted owner epochs
advance a monotonic knowledge floor; old participant versions fail its matching
prerequisite immediately. Matching is not a participant read/write grant and
never opens a closed delivery gate. Informational revisions in OwnerGrant do
not install checkpoints or invent Saved outcomes. The initial grant getter is
identity/fence metadata; current permission knowledge has its own accessor.
The event-loop renewal and participant-gate wiring remain separate work.

The unselected native settlement primitive consumes acknowledged startup once
into a non-cloneable owner controller and one owned confirmed WordCraft document.
The actor can replace that document only after the closed session receipt verifier
and complete application checkpoint/inventory restoration succeed. Fresh saved
revisions must cover the entire advancement in immutable batch order; a rejected
or already-confirmed result keeps the original checkpoint identity. Invalid or
uncertain replies close owner metadata and require the driver to recover all
original intents. Actual FIFO request correlation and participant delivery gates
remain driver obligations. The original opaque startup/settlement proof used content SHA256; that historical
proof does not qualify generic full-tuple checkpoint identity. No live transport or writable application profile is enabled here.
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

### Explicit checkpoint identity capability

The unexposed session profile now requires the closed `checkpoint_identity_v1`
capability in SessionOpen and echoes it in SessionOpened. Old peers fail before
owner acquisition rather than silently interpreting a content hash as a full
checkpoint identity. Existing strict/stateless profiles and public frame limits
are unchanged. These corrected session controls are not an enabled CLI or API.

`session_describe_checkpoint` and `session_checkpoint_described` run on the
independent bounded prepare lane, including while startup awaits an owner grant.
Their opaque input digest binds exact content; the concrete app must separately
prove its format, semantic tuple and COMPLETE external-asset manifest. Unknown
references and unsupported checkpoints fail nonterminally by closing the private
channel; no native diagnostic or content is sent as an error. These two named
controls alone obtain the existing 2MiB raw/canonical bound and share the lane's
aggregate 128-entry/4MiB budget. Final encoded output must fit; decoded-component
limits do not guarantee an enclosing frame fits.

Startup and host settlement verify the generic full-tuple checkpoint digest,
then require application-qualified metadata and the complete target inventory
before retaining one owned document. Creator provenance stays distinct from the
current owner fence. Opaque preparation still uses raw-content hashes. The first
app descriptor supports closed, self-contained engine-canonical D1 only; old raw
checkpoints and unsupported documents are not rewritten or assigned an empty
manifest. Broader document portability remains qualification work.

Native socket tests prove descriptor progress before OwnerGrant, full identity
restore/settlement, original creator retention on takeover, and rejection of
missing capability, content-only hashes, relabelled provenance and forged assets.
They use injected clocks and trusted host fixtures. Retained SQL metadata,
authorized writer-bound full-to-content conversion, actual Go startup, live FIFO,
participant gates and provider/human admission must be composed separately before
enablement. The earlier pinned Go/Rust opaque proofs remain historical evidence.

### Native owner maintenance

The session driver renews its owner lease from the original request
send time, retries the same pending nonce, and checks the BOOTTIME owner guard
after waiting for the shared writer. Preparation remains on its independent
lane. A single cached canonical reply permits exact duplicates without extending
the deadline; changed, unrelated or late replies close the driver. Owner epoch
knowledge may advance, but participant gates and the confirmed document do not.

Deterministic clock and real socket tests cover expiry, retry timing, duplicate
replies and preparation while a renewal reply is withheld. This is owner-only
maintenance evidence only. Canonical participant authority and final delivery
still require the Go host; actual Go/SQL renewal qualification is a separate
integration step.

### Explicit read-only origin inspection

The separate native `from_negotiated_inherited_stream_with_origins` constructor
can acknowledge an explicitly requested `origin_inventory_v1` capability. The
existing constructor remains identity-only and refuses that request before
OwnerAcquire. Preparation handles inventory independently while startup awaits
an owner and after the live document is restored. Required capabilities are
echoed in their original order; identity-only sessions cannot request inventory.

The handler verifies the full checkpoint identity and actual application
description before inspecting every occupied origin. It returns sorted numeric
metadata bound to exact scope/session/runtime/tuple/full basis and the host's
current fence. Creator provenance remains separate. The echoed fence is host
correlation metadata, not native proof of SQL ownership. Unknown, legacy and
read-only inventory qualification fails nonterminally without content diagnostics.

Actual socket tests preserve the owned document's complete native value, stable
IDs and checkpoint bytes while inspecting before/after startup. They also cover
identity/full-metadata transplants and the actual read-only D3 refusal. This does
not reserve origins, construct writers, issue Saved or prove participant rights.
A concrete opaque Go result with original-source and
owner checks in the same writer remains the required host integration.


### Live actor readiness notification

The opt-in consuming actor driver uses an opt-in payload-free thread wake
for both scheduler readiness and actual actor-lane arrivals/closure. It rebinds
that wake before checking work and parking, so a wake between check and park
remains pending. No synthetic protocol frame or extra polling thread is added;
prepare routing, queue byte/count accounting and final owner/delivery checks
remain separate. The existing default core scheduler/actor keeps its100ms
setting. This live driver flushes at40ms to leave validation/encoding headroom
within the unchanged100ms maximum; a full operation/byte batch remains immediate.

The native measurement test covers1/32/255/256 operations and a240KB text
change, three fresh sessions each, withholding all host ACKs. It records actual
input-frame write to complete batch receive. Timing results depend on build
profile and host load: optimized dev2 measurements are evidence for that bounded
matrix, not a real-time guarantee under OS suspension or socket backpressure.
Debug builds can exceed100ms during bounded validation. Idle actor waiting keeps
the existing20ms owner-maintenance interval; readiness does not add1ms polling.
These tests still use mock host authority/history/receipts and do not qualify
production ingress, SQL, source-origin issuance or participant distribution.


### Production private-FD profile selection

The packaged `mooring-collab --rpc-fd 3` binary selects the owned session actor
only when the inherited host explicitly offers the single closed
`mooring.session-causal.v1` profile and exact qualified WordCraft tuple.
Mixed session/stateless offers, ambiguous tuples and malformed/incompatible
session declarations fail before readiness; there is no silent fallback.
Existing offers without a session profile keep their prior no-profile,
strict-prepare or causal-prepare behavior.

The session branch uses the same inherited socket, single reader, independent
prepare lane, actual CLOCK_BOOTTIME, owned application document, owner renewal
and readiness-woken actor. It opens no listener and depends on no example/mock.
Go `StartSessionRuntime` remains disabled unless its explicit Enabled config is
set; this Rust entrypoint does not add a route, origin issuer or permission.
Participant input still requires the host's original sealed submission,
canonical current rights, durable SQL decisions and independent final send
fences. Unsupported actor controls and uncertain results close the channel with
owned pending intents retained. Large snapshot distribution, complete task/read
handling and human-origin admission remain separate qualification work.


### Participant delivery maintenance

The actor schedules participant delivery renewal at one-eighth of the actual
host duration (at most250ms), anchored to the original request-send time.
There is one pending nonce per original ReadLease; retries reuse exact bytes.
A delayed accepted reply can make the next renewal immediately due. One exact
completed canonical reply is harmless and cannot move the deadline, raise
permissions or replace a different pending nonce. Retained encoded renewal/cache
bytes are charged to the existing4MiB participant metadata budget.

The core ReadLease and2s maximum are unchanged. Owner/permission/read-gate
checks run again after writer wait. A reply arriving after the CURRENT gate
expired cannot reopen it even if the reply's own request window would still fit.
Owner permission advancement revokes stale participant gates; it does not grant
new rights. Preparation continues independently while renewal is pending.
Deterministic-clock actual-socket tests cover delayed replies, retry/duplicate
binding, eight transplants, permission advancement and strict expiry. Actual Go
SQL renewal and retained terminal-delivery/status recovery are separate paired
qualifications; this slice alone does not repair dropped receipt recovery.

### Retained terminal delivery

After validating an actual correlated host receipt and complete checkpoint, the
actor reserves receipt bytes and recipient metadata in the same aggregate
128-control/4MiB budget used by both inherited inboxes. Reservation precedes
snapshot installation and removal of original pending intents. Capacity failure
closes the session with those originals retained for host status recovery; it
never creates a terminal rejection or truncates a Saved result.

Verified terminal receipts remain FIFO for each stable participant while an old
read gate is expired or revoked. A separately authenticated new gate for that
same participant can receive them after current owner, permission and ReadLease
checks, repeated after writer wait. Another participant cannot inherit them.
No old gate is revived and one blocked author does not block independent
recipients. Dropping delivered entries releases their shared reservation, even
when the transport has already closed. Process loss still requires current
host-authorized status recovery; retained memory is not a durable outcome log.
Large receipt chunking and the native status path remain separate work. Tests
use actual inherited sockets with deterministic clocks and mock host authority;
actual slow FULL-SQL renewal/delivery remains a paired integration check.
