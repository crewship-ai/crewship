# Iteration 9 handoff: credential dependents

Issue #2674 implements A2 on top of R3 (#2676). It adds a read-only
`GET /api/v1/credentials/{credentialId}/dependents` route and
`crewship credential dependents <name-or-id>`. The scanner includes declared
`credentials_required`, HTTP `credential_ref.type`, and supported
`{{ secrets.<type> }}` templates in steps, hooks and foreach bodies. For each
visible routine, it compares the credential ID selected today by the same SQL
ordering used by the runtime resolver. It never decrypts the value. The UI
links to routines, shows the distinction between configured reference and
current selection, and shows known dependents before deletion and when edit
changes scope/crews. Failed reads are marked unknown, not empty.

`recorded_use: not_attributed` is intentional: credential `last_used_at` does
not identify a routine run, so it cannot prove which run consumed the value.
Dynamic agent and script lookups remain outside static analysis. Non-managers
only receive workspace-visible routine names. Hidden routines cannot be
counted in their response and `visibility_limited` says so.

R3 makes `credential_crews` authoritative for the resolver, probe, and this
read-only selection preview. The preview therefore includes any live crew
grant, including a second crew, and does not treat legacy `crew_id` alone as
access. Agent delivery and human visibility already used the junction; R3
tests the parity on a migrated database.

Validation: targeted Go tests cover scanner nesting, resolver/preview parity,
visible versus hidden routines, selection precedence, and deleted credential;
frontend tests cover dependency links, failed reads and changed-scope impact.
OpenAPI schema and strict documentation gate are part of the same PR.
