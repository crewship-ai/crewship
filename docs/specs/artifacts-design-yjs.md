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
| Build-time `NEXT_PUBLIC_ARTIFACTS_DESIGN_PROTOTYPE` | off | `true` at build time adds link editing and prototype playback ([Prototype links](#prototype-links)) |
| Build-time `NEXT_PUBLIC_ARTIFACTS_DESIGN_TEXT_STYLE` | off | `true` at build time adds the text style controls in Properties ([Text style](#text-style)) |
| Build-time `NEXT_PUBLIC_ARTIFACTS_DESIGN_HIDE_LOCK` | off | `true` at build time adds the hide and lock buttons in Layers ([Hide and lock](#hide-and-lock)) |
| Build-time `NEXT_PUBLIC_ARTIFACTS_DESIGN_SHAPE_STYLE` | off | `true` at build time adds the shape style controls in Properties ([Shape style](#shape-style)) |
| Build-time `NEXT_PUBLIC_ARTIFACTS_DESIGN_EXPORT` | off | `true` at build time adds the **Export HTML** button ([HTML export](#html-export)) |

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

Every change runs through a short-lived Yjs engine that has no database,
network or credentials. It has two separate budgets: up to 15 seconds to start
(node and the Yjs bundle loading, which measured 27-31 ms idle and at most
306 ms under 4x CPU oversubscription) and then up to five seconds for the
change itself. When either budget runs out the process is killed and the
change is refused as `503 Design engine is unavailable`; the server log names
the phase (`startup` or `work`) that ran out. The engine applies
the change to the live state and validates the whole resulting document (see
[Limits](#limits)). Every database transaction below starts behind the shared
writer fence and checks the caller's current credentials, workspace membership
and Page Edit permission at its start and again before it commits. There are
two paths.

**Live sync** (websocket updates) uses one transaction:

1. Open the transaction and check permissions.
2. Read the live state and run the engine inside the transaction.
3. Write the new state and increase `revision`.
4. Check permissions again and commit.

**Versioned changes** (REST operations, restore, and the agent `design_apply`
and `design_restore` tools, which reach the same handlers) use two
transactions:

1. Read the live state, its `revision` and the latest version in a first
   transaction.
2. Run the engine outside any transaction.
3. In a second transaction of the [artifact version store](artifacts-version-store.md),
   check permissions, check that `revision` has not changed, publish the new
   version and write the live state with `revision` and `saved_revision`
   increased. If `revision` changed in the meantime, the request answers `409`
   and nothing is written.

In both paths the server sends the committed Yjs update only to connections
with live-read authority: editors and the separately audited administrative
read path. Shared-only readers synchronize the shared snapshot and receive
presence, without receiving later live document updates. A change that fails
validation, permission or revision checks is not written or broadcast.

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
  `fill`, optional `parentId`, `text`, `src`, `linkTo` and the
  [text style](#text-style) fields). A new node needs its type and
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
applied as a Yjs update to the existing document. Connected browsers authorized
for live reads receive the result; shared-only readers keep their shared
snapshot and presence. Changing the shared pin does not push replacement
document state to an already-open shared-only socket; read again or reopen. Separately authorized
workspace OWNER/ADMIN live reads remain audited for foreign Designs, including
with a read-scoped credential, which grants no write authority. History is
never deleted.

**Share** (`POST .../design/share` with `version`) pins a version for readers.
Only the Page owner can share; an Edit grant does not allow it.

## Prototype links

A layer may carry `linkTo`, the ID of the screen (`frame`) that a click on the
layer opens in prototype playback (#4449). It is a plain string field of the
node's `Y.Map`, like `parentId`:

- A document without `linkTo` is a document without links; nothing else
  changes. Clients that predate the field keep it untouched when they edit
  other fields, and restore copies it with the rest of the node.
- The browser removes a link by deleting the key. REST and agent callers cannot
  send `null`, so they send `""`, which also means "no link".
- The server validates only that `linkTo` is a string of at most 128
  characters. A target that does not exist or is not a screen is stored as is
  and plays as an inert hotspot, because the target screen may be deleted at
  any time by another person.
- Setting a link is an edit and needs Edit access. Playback is local browser
  state: it never sends a Yjs update, operation or awareness change of its own,
  so Read and Comment callers can play.

The server accepts `linkTo` regardless of `NEXT_PUBLIC_ARTIFACTS_DESIGN_PROTOTYPE`,
which only hides the editor controls. A server without this field refuses any
document that contains it, so deploy the server before turning on the UI
variable.

## Text style

A text layer may carry these optional fields (#5161). Each one takes only the
listed values; the server refuses anything else with `400`, so no other value
reaches the canvas or an exported style attribute.

| Field | Values | Absent means |
|---|---|---|
| `fontFamily` | `sans`, `serif`, `mono` (fixed system font stacks, no web fonts) | `sans` |
| `fontWeight` | 100 to 900 in steps of 100 | 400 |
| `fontSize` | 1 to 1000 (pixels) | 20 |
| `lineHeight` | 0.5 to 5 (multiple of the size) | 1.3 |
| `textAlign` | `left`, `center`, `right` | `left` |
| `textWrap` | `none` (lines break only at a newline), `wrap` (lines break at the box width; a long word breaks too) | `none` |
| `overflow` | `visible` (text may run past the box), `clip` (cut at the box) | `visible` |

- A layer without these fields looks exactly as before they existed, on the
  canvas, in prototype playback and in the HTML export.
- Clients that predate the fields keep them when they edit other fields, and
  restore copies them with the rest of the node. Such clients draw the layer in
  the old default style.
- A server without the fields refuses any document that contains them; such a
  Design would not open until the server is updated again. So the change ships
  in two steps. This release: the server accepts and draws the fields, but the
  **Properties** controls stay behind the build-time
  `NEXT_PUBLIC_ARTIFACTS_DESIGN_TEXT_STYLE` (default off), and the agent tool
  tells agents to leave the fields out unless asked. A later release turns the
  controls on once no rollback past this server is expected. Before enabling the
  variable, deploy a server with #5161 everywhere the client runs.
- Only editors change the fields, in **Properties** (when enabled) or through
  `upsert`.

### Hide and lock

Any layer or screen may carry `hidden` and `locked` (#5218), each `true` or
`false`; the server refuses any other value with `400`. Absent means shown and
unlocked, so older documents are unchanged.

- `hidden`: the layer, and everything inside a hidden screen, is not drawn on
  the canvas, in prototype playback or in the HTML export. It stays in
  **Layers** (dimmed) and keeps its comments.
- `locked`: the layer, and everything inside a locked screen, is drawn but a
  click or drag on the canvas passes through it, a marquee leaves it out, and
  the arrow keys and **Delete** do not act on it. It can still be selected
  and edited in **Layers** and **Properties**.
- Both are document state: every viewer sees them, and undo takes them back
  like any other edit.
- A hidden screen is skipped by next and previous artboard, the artboard
  picker, **Fit** and **Play** (#5228); it is still counted when a new screen
  is named and placed.
- Rollout is the same two steps as text style: this server accepts and
  honours the fields, and the **Layers** buttons stay behind the build-time
  `NEXT_PUBLIC_ARTIFACTS_DESIGN_HIDE_LOCK` (default off) until no rollback past
  this server is expected. A server without #5218 refuses a document that
  contains the fields.

### Shape style

Rectangles and images may carry these optional fields (#5219); the server
refuses other values with `400`.

| Field | Values | Absent means |
|---|---|---|
| `radius` | 0 to 10000 (corner radius in pixels) | 6 for a rectangle, 0 for an image |
| `stroke` | `#rgb` or `#rrggbb` | no stroke |
| `strokeWidth` | 0 to 1000 (pixels) | 1 when `stroke` is set |

- The stroke is drawn inside the box on the canvas, in playback and in the HTML
  export (an outline drawn inwards, above an image), so a stroke never changes a layer's size or
  position. A rounded image is clipped to its corners.
- `fill` is the paint of a rectangle. On an image it is painted behind the
  picture (#5228), on the canvas and in the export, so a transparent image
  shows it; an image without a `fill` has no backing.
- **Remove stroke** in Properties deletes `stroke` and `strokeWidth` from the
  layer, so it is drawn as if it never had one.
- Rollout is the same two steps as text style, behind the build-time
  `NEXT_PUBLIC_ARTIFACTS_DESIGN_SHAPE_STYLE` (default off).

## HTML export

`GET /api/v1/pages/{slug}/design/export?format=html&version=N` renders one
immutable version as a single self-contained HTML file (#4449). The server
generates it in Go, so the browser button and `crewship page design export`
receive the same bytes. There is no separate server flag: the route follows the
Design workspace flag like every other Design route, and
`NEXT_PUBLIC_ARTIFACTS_DESIGN_EXPORT` only hides the button.

- **Source.** Always a retained version, never the live document. `version`
  selects it; without it, a caller who may read history gets the latest
  version and a shared-only caller the shared version. The version's
  `design.yjs` runs through the same engine validation as every read.
- **Access.** The canonical Design read rule shared with `GET .../design` and
  `GET .../design/versions`: current Edit authority (owner or Edit grant, with
  a browser session or a CLI credential that also carries `pages:write`) or a
  workspace OWNER/ADMIN (also with `pages:read` alone) reads any retained
  version; everyone else the read admits, including Read and Comment audiences
  and owners or Edit grantees narrowed to `pages:read`, only the shared
  version. Every other case, including an unshared version, a missing version,
  a non-member or another workspace, answers exactly as `GET .../design` does.
  Authority is checked under the writer fence before and after rendering; a
  workspace OWNER/ADMIN's export of a foreign Design is audited as
  `design.workspace_admin_accessed` with operation `export`.
- **Output.** One HTML document with a meta Content-Security-Policy
  `default-src 'none'; style-src 'unsafe-inline'; img-src data:` and no script.
  Every screen (`frame`) is a section `screen-1`, `screen-2`, … in document
  order; each non-screen layer is drawn on its nearest screen with the same
  geometry as the prototype player (positions summed through `parentId`, sizes
  at least 1). Text and names are HTML-escaped; a `fill` that is not plain color
  syntax falls back to the default paint. Only raster `data:image/(png|jpeg|webp|gif);base64`
  images are embedded; other image sources are never fetched and appear as a
  placeholder. A layer whose `linkTo` names an existing screen becomes
  `<a href="#screen-N">`; a link to a missing screen is drawn with a dashed
  outline and no target.
- **Response.** `text/html; charset=utf-8`, `Content-Disposition: attachment;
  filename="<slug>-design-v<N>.html"`, `X-Crewship-Design-Version: <N>`, and the
  same policy plus `sandbox` as a response header. Apart from the Design's
  current name in the title and heading, the output depends only on the
  version, so the same version under the same name always exports to the same
  bytes; renaming the Design changes only those two places.
- **Limit.** An export over 8 MiB answers `413` with code
  `design_export_limit`. Unknown parameters, a repeated parameter, a format
  other than `html` or a malformed `version` answer `400`.

Agents have no export tool: the agent MCP channel returns bounded JSON, and a
run can already read any version it may see with `design_get`.

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
- Awareness (cursors, selection and presence) is limited to one client ID per
  connection. `cursor` is a canvas point; `selection` is the selected layer ID
  (1 to 128 characters), a list of 1 to 200 such IDs for a multi-selection
  (#5217; clients send one layer as a plain ID, so older peers keep drawing it),
  or `null`. Both are view state and are never written
  to the document; a selection naming a missing layer draws nothing.
- `selection` is live-canvas state. The server drops it from awareness sent by
  a person who cannot read the live canvas, and removes it from the awareness
  it delivers to such a person, so a shared-version reader never learns layer
  IDs of the unshared draft. Any other value is dropped. Clients share a
  selection only from an editable live canvas, not from History.
  The server replaces the user name and ID in awareness with the authenticated
  user.
- Incoming messages are limited to 4 MiB. A client that falls 64 frames behind
  is disconnected. The server pings every 20 seconds and closes a connection
  that is silent for 60 seconds.

The browser opens a fresh socket every five minutes after rechecking its
permissions, keeping the local document and any unsent changes.

Each tab holds its own document, so the local canvas draft is one sealed row
with one entry per tab: that tab's full document state and the confirmed canvas
it was made from. An entry and its base are one write, and a tab replaces only
its own entry, under the cross-tab draft lock. After an identity change a tab
that cannot open the row adds a sealed part, and the owner's next load combines
the parts. The row has its own kind, `design-tabs`. The `design-canvas` and
`design-pending` rows of older builds are only read, so a rollback finds no draft
rather than a format it cannot open: a bare state counts as one more entry, and a
mark supplies its base.

Reopening merges every entry in a fixed order and turns what the merge hides into
a conflict notice, so a notice exists only for texts that were really stored. An
entry goes only from the tab that wrote or recovered it, when a confirmed document
holds it and every notice that tab found is stored. Causal coverage cannot speak
for an entry nobody examined, because it includes text a merge dropped, so that
entry waits for the next reopen.

A version save has five seconds from the call, on the monotonic clock, to get a
sync request through the client's copy of the server's bucket and receive the
reply, including any wait behind earlier saves. Closing the socket, closing the
editor or the caller's `AbortSignal` ends it at once, and the work stays
unconfirmed in the browser.

## Who can do what

Live-read/Edit authority means the effective authority of this request, not
only the Page ownership or grant label. Human CLI reads need a read-capable
credential: `pages:read`, `pages:*`, `*`, or an unscoped full-role token;
write scope alone does not imply read scope. Live/history reads need both
`pages:read` and `pages:write` (or `pages:*`, `*`, or an unscoped full-role token) plus current Edit permission, or
current Edit permission through a browser session. Ordinary Read/Comment
callers and non-admin Page owners/Edit grantees using only `pages:read` are
shared-only. Separately authorized workspace OWNER/ADMIN readers can read
latest/history even with read scope, with audit for foreign Designs; that
credential still cannot write. Agent tools retain their separate exact-run
mandate, original human credential and live authority checks; these human CLI
scope examples do not replace agent credential validation. Every credential
form still requires live membership and current Page permission; a wildcard
or unscoped full-role token does not confer Edit by itself.

| Action | Required |
|---|---|
| Create | A human with Page creation permission. Agents cannot create a Design. |
| Read canvas, open the websocket | Read access to the Page; shared-only readers are limited to the shared snapshot and presence. |
| Edit (live sync, operations, snapshot, restore) | Edit access to the Page, or an admitted agent run for this Page. |
| Comment | Edit access, or an explicit Comment grant. |
| Share a version | The Page owner. |
| Export a version as HTML | Same as reading that version: Edit authority (a browser session, or a CLI credential that also carries `pages:write`) or a workspace OWNER/ADMIN (also with `pages:read` alone) for any version, otherwise only the shared version. Human authentication only. |

Shared-only Read and Comment grantees receive the immutable shared snapshot
and presence, not unsaved live canvas updates. Their version list and comments
are limited to the shared version, and they can open only that version by
number. Workspace administrators using the separately audited read path can
still read live state; a read-only token does not gain write permission. Narrowed
tokens are checked for `pages:read` on reads and `pages:write` on changes;
matching wildcards and unscoped full-role alternatives apply as described above.

Snapshot access and presence do not establish live editing or a collaboration
acceptance result. Live multiuser behavior must be qualified separately on the
enabled deployment and its supported clients.

## Agents

Agents use the MCP tools `design_get`, `design_apply`, `design_snapshot`,
`design_versions` and `design_restore`. They reach the same server path as
people, under the exact Page chosen by the human who started the run. An agent
applying operations does not need an open browser; connections with live-read
authority receive the committed update. Shared-only readers keep the shared
snapshot until the owner shares a different version. When the run was started from a comment, its
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
| `parentId` / `linkTo` | 128 characters | `400` |
| Text style fields | the values in [Text style](#text-style) | `400` |
| Operations request body | 4 MiB | `413` |
| Comment request body (`POST .../design/comments`) | 20,000 bytes | `413` |
| Create, snapshot, restore, share and agent request bodies | 8 KiB | `413` |
| Exported HTML file | 8 MiB | `413` `design_export_limit` |
| Version history | 256 MiB per Design | `413` "Design capacity is full; existing work is retained" |

Validation errors answer `400` with "invalid Design update" and change nothing.

## Routes

All routes require authentication and workspace membership. `{slug}` is the
Page slug, or `@<page-id>`.

| Route | Purpose |
|---|---|
| `POST /api/v1/pages/design` | Create (`slug`, `name`). `201`. |
| `GET /api/v1/pages/{slug}/design` | Nodes, order, `can_edit`, `can_comment`, `version`, `latest_version`, `shared_version`, `actor` and `authority_revision`. Optional `version`. The version list is at `GET .../design/versions`. |
| `GET /api/v1/pages/{slug}/design/versions` | Version list. |
| `GET /api/v1/pages/{slug}/design/export` | One retained version as a self-contained HTML file. |
| `GET /api/v1/pages/{slug}/design/ws` | Live websocket. |
| `POST /api/v1/pages/{slug}/design/operations` | Apply operations and publish a version. |
| `POST /api/v1/pages/{slug}/design/snapshots` | Save the live state as a version. |
| `POST /api/v1/pages/{slug}/design/restore` | Restore a version as a new version. |
| `POST /api/v1/pages/{slug}/design/share` | Pin the shared version (owner). |
| `GET /api/v1/pages/{slug}/design/comments` | Read node-anchored comments. |
| `POST /api/v1/pages/{slug}/design/comments` | Add a node-anchored comment. |
| `POST /api/v1/pages/{slug}/design/agent` | Connect the owner's private chat. |
