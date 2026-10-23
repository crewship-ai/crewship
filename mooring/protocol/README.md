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
