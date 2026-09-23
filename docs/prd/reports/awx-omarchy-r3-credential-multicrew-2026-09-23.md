# R3 — Routine credential access for multiple crews

Issue: [#2676](https://github.com/crewship-ai/crewship/issues/2676). This is a follow-up to the AWX/Omarchy release-1.0 credential-dependents work.

## Problem and decision

Credential visibility and sidecar delivery use `credential_crews`, while routine `credential_ref.type` and `credentials_required` used the legacy `credentials.crew_id`. A credential assigned to two crews was usable by both crews' agents, but only the first crew's routine. The junction is now authoritative for routine crew grants. `crew_id` remains a compatibility field, not a grant. An unlinked workspace credential remains eligible only with `scope = WORKSPACE` and an empty legacy `crew_id`; this prevents inconsistent crew rows from becoming workspace-wide secrets. Links to deleted or foreign-workspace crews do not grant routine access.

The resolver still chooses a linked credential before a workspace credential, then the newest ACTIVE row by creation time and ID. Both the resolver and the non-decrypting availability probe use the same candidate query. Endpoint-backed provider credentials remain excluded.

Legacy `PATCH credential {"crew_id": ...}` updates `credential_crews` in the same transaction; null or empty clears the grants and selects workspace scope. A single legacy crew ID intentionally replaces any prior multi-crew assignment, matching the meaning of that field. Clients that need multiple crews should use `crew_ids`.

## Validation contract

- A fixture test grants one credential to two crews, removes one link, deletes a crew, and verifies both resolver and probe deny the revoked crew. It also proves workspace fallback and no grant from `crew_id` alone.
- A migrated-database test confirms the second crew's routine, agent and human member all reach the same credential, while a third crew reaches none.
- A handler test changes and clears grants through legacy `crew_id`, checking scope, compatibility field, and junction rows after each request.
- Run the full Go test suite, vet, documentation gates, and migration lint before merge. This change has no frontend or schema migration.

Existing long-lived agent containers may retain plaintext already delivered before grant revocation; this change governs subsequent routine resolution and future agent delivery. Historical run provenance is not reclassified.
