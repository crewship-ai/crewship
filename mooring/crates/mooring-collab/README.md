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
