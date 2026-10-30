# Design Yjs authority

Status: current contract. Experimental and off by default; see
[Availability](#availability).

A Design artifact is a collaborative canvas of frames, rectangles, text and
images. People in the browser and agents edit the same document. The document
is a standard [Yjs](https://docs.yjs.dev/) document, and the server is the only
authority for it: it holds the current state, checks every change against live
permissions and decides what is persisted. Browsers are replicas.

Saved versions of a Design use the [artifact version store](artifacts-version-store.md)
in the `design` family. The live document and the version history are kept
apart and meet only when the live state is saved as a version.

## Availability

| Gate | Default | How to enable |
|---|---|---|
| Workspace flag `artifacts_design` | off | `crewship feature-flag enable artifacts_design` |
| Instance switch `CREWSHIP_ARTIFACTS_DESIGN_ENABLED` | unset | `true` enables Design for every workspace |
| Build-time `NEXT_PUBLIC_ARTIFACTS_DESIGN` | off | `true` at build time shows the canvas in the Artifacts library |

The Design flag is independent of `artifact_projects`. When it is off, every
Design route answers `404`. The server also needs a `node` executable on its
`PATH`; without it, Design operations answer `503` and nothing is changed.

## Two kinds of state

| State | Where | What it is |
|---|---|---|
| Live document | `artifact_design_documents` | The current Yjs state (at most 4 MiB), a `revision` counter and the last `saved_revision`. |
| Versions | `artifact_versions` and related tables | Immutable copies of the live state as the single file `design.yjs` (`application/vnd.yjs-update`). |

Creating a Design writes both: an empty live document and version 1 with the
message `Create Design`.

## How a change is applied

Every change, whether it arrives over the websocket, through the REST API or
from an agent tool, follows the same path on the server:

1. Open a database transaction behind the shared writer fence.
2. Check the caller's current credentials, workspace membership and Page Edit
   permission.
3. Read the live state and apply the change in a short-lived Yjs engine. The
   engine has no database, network or credentials and runs for at most five
   seconds.
4. Validate the whole resulting document (see [Limits](#limits)).
5. Check permissions again, write the new state, increase `revision` and commit.
6. Send the resulting Yjs update to every connected browser.

A change that fails validation or permission checks is not written and not
broadcast. Connected browsers only ever receive committed state.

## Operations and snapshots

There are two ways to change a Design.

**Live sync** over the websocket carries standard Yjs updates. Concurrent edits
are merged by Yjs; there is no version check and no conflict for the user to
resolve. Live sync changes only the live document. While an editor is
connected, the server saves the live document as a version with the message
`Automatic Design checkpoint` once it has unsaved changes and has been idle for
10 seconds.

**Operations** through `POST /api/v1/pages/{slug}/design/operations` are a list
of node changes:

- `upsert` with a `node` (`id`, `type`, `name`, `x`, `y`, `width`, `height`,
  `fill`, optional `parentId`, `text`, `src`). A new node needs its type and
  full geometry; an existing node changes only the fields supplied.
- `delete` with an `id`.
- `reorder` with a complete `order` array of node IDs.

The operations are applied to the current live state, so the request carries no
`base_version`. Each successful request publishes a new version and updates the
live document in the same transaction. If the live document changed between the
server reading it and committing, the request answers `409` with "Design
changed; reload and reapply" and nothing is written.

**Snapshot** (`POST .../design/snapshots`) saves the current live state as a
version without changing it.

**Restore** (`POST .../design/restore` with `version`) changes the live document
to match an earlier version and publishes that as a new version. The restore is
applied as a Yjs update to the existing document, so connected browsers stay
connected and see the result. History is never deleted.

**Share** (`POST .../design/share` with `version`) pins a version for readers.
Only the Page owner can share; an Edit grant does not allow it.

## Websocket

`GET /api/v1/pages/{slug}/design/ws` upgrades to a websocket that speaks the
standard `y-protocols` sync and awareness messages in binary frames.

- The request must come from the same origin as the server; a foreign browser
  origin gets `403`.
- The browser passes the `authority_revision` it received from
  `GET .../design`. If permissions changed since then, the upgrade is refused.
- Every two seconds, and before every frame it sends, the server checks the
  connection's permissions again. When they no longer hold, it closes the
  socket with code `1008`.
- Only editors may send document updates. A reader who sends one is
  disconnected.
- Awareness (cursors and presence) is limited to one client ID per connection.
  The server replaces the user name and ID in awareness with the authenticated
  user.
- Incoming messages are limited to 4 MiB. A client that falls 64 frames behind
  is disconnected. The server pings every 20 seconds and closes a connection
  that is silent for 60 seconds.

The browser opens a fresh socket every five minutes after rechecking its
permissions, keeping the local document and any unsent changes.

## Who can do what

| Action | Required |
|---|---|
| Create | A human with Page creation permission. Agents cannot create a Design. |
| Read live canvas, open the websocket | Read access to the Page. |
| Edit (live sync, operations, snapshot, restore) | Edit access to the Page, or an admitted agent run for this Page. |
| Comment | Edit access, or an explicit Comment grant. |
| Share a version | The Page owner. |

Readers see the live canvas read-only and receive live updates. The version
list and comments they see are limited to the shared version, and they can open
only that version by number. Tokens need `pages:read` for reads and
`pages:write` for changes.

## Agents

Agents use the MCP tools `design_get`, `design_apply`, `design_snapshot`,
`design_versions` and `design_restore`. They reach the same server path as
people, under the exact Page chosen by the human who started the run. An agent
applying operations does not need an open browser; people who have the canvas
open see the change immediately. When the run was started from a comment, its
first successful operations request answers that comment.

The CLI equivalents are the `crewship page design` commands; see the
[Page CLI](../cli/page.mdx).

## Limits

| Limit | Value | Result |
|---|---|---|
| Operations per request | 500 | `400` |
| Nodes | 2000 | `400` |
| Entries in `order` | 4000 | `400` |
| Encoded document | 4 MiB | `400` |
| Node types | `frame`, `rect`, `text`, `image` | `400` |
| Coordinates and sizes | finite, absolute value at most 1,000,000; sizes not negative | `400` |
| `text` / `src` / `name` | 100,000 / 2,000,000 / 2000 characters | `400` |
| Operations request body | 4 MiB | `413` |
| Other request bodies | 8 KiB | `413` |
| Version history | 256 MiB per Design | `413` "Design capacity is full; existing work is retained" |

Validation errors answer `400` with "invalid Design update" and change nothing.

## Routes

All routes require authentication and workspace membership. `{slug}` is the
Page slug, or `@<page-id>`.

| Route | Purpose |
|---|---|
| `POST /api/v1/pages/design` | Create (`slug`, `name`). `201`. |
| `GET /api/v1/pages/{slug}/design` | Live nodes, order, `can_edit`, `can_comment`, versions and `authority_revision`. Optional `version`. |
| `GET /api/v1/pages/{slug}/design/versions` | Version list. |
| `GET /api/v1/pages/{slug}/design/ws` | Live websocket. |
| `POST /api/v1/pages/{slug}/design/operations` | Apply operations and publish a version. |
| `POST /api/v1/pages/{slug}/design/snapshots` | Save the live state as a version. |
| `POST /api/v1/pages/{slug}/design/restore` | Restore a version as a new version. |
| `POST /api/v1/pages/{slug}/design/share` | Pin the shared version (owner). |
| `GET /api/v1/pages/{slug}/design/comments` | Read node-anchored comments. |
| `POST /api/v1/pages/{slug}/design/comments` | Add a node-anchored comment. |
| `POST /api/v1/pages/{slug}/design/agent` | Connect the owner's private chat. |
