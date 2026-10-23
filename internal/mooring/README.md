# Mooring host JSON profile

See [the pilot contract](../../docs/specs/mooring-pilot.md). Application registration
is declarative; host authorization never comes from a manifest or document.


The unused `OwnerStore` primitive is separately disabled by default. Its caller
must provide a trusted OS boot identity and a suspend-aware monotonic clock;
there is no wall-clock fallback. A caller authenticates the runtime, then checks
its capability inside the same writer transaction as participant authorization
and persistence, after expensive validation and immediately before writing.
The persisted owner is unique per Artifact across application profiles. A lease
capability conveys no participant permissions and is never sent to an editor.
Expired ownership can only be acquired with a new generation and random token.
Deletion removes the ephemeral owner record and old capabilities fail closed. Workspace/application moves are not supported by
this primitive and require a separately authorized transition; a mismatched
scope cannot silently reuse the original ownership record. No HTTP routes,
CLI command or runtime process currently invokes this primitive.

### Managed child transport (unused primitive)

`StartRuntime` is separately disabled unless its caller explicitly enables it.
On Linux, Go gives the child one inherited AF_UNIX stream at FD3 and runs
`mooring-collab --rpc-fd 3`; no listener, token argument or host secret reaches
an editor. The release caller must select and verify the packaged binary.
Other operating systems fail closed until a reviewed transport exists.

Frames are a four-byte big-endian byte length followed by UTF-8 JSON, bounded
to 1..262144 bytes in each direction. Invalid/truncated incoming frames close
the channel. JSON shape, duplicate-key/JCS rules, decoded payload limits, scope,
current grants and delivery leases remain dispatcher responsibilities.
The private host Hello / RuntimeReady handshake has a five-second deadline
and requires the exact protocol/model/engine tuple; readiness grants no
participant authority. No content operation or Saved receipt is dispatched
by this primitive. Child death/cancellation closes the channel; a later
integration must close all associated delivery gates and preserve pending drafts.
The child receives only LANG=C.UTF-8 and TZ=UTC; backend credentials and loader
environment are not inherited. Diagnostics are discarded. Tests use an actual helper child and inherited FD,
not a running Rust service or live collaboration acceptance.

For actual Go/Rust bootstrap acceptance, build the pinned child using
`crewship-heavy cargo build --manifest-path mooring/Cargo.toml -p mooring-collab --release --locked --offline`,
then run `crewship-heavy env MOORING_TEST_RUST_BINARY=/absolute/path/to/mooring-collab go test -race -tags mooring_rust_integration ./internal/mooring -run '^TestRuntimeActualRustChild$' -count=1`.
This opt-in test starts the real Rust process, checks readiness, unsupported
post-bootstrap traffic closure and host cancellation. It does not exercise
participants, leases, content dispatch or durable receipts. The explicit build
tag excludes this dependency from ordinary Go tests; selecting it without the
required binary fails instead of silently skipping.

`DeliveryStore` is separately disabled and unused. Its authority callback must
resolve the authenticated human/run and current scope in the same FULL writer
transaction. Gates bind the exact private owner capability as well as public
name/generation, permission version and boot clock. TTL is at most two seconds.
`RevokeTx` belongs inside the canonical ACL transaction and cannot confirm before
that transaction commits. A persistent barrier captures a monotonically assigned
gate ordinal cutoff; later admissions cannot change its membership. Repeated
revocation without new admission reuses the barrier, so revocation never consumes
admission capacity. No issued gates yields only a prepared empty-set decision
(`NoIssuedGates`), confirmable by the host after the ACL commit; it has no
persistent status row. Otherwise completion waits for authenticated runtime
blocked acknowledgements or every original issued expiry, including old owners.
Portable backups omit runtime gates/barriers. Physical recovery closes gates
and clears source acknowledgements while retaining issued expiry bounds.
Replacing an existing target with issued delivery metadata is refused before
deletion, including workspace slug matches and offline target replacement.
Qualified retirement of this metadata remains future work.
Renewing with the last nonce does not extend the host expiry. Integration must
retain the original Rust pending renewal and send timestamp across retries;
rebinding a retry to a fresh timestamp could outlive that unchanged host expiry.
The wire nonce-to-pending-renewal binding is not implemented by this store.
There are no tickets, API/CLI routes, production identity callbacks or content
dispatch paths in this slice; synthetic authority tests are not live RBAC proof.

For an exclusive host-initiated dispatcher, `Runtime.Exchange` serializes whole
request/reply pairs and bounds each exchange to five seconds or the caller's
earlier deadline. Queued cancellation does not close an active caller's channel;
cancellation during I/O or a transport error closes it so a late response cannot
become another request's reply. Typed callers still validate shape, correlation,
scope and every candidate before use. Do not mix this mode with direct RPC reads
or unsolicited runtime requests; an asynchronous protocol needs a separate
reviewed multiplexer. These operational timeouts are not delivery leases.
