# Experimental wire schema and generated shapes

`schema.json` is a self-contained draft, not a claim of implemented endpoints.
`generate.py` emits Rust serde types, Go shapes and TypeScript interfaces using
the Python standard library plus the existing `rustfmt` and `gofmt` formatters. Run `python3 mooring/protocol/generate.py`
from the repository root; `--check` rejects generated drift. Source ordering is
preserved, and type generation does not perform network I/O.

Generated types do not implement authorization, pattern/size limits, dependency
validation, atomic outcome consistency, canonical fingerprints or full schema
validation. Go unions are `json.RawMessage` aliases to preserve JSON objects;
the host must validate and discriminate them before use. Rust constants are
single-valued types and Rust counters validate canonical decimal u64 strings.
Go and TS counters remain strings and require equivalent validation at inputs.

The separate Rust `wire::validate_bounded_envelope` helper strictly parses and
JCS-normalizes a body, then validates the exact bundled schema vocabulary.
It checks references, conditionals, unions, required fields, extra properties,
string/array limits, uniqueness and the eight pinned pattern languages. Unknown
schema keywords or patterns fail the vocabulary audit, including under `not`.
It is an implementation of this schema subset, not an arbitrary-schema engine.

`wire-profile-fixtures.json` defines the negotiated E3 small-frame profile:
262144 raw and canonical UTF-8 bytes, container depth 64 and 32768 structural
tokens (opening `{`/`[` and commas/colons outside strings). Resource preflight
runs before JSON allocations. Generic checkpoint/frame maxima remain larger;
this helper rejects larger bodies and cannot claim support for those paths.
The companion `normalize_bounded` applies resources and strict JSON/JCS without
interpreting application field names. Identifier fields permit 1–200 Unicode
scalar values excluding NUL, without Unicode normalization; opaque payload
strings retain their own application rules. Rust planner, client and fingerprint
identifier admission use that same predicate. Version negotiation uses the
frozen three-part, non-leading-zero, at-most-nine-digit component grammar.

Shape validation does not authenticate sender direction, host issuer, scope,
participant or gate; negotiate durability; establish contiguous saved revisions;
verify canonical base64 pad bits; or validate adapter semantics and causal reads.
Those checks must precede dispatch. The semantic fixtures continue to describe
requirements, not execution evidence for those unimplemented integrations.

`boundary::validate_commit_result` is a separate pure check against an exact
pending request. Its non-deserializable transport context must come from verified
host admission; an issuer string or runtime message cannot construct that proof.
It checks scope, request ID, owner generation, immutable atomic membership,
per-operation revision continuity and disjoint saved ranges. Older replay ranges
may coexist with newer ranges and need not be adjacent. The outer durability is
a conservative lower bound over saved members and must meet the caller's required
minimum; original per-operation receipts are not rewritten. An all-rejected reply
makes no saved claim. `crewship` is the logical host identity across restarts;
history from another issuer needs a future explicit provenance protocol.
Modes come from caller-qualified host/session policy; Hello/Welcome does not yet
implement a durability-selection field or replicated-durability qualification.

This helper neither issues receipts nor applies document state. It does not yet
validate document-update wrappers, owner/delivery grants or their session/nonces;
those remain unsupported until their separate binding validators are connected.
`canonical_base64_decoded_len` checks standard alphabet, padding, unused pad bits
and decoded size without allocating decoded data. Application decoding and
delivery permission remain separate. Shared `boundary-semantics-fixtures.json`
covers receipt tampering, trusted minimums and base64 boundaries; it is not live
admission or persistence evidence.

`mooring_protocol::negotiate` initially accepts only an exact tuple of protocol,
model and engine versions. Unsupported versions return `version_mismatch` before
any document content. Minor/patch compatibility needs shared evidence before
widening this policy. The Rust library is currently unused by deployed services.


`conformance-fixtures.json` contains positive/negative JSON Schema Draft2020-12
cases plus semantic rejection inputs. Shape-valid semantic fixtures are not
proof that authentication, atomicity, revocation or persistence enforce their
expected errors. `fingerprint-fixtures.json` defines required cross-language
JCS golden vectors; the shape generator does not implement canonical hashing.
Generic numeric payloads must be normalized to the specified binary64 model
before adapter validation as well as before hashing.


`mooring_protocol::canonical::normalize` is a separate bounded input helper. It
rejects raw duplicate properties (including escaped aliases), invalid Unicode,
nonfinite numbers, trailing JSON and oversized input/output. It then emits JCS
with exact pinned `serde_json_canonicalizer` and reparses those bytes. Adapter
validation must consume the returned normalized value, so an unsafe integer
JSON number cannot hash as one value while applying as a different exact u64.
Strings containing exact large integers remain strings.

`fingerprint_atomic` hashes full ordered membership, authenticated participant
references, scope, payloads, read preconditions, dependencies and immutable task
identity. It excludes supplied fingerprints and transport/session-owner fields.
Hosts use `matches_wire` after their own schema and authority validation; client
construction may use `with_fingerprints`. This helper does not authenticate a
participant, determine ownership, validate application semantics or issue Saved.
The shared golden tests cover numeric/UTF-16 ordering and escaping, malformed
raw JSON, membership perturbations, stable retry identity and normalized adapter
input. Checkpoint tests currently verify canonical preimages only; they do not
implement checkpoint wire decoding/integrity or recovery authority.

The inherited-channel `mooring.prepare.v1` control profile adds exactly four
shapes: `validate_checkpoint`, `checkpoint_validated`, `prepare_atomic`, and
`atomic_prepared`. Bootstrap remains limited to 262144 bytes. The host must
explicitly offer the profile and verify the exact runtime selection before a
private transport raises its frame limit. Participant transports cannot offer
or select this profile. The public bounded validator and SDK reject the private
control messages and negotiation fields.

The separate control validator permits 2097152 raw and canonical bytes, depth
64 and 32768 structural tokens, only on caller-attested inherited transport and
in the correct direction. Checkpoints use canonical standard base64 with zero
padding bits, at most 1572864 decoded bytes, and SHA256 of those exact opaque
bytes before application interpretation. A component that fits its own limit
may exceed the whole frame limit; it must fail without truncation. Public and
atomic batch limits remain unchanged. These helpers do not establish channel
provenance or advertise runtime support on their own.

The first prepare capability is `agent_confirmed_current`: host-owned complete
target versions and prospective per-operation revision labels accompany the
immutable change without becoming part of its fingerprint. Human updates,
provisional target reads, and causal stale-read overlays remain unsupported in
this first capability. Prepared checkpoints and effects are speculative; only
the host can commit them or issue a durable receipt. Invalid checkpoints and
uncertain engine errors close the private channel as a nonterminal failure;
there is no fifth large ProtocolError envelope.

The draft `mooring.session-causal.v1` shapes and `session_control::validate`
reserve a separate inherited-channel session contract. They do not select the
profile in a running child. Existing strict and causal preparation definitions
remain identical. The fixed limits in `SessionOpen` describe the initial
profile; generic protocol maxima are not an advertised session capability.
Batch envelopes and ordinary inner participant messages retain the 256 KiB
raw/canonical bound. Larger checkpoint, document-update or host-receipt payloads
require explicit `bounded_transfer_v1` selection and bounded chunks; wrapping
an ordinary public message cannot enlarge its limit.

The session fixtures execute closed shapes, private directions and profile
separation. Their `semantic_cases` are pending driver requirements, not runtime
proof: host-captured FIFO status barriers, exact owner/session/gate binding,
retained basis validation, precommit output budgeting, authenticated receipts,
chunk digest/reassembly and per-chunk send fences still require integration.
No host `Saved`, admission, live distribution or recovery guarantee follows
from a valid shape. The actual driver must compose the existing causal prepare
validator explicitly and keep preparation progressing while durable replies
are pending on its single inherited-channel reader.
