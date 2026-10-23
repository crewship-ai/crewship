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
