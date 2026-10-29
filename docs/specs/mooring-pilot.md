# Mooring pilot JSON profile

The experimental Mooring application profile is enabled with the workbook
integration. It is a personal-document L2 pilot, not a claim of multiuser L3
support. Shared DIRECT operations are refused. GridCraft continues to use its
qualified native engine, canonical workbook tables, mutation protocol and receipts.
Unsupported GridCraft mutations remain blocked.

`internal/mooring/manifests/*.json` is a compiled, reviewed application registry.
Document content cannot install a manifest. Each manifest declares `id`, protocol
version `1`, format, editor entry pinned under `/b/<sha256>/index.html`, initial
JSON state, and command names with JSON Schema arguments. The JSON profile has
one generic effect, `replace-state`: validated arguments become the entire next
state. It does not interpret application IDs or fields. Adding an application
with this profile requires a manifest and editor package, not an API handler,
MCP tool or database table for that application.

The common `artifacts.RevisionStorage` boundary preserves each adapter's native
request, authorization and receipt types. `GridCraftAdapter` uses the existing
engine-backed store. `mooring.JSONAdapter` uses generic document, receipt and
intent tables. Every command executes within the host's SQLite writer fence,
with current credential, Page authority, revision and generation checks. Full
state replacement uses strict compare-and-swap; a stale request cannot overwrite
another confirmed change. The editor must retain unsent drafts separately.

## HTTP and CLI

All human routes require current human credentials and workspace context.

| Endpoint | CLI |
|---|---|
| `GET /api/v1/mooring/apps` | `crewship mooring apps` |
| `POST /api/v1/mooring/documents` | `crewship mooring create --file packet.json` |
| `GET /api/v1/pages/{slug}/mooring` | `crewship mooring read <slug>` |
| `POST /api/v1/pages/{slug}/mooring/commands` | `crewship mooring command <slug> --file packet.json` |
| `POST /api/v1/pages/{slug}/mooring/mandates` | `crewship mooring admit <slug> --file packet.json` |
| `DELETE /api/v1/pages/{slug}/mooring/mandates/{mandate}` | `crewship mooring revoke <slug> <mandate>` |
| `POST /api/v1/internal/mooring` (host IPC only) | `crewship mooring agent --file packet.json` (via authenticated sidecar) |

Creation takes `{app_id,slug,name}` and stages a private personal Page. Reads
return `{app_id,manifest,state,editor}`. State is `{rev,generation,snapshot}`.
Commands take `{mutation_id,base_rev,generation,command,arguments}`. The same
mutation ID and authority with the same packet return the original receipt;
a different packet with that key is a conflict. Preserve the exact packet on an
uncertain response. Workspace backups retain generic JSON state and immutable receipts. Live run
mandates are excluded and require fresh human admission after restore.

The history cap is 1000 revisions per document; the profile
refuses further changes without pruning confirmed history.

A successful receipt contains `mutation_id`, `rev`, `outcome: committed`,
`issued_by: crewship`, `durability: node`, `visibility: committed-revision`, the
canonical state SHA-256 `payload_hash`, and server `at`. It is returned only after
the transaction commits with SQLite synchronous FULL. This covers process/node
storage recovery, not replicated storage or loss of the storage volume.

## Agent authority and live updates

The generic `mooring_command` MCP tool accepts `action: read|command`,
`artifact_id`, `mandate_id`, and (for command) `request` in the command shape above.
The sidecar forwards the signed live-run credential; it does not mint identity.
Read returns the installed manifest so an agent discovers declared commands.
A human first admits `{mandate_id,run_id,base_rev,generation,command}`. This binds
one declared command to the retained live human-chat run, private Page, policy
version, Page authority version and exact revision/generation. The agent cannot
admit or widen its own mandate. Human authority, run lifetime, policy, private
Page ownership and mandate revocation are checked again inside every writer
transaction, including exact retries. A changed base requires a new human mandate.

After commit, the host sends `mooring.committed` on the existing authenticated
`page:<artifact_id>` channel with `{artifact_id,rev,generation}`. This is an
invalidation, not content: the host rereads through authorized HTTP, and the
shared SDK delivers the confirmed snapshot to the isolated editor. Reconnect and
reload reread durable state. Event delivery itself is not a durability receipt.
The generic JSON profile does not promise offline merge or recovery of drafts
after forced closure; the UI warns before ordinary departure with unsaved work.

The small Hello editor is a manifest plus client and immutable bundle. Its `set`
command and text schema are declarations, not special host code. The SDK and
GridCraft adapter share the private MessagePort handshake; native GridCraft
operation semantics remain in its adapter.

<!-- docs-inventory: ignore-api /api/v1/internal/mooring — protected host IPC, deliberately excluded from public OpenAPI. -->

## Isolation and release scope

The pilot uses an explicitly configured, separately served editor origin and
opaque credentialless frames. Its development deployment approval does not
approve a default isolation policy for customer installations. The same-host
transport remains experimental and requires a release decision before enabling
it for customers. Application packages receive neither a host credential nor
general host API access.
