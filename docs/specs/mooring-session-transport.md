# Mooring human session transport

This experimental Rust-backed session profile is separately disabled by default.
The server registers neither route unless trusted configuration enables it and
verified runtime assets pass startup checks. Invalid enabled configuration fails
closed. The [pilot JSON profile](mooring-pilot.md) remains a separate contract.
This reference describes implemented handlers, not deployed multiuser or agent
acceptance. Page revocation currently can close a shared runtime and disconnect
an unaffected participant; continuity through that event is not qualified.

## HTTP operations

| Operation | Purpose |
|---|---|
| `POST /api/v1/pages/{slug}/mooring/session` | Admit a human connection against current Page authority |
| `GET /api/v1/pages/{slug}/mooring/ws` | Upgrade the admitted connection to a WebSocket |

Both operations require current human authentication, workspace context and Page
access. A slug or `@ID` selects the Page in that workspace. The restore fence runs
before authentication/body handling. Read access cannot authorize a writer.
Agent/internal credentials cannot substitute for the human session admission.

### Session request and response

The POST body is a JSON object with optional `write` (boolean, default `false`)
and `checkpoint_base64` (canonical base64 initialization checkpoint). Explicit
JSON null for either field has the same effect as omission. Unknown or
duplicate keys, case aliases and non-object bodies are refused. The request body
is limited to 2 MiB and a decoded checkpoint to 1536 KiB. A checkpoint cannot
replace an already-open document; existing canonical storage and checkpoint
validation govern initialization. There are no caller-selected role or identity
fields.

A successful HTTP 200 JSON response contains `session_id`, `checkpoint_revision`,
`sdk_prefix`, `participant_ref`, `ticket`, `connection_ref`, `origin`, `versions`
and `websocket_path`. Revision and origin are decimal strings; origin is a
host-reserved positive safe integer. The version tuple is protocol `0.4.0`, model
`0.2.0`, engine `0.10.0`. An optional `editor` contains `url` and `bundle_digest`
only when a verified editor provider supplies both. Responses use `Cache-Control:
no-store`. This admission response is not a committed mutation or a Saved receipt.

### WebSocket upgrade

Append the separately returned `connection_ref` query parameter to the returned
`websocket_path`, which already carries workspace context. Authentication must
match the original admission. A connection can be used only once. The `Origin`
header must exactly match the trusted frontend origin; a client cannot nominate
a different trusted origin. Missing, foreign, stale or reused references are
refused before upgrade. The first public frame is the original `session_join`
with its retained one-use ticket and version tuple.

Successful upgrade is HTTP **101 Switching Protocols**, with `Upgrade` and
`Connection` headers and no HTTP response body. Subsequent messages are protocol
frames, not JSON HTTP responses. Authentication/Page/restore failures before
upgrade use the surrounding API's JSON error envelope. A malformed WebSocket
handshake may instead return a plain HTTP handshake error from the transport.

## Confirmation and limits

The HTTP adapter forwards bounded original operations through the private Rust
transport and canonical host storage. A queued/pending frame is not a saved
change. Only a confirmed host commit authorizes the corresponding Saved response;
clients retain uncertain originals without silently reconstructing packets.
Delivery authorization expires after the configured lease (default five seconds,
never longer), measured from request send time. Revoked writers cannot acquire
fresh write authority through a reader connection.

The current UI restricts undo/redo for deletions or changes overlapping another
participant's deletion and displays a refusal message without claiming a new
save. Complete two-browser-plus-agent behavior, unaffected-participant continuity
after Page revocation and deployed UI acceptance remain separate qualification
work. This contract does not declare those scenarios complete.

The matching CLI commands and their input/output behavior are documented in
[the Mooring CLI reference](../cli/mooring.mdx). Runtime configuration is listed
in [environment variables](../configuration/environment.mdx).
