# Private runtime bootstrap

`mooring-collab --rpc-fd 3` runs only on Linux with an already inherited connected
Unix socket. It never binds a listener. The Go host owns process lifetime,
selects the packaged binary, strips backend credentials from the environment,
and supplies descriptor 3. No participant credentials or content are accepted
on the command line.

Each frame is a four-byte big-endian unsigned byte count followed by UTF-8 JSON.
Raw lengths outside 1..262144 are rejected before allocation. Truncated, invalid
UTF-8 and malformed JSON frames terminate the connection. Bootstrap has a
five-second absolute deadline, including fragmented reads. Concrete Hello
parsing rejects unknown and duplicate fields; an exact supported version tuple
returns RuntimeReady with the same request_id. Readiness carries no authority,
scope, permission, content or durable receipt.

This initial child implements bootstrap only. EOF stops it cleanly, and every
post-bootstrap message closes the channel. It has no dispatcher, participant
state, mock authority, application adapter, durable log or Saved issuer. A future
content boundary must add schema limits, binary64/JCS normalization, host-bound
scope and identity, owner fencing and delivery lease checks before dispatch.
Syntax validation here is not a full protocol validator.

From the repository root:

```sh
crewship-heavy cargo test --manifest-path mooring/Cargo.toml --config mooring/.cargo/config.toml --locked --offline -p mooring-collab
crewship-heavy cargo build --manifest-path mooring/Cargo.toml --config mooring/.cargo/config.toml --locked --offline -p mooring-collab --release
```

Tests use real Unix socketpairs for fragmentation, EOF, exact readiness,
unsupported versions, duplicate/unknown fields, post-bootstrap refusal and
bootstrap timeout. These are transport/bootstrap checks; they do not establish
browser collaboration or end-to-end authority enforcement.

The unselected session library also exposes a consuming `Driver::into_actor`
transition. It transfers the acknowledged owned document into the bounded core
actor while retaining the original FD, prepare worker, owner nonce history and
first-send renewal deadlines. `RunningDriver` exposes only immutable confirmed
state, owner maintenance and recovery; participant admission, timer dispatch
and verified receipt settlement are not connected by this transition. A bare
restore or renewal reply cannot replace the actor's confirmed document.

The separately invoked `RunningDriver::step` connects correlated host ticket
replies to host-configured read leases (default five seconds, at most five seconds), exact participant input bindings, immutable
`submit_changes`, the native core timer and FIFO host settlement. B may leave
while A awaits its durable reply; an earlier sent dependency stays pending.
Only a verified `SessionCommitResult` installs the host checkpoint. Uncertain
closure, foreign members and duplicate old receipts retain unresolved originals
for recovery. Participant output runs its gate after encoding and writer wait;
the Go conduit must independently enforce its final recipient gate.

This initial actor path remains unselected by the production CLI. Tests use
mock host authority/history/receipts and clocks with actual native timers and
the actual WordCraft adapter/document. It does not qualify host-issued origins,
SQL durability, task/proposal input, FIFO status controls, delivery renewals or
large checkpoint/receipt chunk distribution. Unsupported controls close all
lanes without manufacturing an outcome; these paths must be connected before
service enablement. The current 20 ms inbox poll can add dispatch jitter beyond
the core timer's 100 ms deadline; end-to-end latency is not yet qualified.
# Whole-document binding preparation experiment

The default-off Cargo feature `wordcraft-binding-experiment` selects
`experimental_binding::BindingAdapter`, inactive application preparation for
the separate WordCraft binding checkpoint format. It consumes a bounded raw Yrs
update inside the existing strict `PrepareAdapter`/private-dispatch contract.
It does not execute engine commands. Callers must gate engine commands with
external effects before execution; validating a resulting document cannot undo
file, network, font-loading or macro effects.

This is **untrusted DATA preparation for locally generated fixtures**, not a
live-admission capability. Neither numeric origin nor declared basis proves
authorship, allocation ownership or permission to modify another author's
formatting history. Those proofs must be integrated before participant updates
can be accepted. The basis envelope preserves producer data; it does not attest
the original authoring checkpoint.

Only explicit host `WriteScope::WholeDocument` configuration is accepted. Scoped
block mandates are refused. Every update conservatively requires current reads
and emits content effects for **all live targets**, including unchanged blocks
and document metadata/structure. New/deleted identities and retained tombstones
are reported separately. These are conservative fences, not precise affected
block attribution: equal-value formatting can change CRDT history without
changing native JSON. Precise deep-event tracing is required before narrower
mandates can be supported.

This adapter accepts one operation per atomic change and uses strict current
read observations for every live/reserved target. It rejects stale concurrent
text submissions even when the standalone binding could merge their Yrs data.
The existing target-effect grammar cannot resurrect a tombstoned identity, so
structural Undo requiring resurrection is unsupported. This is not a qualified
collaboration session or complete Undo implementation.

Candidate creation currently restores/copies the full checkpoint, scans complete
inventories, validates an isolated binding clone and serializes a full candidate;
the generic dispatcher revalidates that candidate. These correctness checks have
not been qualified for interactive latency or process-memory growth.

The adapter grants no participant authority, origin ownership or durable status.
The host must authenticate/authorize the whole document, bind the input to its
exact scope/change, reserve CRDT origins, and commit before any Saved receipt.
Production registration, the production executable and session profiles do not
select this adapter. Production checkpoints remain a separate format.

The explicit `binding_prepare_host` example implements the inherited-FD entry
contract and delegates framing, negotiation and preparation to the existing
generic private dispatcher. `binding_prepare_fixture` generates local inputs
from the pinned native fixture. To run the separate Go process/SQL regression:

```sh
crewship-heavy cargo build --manifest-path mooring/Cargo.toml \
  --config mooring/.cargo/config.toml --locked --offline -p mooring-collab \
  --features wordcraft-binding-experiment \
  --example binding_prepare_host --example binding_prepare_fixture
fixture_dir=$(mktemp -d)
crewship-heavy mooring/target/debug/examples/binding_prepare_fixture "$fixture_dir"
binary="$PWD/mooring/target/debug/examples/binding_prepare_host"
crewship-heavy env MOORING_TEST_RUST_BINARY="$binary" \
  MOORING_TEST_RUST_SHA256="$(sha256sum "$binary" | cut -d ' ' -f 1)" \
  MOORING_TEST_WORDCRAFT_CHECKPOINT="$fixture_dir/checkpoint.json" \
  MOORING_TEST_WORDCRAFT_PAYLOAD="$fixture_dir/payload.json" \
  MOORING_TEST_WORDCRAFT_READS="$fixture_dir/reads.json" \
  go test -tags mooring_binding_experiment ./internal/mooring \
  -run '^TestBindingPrepareActualRustSQLReceiptAndRetry$' -count=1
```

This regression uses the existing SQL store, which still saves a complete
checkpoint per revision. It proves neither journal/snapshot storage nor browser,
MCP, concurrent structural intent, or production-session qualification. Retained
historical checkpoint identity includes original generation and metadata; a
future journal must reconstruct that exact basis before snapshots can replace
historical checkpoint rows.
