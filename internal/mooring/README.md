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


## Unused collaboration persistence boundary

`BatchStore` is a separate, default-off host primitive. It has no route, runtime
supervisor call, UI or application registration. Enabling its constructor is
insufficient: it requires the same database as `OwnerStore`, a qualified adapter,
current host authority callbacks and explicit finite limits. The caller must
first authenticate its runtime and validate the transport/schema/negotiated
versions and raw frame bounds. Typed inputs do not prove a wire envelope valid.
The primitive bounds immutable batch input to 256 operations and 262144 bytes;
a transport must also account for envelope overhead and advertise lower limits
when necessary. JCS normalization precedes application semantics.

The adapter validates opaque checkpoint bytes and applies a complete change to
the latest accepted checkpoint without external side effects. It must preserve
application format/version metadata within the qualified checkpoint contract.
Host Go does not interpret application commands or CRDT internals. Intra-change
operation order is immutable; cycles reject before acyclic forward dependencies.
Whole changes follow stable topological order. A failed change and its dependent
changes are rejected while independent changes can survive. Every accepted
operation receives a contiguous revision; checkpoints exist at atomic boundaries.

The host checks current authority before and after expensive application work,
then owner capability in the same FULL-synchronous SQLite writer transaction
immediately before persistence. Callback inputs are isolated copies so mutation
cannot change immutable authorship/membership. Outcome, task, initialization and
checkpoint-read authority are distinct callback intents. The callback must bind
participant references to host identities; an owner lease grants no such rights.

Operation IDs, fingerprints, memberships, task attribution, checkpoint history
and audit are retained until artifact deletion. Quota pressure never deletes
idempotence evidence. A full admission ledger returns a capacity error without
a terminal receipt; checkpoint quota rejection is terminal only after a durable
ledger commit. Prepared adapter rejection, unknown engine/storage outcome,
stale owner and failed SQL commit cannot become Saved. Status authorizes scope
before lookup and compares `expected_fingerprint` before exposing any outcome.

Only successful SQL commit triggers `AfterCommit`, containing newly committed
changes in their application order. Exact retries do not reapply or emit again.
The callback is internal to the authenticated host, not a delivery capability;
recipient permission versions, leases, closed gates and revocation barriers must
still be enforced by the future delivery integration. Durability is `node`.

Tasks here are immutable admitted attribution/policy and cancellation records,
not a scheduler or complete task execution API. Restoring durable task/checkpoint/
operation/audit records grants no execution authority; fresh runtime/participant
admission and fresh owner capability remain mandatory. All six collaboration
tables are portable durable data; process-bound owner leases stay excluded.
