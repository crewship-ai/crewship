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
