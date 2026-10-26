# JCS dependency

`github.com/gowebpki/jcs` is pinned to **v1.0.2**, upstream commit
`f5011b64e085aad7a0501c2d615065832d252a36`.
[Primary source](https://github.com/gowebpki/jcs/tree/v1.0.2),
[primary license](https://github.com/gowebpki/jcs/blob/v1.0.2/LICENSE).
The exact Apache-2.0 license is retained in `jcs-v1.0.2-LICENSE.txt`
(SHA-256 `cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30`).
The runtime package imports only Go's standard library.

The pinned parser rejects duplicate names (including escaped equivalents),
invalid UTF-8, lone UTF-16 surrogates and nonfinite binary64 numbers. It sorts
object names as UTF-16 code units and does not normalize Unicode. Its internal
maximum nesting depth is 10000; Mooring checks the stricter 64-container limit
before calling it, together with 1 MiB raw/canonical limits and 32768 structural
punctuation tokens outside strings to bound wide trees. Atomic fingerprinting
also admits at most 256 operations before canonicalization. The parser builds
one tree and emits one output buffer, avoiding repeated subtree serialization.

Shared public golden vectors live in
`mooring/protocol/fingerprint-fixtures.json`; Go tests exercise their number,
Unicode, membership, task and retry cases. These checks establish fingerprint
bytes, not protocol schema validation, authorization or application correctness.
Adapter semantic validation must consume normalized returned bytes, never the
original exact-integer JSON representation.

The helper has no production caller or transport route. The containing Go
package is imported by the CLI, so dependency notices must still account for
the new import even while the helper remains unused.
