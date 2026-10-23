# Mooring Rust workspace

Application-independent collaboration code. This workspace is currently unused
by Crewship, so it does not start a service or change the deployed application.
The Go host remains the authority for permissions, fencing, durable writes and
receipts. Draining a Rust batch never means the changes have been saved.

`mooring-core` begins with bounded batches of atomic changes: 100 ms from the
first operation, 256 operations, or 256 KiB, whichever comes first. These are
initial defaults, configurable by the runtime. Oversized atomic changes return
to the caller intact. The runtime must schedule a timer at `deadline()` and send
batches returned by `push` or `take_due` immediately. Transport envelope bytes
must be reserved when configuring the batch byte limit. This primitive does not
claim end-to-end latency or durable storage guarantees.

From the repository root:

```sh
crewship-heavy cargo test --manifest-path mooring/Cargo.toml --locked
crewship-heavy cargo clippy --manifest-path mooring/Cargo.toml --all-targets --locked -- -D warnings
crewship-heavy cargo build --manifest-path mooring/Cargo.toml --target wasm32-unknown-unknown --locked
```

The workspace has no external dependencies yet. New dependencies must be pinned,
vendored and accompanied by their license texts before integration.

`mooring_core::lease::DeliveryGate` is a fail-closed primitive for delivery
checks. A verified host response grants time only from the renewal request's
send timestamp. Revocation permanently closes the gate; late responses cannot
reopen it. Native runtimes must obtain timestamps from `lease::boottime()`;
this uses 64-bit Linux `CLOCK_BOOTTIME` and returns an unsupported error elsewhere.
Tests advance a simulated suspend-aware clock; they do not suspend the OS or
process. They simulate response delay, but do not prove every runtime
content path checks the gate. The caller must bind each gate and renewal reply
to one authenticated participant and session.
