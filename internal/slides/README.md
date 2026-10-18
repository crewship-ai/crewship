# Slides storage model

This package defines bounded typed deck data, strict JSON decoding, canonical
encoding and content hashes. Backup restoration uses these rules to check stored
snapshots and rewrite embedded Page identity without changing slide or element
identities.

The model recognizes schema 1 and explicit schema 2 image metadata. It does not
provide image storage or transport, a write service, an editor, an API or a CLI.
The shared fixture pins canonical schema 1 bytes and their hash. Unsupported
versions, unknown fields and invalid structures fail validation.

## Typed operation contracts

`DecodeInitialize`, `DecodePatch`, `DecodeReorder` and `DecodeRestore` strictly
parse request identities, known fields and schema versions. Initialization
binds the stable Page identity to the deck identity. Patch operations target
existing slide/element IDs with hashes; structural edits require an explicit
order precondition in the request. Schema 1 to 2 image attachment additionally
requires an explicit target schema and original content hash.

`ApplyPatch` builds and validates an independent candidate from a decoded
request. It retains existing IDs, checks operation target hashes in sequence,
and rejects an invalid batch without changing the input deck. It validates the
complete resulting deck against the same canonical model and aggregate budgets
used by backup restoration. `ObjectIDs` and `SlideHash` expose existing identity
and hashing rules to a future persistence adapter.

These are pure content operations, not a write service. Callers must first use
the strict decoder, then verify current transactional Go authorization, immutable
Page identity, base revision and order version before committing. `ApplyPatch`
does not enforce base revision or order-version CAS, receipt replay, historical
ID tombstones, asset access/pins or retention. A future store must enforce those
conditions and persist revision, receipt and identity changes atomically.
Reorder and restore decoders do not themselves reorder or restore stored data.

This extraction adds no schema, migration, format selection, agent tool, API,
editor, Present or export path. The existing schema1/2 model and backup lineage
remain unchanged. Tests cover strict initialization, bounded targeted edits,
atomic rejection, identity preservation and explicit image upgrade fencing;
they do not constitute model/browser/Office or complete S0 acceptance.

## Durable initialization and reads

`Store.Initialize` and `Store.Read` use the shipped Slides tables. A fenced
transaction checks live Go authorization before receipt lookup and again before
commit, requiring the same authorized actor both times. Initialization retains
one immutable revision, its request-bound receipt and persistent slide/element
identity reservations together. Identical authorized retries return the original
receipt; changed actor or request content conflicts. Reads validate the stored
schema, stable Page identity and canonical content hash.

Deck-count admission is per workspace. Revision/history-byte and retained-ID
budgets remain per deck. Capacity rejection and revision-hook rejection roll
back heads, revisions, receipts and identity reservations together. Images
require a real `OnRevision` hook that validates access and pins references in
the same transaction; absence of the hook rejects image initialization.

This cut supplies initialization/read persistence only. It does not connect a
public handler, create a Page, infer authorization from crew membership, enable
agent authoring, edit a saved deck, implement comments or select an S0 format.
The caller supplies the current Go authority and authorized asset-pin hook.
