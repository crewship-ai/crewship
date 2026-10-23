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
