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
validate document-update wrappers or install grant state. Separate grant binding
checks are described below; their transport/controller integration is still absent.
`canonical_base64_decoded_len` checks standard alphabet, padding, unused pad bits
and decoded size without allocating decoded data. Application decoding and
delivery permission remain separate. Shared `boundary-semantics-fixtures.json`
covers receipt tampering, trusted minimums and base64 boundaries; it is not live
admission or persistence evidence.

`grant::PendingGrant` checks delivery/owner renewals, initial owner acquisition and
ticket-validation replies against immutable first-send request metadata. Exact
session, scope, generation/fence, permission version, gate and nonce bindings are
checked where already assigned. Initial acquisition and ticket validation accept
host-assigned session/fence or participant/gate only from caller-attested host
transport; selected versions and a known permission/generation floor remain bound.
Expiry is exclusive first-send time plus granted duration, bounded by caller policy.
Retries use the same stored bytes and private original timestamp, so a repeated
full-duration host response cannot restart the deadline.

The runtime must enforce fresh nonces and retain this same pending object across
retries; constructing a new object for an old nonce would violate the contract.
Native timestamps must come from CLOCK_BOOTTIME, with unavailable/regressing clocks
failing closed. The pure helper does not authenticate a socket, register fresh gates,
consume responses, revoke existing state or authorize content. The stateful controller
must still check the delivery gate immediately before every send and close on clock
failure. Debug output for bound grants omits owner fence tokens and participant data.

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
