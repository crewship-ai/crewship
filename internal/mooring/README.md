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
the latest accepted checkpoint without external side effects.
It returns an internal `PreparedFailure{OperationID,Reason}` naming the actual
failed immutable member. Missing/foreign identity or uncertain engine error
aborts without a terminal outcome. The actual failed operation receives the
direct reason; atomic siblings and all descendants preserve that original root.
Host admission failures use a documented deterministic first-member group root.
Definite admission/external dependency failures propagate before SCC detection;
internal and whole-change cycles precede acyclic internal forward-order errors.
The shared batch-resolution vectors test this intersection with the Rust planner;
Rust's eligible-for-host result is not a persistence receipt. It must preserve
application format/version metadata within the qualified checkpoint contract.
Host Go does not interpret application commands or CRDT internals. Intra-change
operation order is immutable; cycles reject before acyclic forward dependencies.
Whole changes follow stable topological order. A failed change and its dependent
changes are rejected while independent changes can survive. Every accepted
operation receives a contiguous revision; checkpoints exist at atomic boundaries.

The host checks current authority before and after each expensive application,
then rechecks every fresh accepted author after all application work. A later
change can expire an earlier author's clock-bound grant despite the SQLite
writer lock. Any changed or uncertain final authority rolls back the entire
batch with no terminal result/event; prepared independent work remains a draft.
Owner capability is checked in the same FULL-synchronous SQLite writer transaction
immediately before persistence and again immediately before commit. Callback inputs are isolated copies so mutation
cannot change immutable authorship/membership. Outcome, task, initialization and
checkpoint-read authority are distinct callback intents. The callback must bind
participant references to host identities; an owner lease grants no such rights.

A reused ID with different fingerprint/membership conservatively rejects the
whole batch before admission, preserving the original ledger and emitting no
fresh terminal result. Independent fresh work remains pending and may retry
separately with its same immutable IDs. This is a primitive admission policy and
remaining P4.2 integration limitation: protocol-wide conflict-attempt semantics
must be decided and tested before enabling the wire path. It is excluded from
the shared planner intersection, not a claim that the full PRD is complete.

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


Current portable recovery qualification is **exact IDs into an empty artifact
destination**. Workspace/crew forks, artifact ID remapping and history merges
are refused before any destination deletion/write. Fingerprints bind original
scope; re-keying history, dropping it or presenting it as new-scope Saved would
violate that identity. Qualified clone/import provenance belongs to the later
recovery integration. No transformed restore support is claimed by this primitive.

`CanonicalE3WireJSON` is an unused strict JSON/JCS resource primitive for the
explicit negotiated `mooring-e3-bounded-json-v1` implementation profile: 262144
raw and canonical body bytes, 64 container levels and 32768 structural tokens
(opening braces/brackets, commas and colons outside strings). It retains generic
protocol maxima; a larger task/checkpoint is unsupported on this profile until
qualified transport support exists. Drafts must remain intact on refusal.
`ValidWireIdentifier` uses 1–200 Unicode scalar values excluding NUL; application
payload strings remain opaque. Version syntax validation does not negotiate
support. Complete envelope schema, authentication, directions and scope checks
remain separate prerequisites before dispatch. No route calls these helpers.

`NewWireSchema` accepts a trusted public schema snapshot and audits the complete
fixed Mooring schema vocabulary before construction: unknown keywords/patterns,
remote/missing references and cycles fail closed even in unused branches or
negations. The recursion bound is derived from the acyclic schema graph, not an
unadvertised message work quota. `ValidateE3` returns canonical bytes after the
bounded JSON and envelope shape pass. Shared semantic-negative fixtures remain
shape-valid deliberately; authentication, direction allowlists, authority, cross-
field equality, immutable membership/fingerprints and app effects still require
separate enforcement. No runtime route uses this interpreter.

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

New Mooring run admission additionally requires the original authenticated
human receipt's decimal-text user revocation counter and exact session/CLI
credential provenance. Authentication carries a copied reference through the
signed socket ticket, host-only message option, queued origin and immutable run
principal. No raw bearer is retained. Missing legacy provenance is preserved as
missing and cannot be refreshed from a later run bearer or current user lookup.
Bare legacy receipt capture remains unfenced even when a current user row can
be resolved; only an original authenticated source creates the new user fence.
Generic Docs/GridCraft retention and queued resume keep their ordinary run
lifetime while preserving the original source and user fence. Original JWT
expiry is enforced by strict Mooring capture/check, not made a generic run
deadline. Initial source capture still requires a live exact credential.
The API helpers validate the original row identity, current user/counter, status
and explicit scope, and return the earlier original/current credential expiry.
ADMIN references also bind the current bearer HMAC key identity. New scoped
admission rejects malformed scope metadata; canonical SQL NULL, empty text and
empty-array unrestricted policy remain supported. Generic provenance retention
does not change the legacy bearer scope parser.
The production callback must use the writer-bound reader and intersect that
horizon with the run, exact current attempt and mandate deadlines. These helpers
do not implement that callback or enable Mooring. Existing web issue origins
retain their separate web execution revision rules.

For an exclusive host-initiated dispatcher, `Runtime.Exchange` serializes whole
request/reply pairs and bounds each exchange to five seconds or the caller's
earlier deadline. Queued cancellation does not close an active caller's channel;
cancellation during I/O or a transport error closes it so a late response cannot
become another request's reply. Typed callers still validate shape, correlation,
scope and every candidate before use. Do not mix this mode with direct RPC reads
or unsolicited runtime requests; an asynchronous protocol needs a separate
reviewed multiplexer. These operational timeouts are not delivery leases.

An operator may explicitly request `RuntimeConfig.ControlProfile` equal to
`mooring.prepare.v1`. Bootstrap still uses the 262144-byte ceiling. The host
offers this profile in Hello and requires the exact RuntimeReady selection;
missing, different or unsolicited selection fails closed. Only after readiness
does the private transport permit frames up to 2097152 bytes. Default runtime
calls retain the original ceiling. RuntimeReady uses closed, case-sensitive
objects and rejects duplicate keys before selecting any profile. Every RPC
frame is bounded to depth 64 and 32768 structural tokens before syntax parsing.
This transport choice does not relax public
participant/batch validation: a typed private dispatcher must accept only the
four negotiated control envelopes with their separate raw/canonical, decoded
checkpoint, depth and structural-token checks. Framing-helper echo tests are
not semantic private-dispatch or D3 application acceptance.

The optional target preparation primitive records host-owned stable target
versions and per-operation effect provenance in the same SQL transaction as
checkpoints and immutable outcomes. It is separately default-off and never falls
back to a context-free adapter when enabled. `agent_confirmed_current` requires
complete qualified checkpoint inventory, exact current read observations and
host prospective operation labels. Missing targets, retained tombstones and
stale observations have distinct member failures; corrupt inventory or runtime
binding errors abort nonterminally. Causal stale reads, provisional targets and
human updates require later qualification; this primitive does not complete E3.

Target and effect rows, normalized dependency arrays, and explicit provenance
completeness survive exact-ID backup/restore as durable history. Legacy records
are marked incomplete rather than invented as no-effect history. Tombstone IDs
cannot be reused; there is no history pruning to make quotas pass. Count defaults
are 65536 target identities, 1000000 effect rows, 256 effects per operation. The
additional provenance default is 128 MiB of logical serialized UTF-8 bytes:
exact `encoding/json` dependency arrays plus complete effect `record_json`
objects (scope, operation/target IDs, before/after revision labels and flags).
This is independent of SQLite file size and private/public frame budgets. Blob
length counts UTF-8 bytes, without numeric casts of u64 counters. SQL binds each
accounting record to indexed columns so a short unrelated JSON object cannot
hide long identifiers. Known dependency bytes are checked before preparation;
actual bounded prepared effect bytes must fit before ledger/checkpoint commit.
Capacity failure rolls back the whole batch nonterminally, preserving drafts and
prior durable history. Structural revisions use the same host operation revision;
canonical decimal TEXT is ordered by length and BINARY text across the full u64
range. Actual thousand-operation task/causal admission remains separate acceptance.


The dormant human Page authority adapter uses canonical current Page grants,
crew membership, folder ACLs and private custody in the protected SQLite writer.
It requires the host-captured original session/CLI credential and current scope;
request role snapshots cannot grant access. Cross-workspace folder references
fail closed. Initialization requires an empty Page owned explicitly by that
human. The result is a current permission/member binding, not a ticket or Saved
receipt. Outer admission must preserve its original captured values, qualify
engine versions and host incarnation, audit private owner reads, and require a
send-time delivery gate for every protected response. No route is enabled here.

The separate unused causal SQL qualifier builds compact original-read
attestations from complete retained operation dependencies and target effects.
It checks every intervening effect, positive zero-effect completeness markers,
exact unsigned counters, target history continuity and original read bindings.
Missing/corrupt history returns a nonterminal error rather than a guessed stale
rejection. Local work budgets are 10,000 operations, 65,536 dependency edges, 100,000
effects, 128 MiB encoded history and 1,000,000 traversal steps; exceeded budgets preserve
pending work. The existing strict-current private profile remains unchanged.
The 1k-task qualification test uses an explicit mock application/allow-all host
fixture; it does not establish participant authorization or WordCraft transport.

`TargetStoreConfig.ReadPolicy = "agent_dependency_covered"` separately opts into
causal-agent preparation. It requires `CausalTargetBatchAdapter` and explicit
finite history budgets. Under the same SQL writer, the host validates the actual
input inventory, qualifies every intervening retained effect, and passes an
isolated compact context alongside the unchanged original operation. It then
independently validates all candidate effects and every earlier same-atomic
writer against immutable dependency closure before the existing commit fences.
The empty policy retains strict-current behavior. This store primitive does not
select a private runtime profile or establish application/participant authority;
those require separately qualified integration.

The inactive human registry also has a trusted Rust-only replacement seam.
Capture `storeBinding` once when constructing each store; its authority and
adapter are immutable for that actual runtime binding. The bounded host
operation wrapper acquires its runtime lease before the SQL writer and retains
it through commit/response. Replacement refuses active operations, requires the
actual old child's completed process exit and a fresh owner generation, and
verifies the replacement's exact profile/versions. It invalidates every old
ticket, closes all persisted scope gates without deleting issuance evidence,
and admits no replacement until every old gate is blocked or its original
BOOTTIME expiry has passed. Old store outcome/retry authority fails after the
binding changes even when preparation would not contact Rust.

Reconnect rechecks an existing original human admission and creates only a new
bounded single-use ticket. Session/participant/source/member/epoch/deadline are
never refreshed. A registry retains its original host restore incarnation;
database restore or Go restart cannot revive those capabilities. The manager
must use the one registry-selected session ID and separately validate the exact
replacement `SessionOpened` before forwarding any content. This seam installs
no session manager, Rust control messages, browser route or raw-human write
profile. The tagged replacement test uses actual pinned native child processes
and canonical FULL SQL with an explicit fixture clock/ingress; the separate
actual HTTP test qualifies signed authentication and BOOTTIME storage behavior.
`OwnerStore.Release` is a host-only voluntary shutdown primitive. The exact
current token/scope/generation must pass under the FULL SQL writer; expiry and
closure of its gates commit together. It never deletes the owner generation,
changes original gate expiry/ordinal, or fabricates a blocked acknowledgement.
The manager must stop/drain the actual old runtime. A subsequent Acquire may
immediately issue a new owner generation, but replacement content admission
must still wait for the original outstanding delivery barrier. A stale release
cannot affect that new owner. This does not enable any session or public route.
`StartSessionRuntime` is an unused, default-off transport for the explicit
`mooring.session-causal.v1` profile and WordCraft 0.4.0/0.2.0/0.10.0 tuple.
It owns one inherited-FD reader. Correlated checkpoint preparation remains able
to progress while an actor control awaits its host response; the old `Runtime`
and `Exchange` are not exported from it and their factories reject this profile.
`Open` uses the registry's one existing session ID; `Restore` preserves the
host's exact checkpoint wrapper. Both return correlated transport responses,
not proof of owner validity or installed document state.

Outstanding requests, queued controls, borrowed controls and retained replies
share 128 entries and 4 MiB of encoded control bytes. Ordinary whole wrappers
(including metadata) retain the 256 KiB raw/canonical ceiling; only the explicit
checkpoint/commit and causal preparation purposes use the 2 MiB private limit.
These are retained-queue bounds, not a total process RSS bound or caller-memory
bound. One writer enforces a five-second whole-method deadline. Cancellation
before sending removes only that request; cancellation after sending poisons
and kills the channel. `Done` closes after both actual child exit and reader
exit. Original submitted intents remain the caller's recovery responsibility.

This slice does not enable a CLI session actor, participant route or live
submission. The lifecycle manager must bind actual owner lease, startup basis,
renewal nonces and restored inventory before activation. The host dispatcher
must process sealed submissions/status barriers in FIFO order, stop on an
uncertain prefix, recheck original per-operation authority and final delivery
gates, and reject encoded output over budget before SQL COMMIT. Readiness,
correlation and private process identity do not supply those permissions.

`SessionActivation` is a separate opaque INITIAL startup proof. The channel
records only small hashes/summaries of its actual sent Open/Restore and actual
correlated owner grant/Restored/Opened messages; a caller-supplied copied Opened
is never evidence. Activate requires that exact concrete session adapter/store,
preselected session ID, runtime reference, nonce and owner fence, then checks
current SQL checkpoint bytes/digest and the complete target inventory in the
same FULL writer as OwnerStore.Check. It mints only after transaction success
and refuses duplicate activation. Initial inventory has no fake empty atomic
operation. The token remains tied to that exact store/process/owner and fails
on channel death or stale ownership; it does not pin the initial document head
forever after real saved commits advance it. It carries no human, agent, reader,
write or delivery permission. API constructors and every later operation still
need independent current canonical authority, lifetime leases and final fences.

The inactive API session binding consumes an actual `SessionActivation` and the
original initialized store. A private set-once authority cell denies all reader
operations before activation; initialization alone uses a bounded original-human
operation. The final initialization fence distinguishes the newly installed
collaboration attachment from competing native engines while rechecking current
human authority, personal custody and owner lease. Other Page custody checks
keep their original behavior. Store callbacks identify the actual invoking
store; only the narrow internal sealed-processing copy retains its original
store identity. A copied store/configuration cannot borrow this API authority.
The session constructor derives identity and versions from activation, checks
FULL SQL ownership, and never accepts a copied wire Ready message. Dead child,
source ABA and wrong-store bindings fail closed. The existing prepare-only
replacement seam cannot replace a live session. Actual session recovery, raw
participant admission, FIFO settlement, provider isolation, UI and deployment
remain separate qualification requirements.

The dormant checkpoint identity configuration retains an application-qualified
format, semantic tuple and complete asset manifest beside each new checkpoint.
There is no default empty manifest or backfill: legacy NULL remains unknown.
Canonical metadata sorts unique asset IDs by UTF-16 and preserves decimal u64
sizes. The generic host never parses application state to guess its references.
The session activation path requires the same concrete negotiated adapter for
checkpoint description and target preparation. The generic callback interface
itself grants no process or participant authority.

Both initialization and Saved account metadata bytes alongside decoded checkpoint
bytes in retained history capacity. Complete SQL usage is checked again after
trigger/output effects and before final authority/owner fences. Capacity errors
publish no partial checkpoint or event. Exact retries retain the original metadata
and do not call the descriptor again. An UPDATE guard prevents normal writers
from relabelling retained metadata; it is not authenticity against arbitrary trusted
SQL deletion/replacement or tampered portable input. Portable restore retains exact
known metadata and legacy NULL. The concrete session descriptor uses the sole
correlated reader and an independent preparation lane, including while Open
awaits ownership; mismatched input/scope/session/tuple poisons the process.


`SessionCommitter.CommitFull` is the separate dormant full-identity entry. It
accepts `FullCheckpointBasis`, then checks original sealed processing authority
and the exact captured owner before resolving retained metadata/content inside
the same FULL writer. Both the original atomic-boundary checkpoint and current
head must have qualified retained metadata and creator provenance. Legacy NULL
is unknown; neither a content digest nor a fresh owner generation substitutes
for the original full identity. Identity-enabled stores refuse the older raw
`SessionCommitter.Commit` entry. Immutable operation reads are never rewritten.

The uncommitted output callback receives isolated copies of the actual full
checkpoint, target versions and receipt. Its closed `SessionCommitResult` must
match those SQL values and fit both the 2 MiB raw and canonical budgets before
COMMIT. Final author/owner fences still run after output preparation. Runtime,
session and request correlation remain the concrete verified manager's duty;
this primitive does not authenticate caller-provided strings or qualify a live
runtime factory. The existing strict/stateless paths retain their behavior.


The unexposed session profile requires `checkpoint_identity_v1` in Open and its
exact Opened echo. Activation checks the actual transcript and full checkpoint
identity against retained metadata under the writer; `InitialFullBasis` is
separate from `InitialBasis`'s internal content digest. Legacy opaque startup
proofs do not establish this capability. Strict/stateless profiles are unchanged.

`ReadFullCheckpoint(ctx, exactOwnerLease)` returns a retained full wrapper and
complete target versions under the same FULL writer. It invokes the immutable
current-reader callback before loading data and again after decoding, with owner
checks before and after; no caller can supply an authorization override. An API
startup reader needs its own bounded original-source operation rather than
relaxing the existing preactivation denial. The actual inherited-peer tests use
mock participant authority, real CLOCK_BOOTTIME and explicit host Release to
prove owner 2 restores unchanged creator-1 data. They are not two-process cold
recovery, human admission, participant delivery or production enablement proof.

The inactive API session cell enables checkpoint metadata only through the same
concrete session adapter used for target preparation. Its original immutable
store authority admits two separate preactivation purposes: initialization and
an authenticated startup checkpoint read. Each purpose retains the actual HTTP
credential and ingress lifecycle, exact live process and captured owner through
the complete bounded store operation. A copied store or escaped context cannot
borrow that authority; binding permanently closes the startup entry.

Startup reads use `ReadFullCheckpoint` under its initial/final canonical reader
and owner checks. The manager restores the returned retained wrapper and target
inventory instead of constructing checkpoint identity from caller strings.
These reads grant no participant delivery rights, install no public route and
do not establish Saved, native UI collaboration or deployment qualification.

A private-owner override during restoration does not itself record
`artifact.private_owner_accessed` or authorize external output. Before any
content leaves the host, a future delivery handler must durably record the
required owner-access audit and recheck the current recipient and delivery
lease. This startup helper supplies neither audit nor delivery authority.
