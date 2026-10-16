# Slides typed persistence foundation

This is the S1a foundation on existing human-owned Pages. It supplies bounded
schema 1 storage, revisions and a human API/CLI. It does not supply a native
viewer/editor, Present, image assets, selected-edit agent admission,
or an Office export gate. The derived Page descriptor is `slides` with
`supported:false` and `reason:viewer-unavailable`; clients must not initialize
another content type over it. A working Slides product requires those later cuts.

Human comments are a separate retained history described below. Native comment UI
is supplied by a later integration; these API/CLI routes do not advertise a viewer.

## Canonical contract

`internal/slides/model.go`, `lib/slides/model.ts` and
`testdata/slides-schema-v1.json` define the shared integer-geometry schema and
versioned hash encoding. The deck ID equals the immutable Page ID. Slides and
elements have globally unique ASCII IDs within a deck. `slide_order` owns
presentation order; canonical serialization sorts the separate slides collection
by ID. Element array order owns paint order. Unicode is preserved without
normalization, including LF and valid surrogate pairs; invalid UTF-8, lone
surrogates, CR/other controls, duplicate/unknown/case-aliased keys and trailing
JSON are rejected. Hashes use SHA256 of the documented domain prefix and
canonical UTF-8 JSON. The cross-language checker executes both Go and TS.

Schema 1 admits text, rectangle/ellipse shapes and rectangular tables. It admits
no image, author HTML/JavaScript, iframe or external resource reference. A deck
has 1–100 slides, at most 1000 total elements, at most 2MiB of canonical JSON and
integer frames 64–4096 pixels. Text is bounded to 32KiB, notes 16KiB and table cells
4KiB in UTF-8. Tables have at most 30 rows and 20 columns, with an aggregate limit
of 2000 actual cells per slide and 10000 per deck, including empty cells across
all tables. Final candidate validation applies after every batch and restore.

## API and CLI

All routes use the normal workspace context and live Page ACL. CLI reads:

```sh
crewship slide read PAGE
crewship slide outline PAGE
crewship slide history PAGE
crewship slide at-rev PAGE REVISION
```

Mutations take an exact JSON request with `--file FILE` or `--file -` for stdin:

```sh
crewship slide initialize PAGE --file request.json
crewship slide patch PAGE --file request.json
crewship slide reorder PAGE --file request.json
crewship slide restore PAGE --file request.json
```

Corresponding reads are `GET /api/v1/pages/{slug}/slides`, `/outline`, `/history`
and `/revisions/{revision}`. Mutations are `POST` under the same Slides path
with `/initialize`, `/patch`, `/reorder` and `/restore`. The generated OpenAPI
catalog also supplies the existing generic CLI/MCP discovery mechanism.
There is no registered internal Slides agent endpoint or supported Slides run
delegation in this cut. Human CLI mutation is not selected-edit agent admission.

Every mutation requires `page_id`, `schema_version`, `base_rev` and
`mutation_id`. Initialization requires revision 0 and a matching full deck.
Existing mutation requests require a whole-head revision CAS. Reorder and
restore additionally require `if_order_version`; patches inserting/deleting
slides require it too. The semantic operation union is `set_element`,
`insert_element`, `delete_element`, `set_notes`, `insert_slide`, `delete_slide`.
Set-element replaces the full element while preserving ID. Element set/delete
preconditions hash the element; insert-element, set-notes and delete-slide hash
the whole slide. Preconditions are **progressive within an atomic batch**:
each hash sees earlier operations. A caller must compute intermediate hashes
when editing the same slide repeatedly. No operation guesses or rebases them.

A stale revision/order/object hash, mismatched immutable Page ID or conflicting
mutation ID returns 409. Deleting/recreating the same slug never retargets an
already prepared mutation. The CLI sends one exact request and never fetches
new state or rewrites the saved Page ID on conflict. Invalid candidates return 400;
retention quota returns 429. A successful receipt acknowledges its own immutable
revision, not necessarily the current head after another writer commits.

## Authority, retention and lifecycle

The Go Store serializes mutations, reads and receipt replays under a SQLite
writer fence. Trusted API authorization checks active unrestricted workspace
membership, the exact session/CLI credential and Page ACL inside that transaction,
and again before commit. Initialization requires the human Page owner. Content
cannot nominate an actor. Future internal run integration must retain the existing
live human-chat principal/delegation and immutable Docs Page scope checks.

Default retention bounds are 128 decks per instance, 1000 revisions, 10000 lifetime
object identities and 64MiB retained canonical revision bytes per Page. Quota
rejection preserves confirmed history. Deleted slide/element IDs remain tombstones
and cannot be reused by insertion; explicit restoration can revive the original
identity. Revision insertion, head, identity reservation and request receipt commit
atomically. The trusted `OnRevision` hook runs after the immutable revision insert
and before receipt/commit in the same transaction; future asset pinning must reject
or commit atomically there.

Database guards exclude legacy artifacts, boards, Docs, project source/live rows
and panels on the same Page in both directions. Normal handlers also fail closed
on Slides. Slides retain a human owner: departure or account erasure requires an
explicit eligible human successor, never an implicit crew fallback. Page deletion
cascades its private Slides state; it does not affect another Page's namespace.

Pages-category backups include heads, every immutable revision, receipts and
identity tombstones. The installation-local writer singleton is regenerated.
Same-identity restore retains receipts subject to live authorization. Fork rewrites
embedded deck IDs and hashes in **every** head/revision while preserving object IDs
inside the new Page namespace. It clears source replay receipts; retained revision
mutation IDs reject historical mutation-ID reuse with 409. `remap_slides.go` is the
single canonical rewrite authority; future image references must join this rewrite
before hash recomputation. Restore rejects malformed canonical data, wrong hashes,
Page binding and row/snapshot schema mismatches before committing any rows.

## Stable human comments

Comments use envelope `schema_version:1` and `anchor_schema_version:1`, both
independent of deck schema; mixed-version retained deck history remains valid. An original
anchor contains `{kind:slide|element,slide_id,element_id?,revision}`; element ID is
required only for an element anchor. Create requires immutable `page_id`,
`schema_version:1`, `base_rev`, `mutation_id`, `anchor` and nonblank `body`.
`anchor.revision` must equal the current `base_rev`; the target must exist there.
Reply requires `page_id`, `schema_version`, `mutation_id`, `if_version`, `body`;
resolve/reopen replaces body with a required boolean `resolved`. All three reject
unknown/case-aliased/duplicate keys, null fields and trailing JSON, within16KiB.

COMMENT access permits create/reply. WRITE access permits resolve/reopen. Read
requires Page read access. Every route explicitly rejects run authority; these
human comments do not grant agent comment capability. Exact live session/CLI
credentials and ACL apply to initial mutations, receipt retries and each list
page. CLI token reads require `pages:read`; mutations require `pages:write`.
Advisory `can_comment`/`can_resolve` flags are captured under the same read fence.
Workspace invalidation emits only `slides.comments.changed` with an empty payload.

Thread human version increments for each immutable comment/reply/resolve/reopen
event, independently of the deck revision. Replying to a resolved thread returns
409; reopening requires WRITE. Comment body is at most10000 UTF-8 bytes. Retention
caps per Page are1000 threads,10000 events and16MiB of event bodies. Quotas reject
new events without pruning history. Events retain opaque trusted actor attribution
without a user/agent/chat foreign key that could erase comment history.

Original anchors never change. Text or geometry changes and reorder preserve
attachment. Target deletion marks a thread `detached`; explicit restore of the
original identity marks it `restored` with `last_missing_revision` and
`restored_at_revision` provenance. There is no fuzzy matching. `projection_revision`
identifies the deck revision used for the current status. Internal projection
updates and the existing trusted external `OnRevision` hook share one transaction;
projection updates do not consume human thread versions.

Thread and event lists use `limit` (default25, maximum100) and an opaque `cursor`.
Responses include `items`, `next_cursor`, `watermark`, `has_more`, `can_comment`,
`can_resolve`. Membership watermarks exclude later-created threads/events until a
fresh list; current permissions are rechecked on every continuation. Clients must
follow `has_more`/`next_cursor` rather than treating the first page as complete.

Pages backups include threads, immutable events and receipts. Original anchor
revisions are pinned with a deferred NO ACTION foreign key; Page/head deletion
still cascades the entire owned graph. Restore validates the original anchor at
its pinned snapshot and contiguous event versions/kinds/count/resolution state,
then reconstructs projection/provenance from actual retained deck history.
Imported projection fields are not trusted. Fork remaps the Page namespace while preserving thread IDs,
keeps stable slide/element anchors and historical event mutation IDs, and clears
comment replay receipts. A historical mutation ID remains a409 tombstone.
