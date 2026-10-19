# Native Slides ordering and retained revisions

This cut reuses Store Reorder, Restore and History from #3229 commit
`f0a756d7151b2aa6431fb295416fa3056c1627f7` (39, 41 and 22 source lines).
The shared initialized/Patch store remains authoritative for the writer fence,
current principal checks, request hashes, receipts, revision quotas, immutable
writes, object identity reservations and real image revision hooks.

Reorder binds Page/schema/revision/order conditions to an actor-bound request.
It retains all objects and changes only ordering. Exact acknowledged retries
return their original revision before quota/CAS checks; changed requests or
actors cannot reuse the receipt. Restore explicitly copies a retained revision
into a new child of the current head, preserving lineage and historical IDs.
It never rewinds the head or bypasses current conditions or attachment admission.
Legacy schema 1/2 are retained, including explicit restore to an older schema.
No missing image hook is replaced by a successful no-op.

History returns descending metadata bounded by MaxRevisions, after checking the
current head inside the same authorized transaction. The extraction adds one
fail-closed correction: any transaction/authorization error discards collected
history metadata. Every caller must discard all operation results on any error;
a candidate receipt is not confirmation before the fenced transaction commits.

Only these methods, focused SQLite regressions and this README are included.
Existing Read(revision) is reused. No API/router, schema migration, capability
activation, UI, format decision, backup helper or comment model is added.
The image rejection test deliberately has unpinned retained metadata and no real
asset hook; it verifies rejection and does not claim admitted asset behavior.

Focused checks use crewship-heavy and select TestVersions before focused vet.
They cover durable order/restore lineage, retries at quota, actor/hash and CAS
conditions, identity reservations, auth rollback/no history disclosure, bounded
history, close/reopen and missing image hooks. Broader API/CLI/browser/model and
Office acceptance remain separate gates; these tests do not establish them.
