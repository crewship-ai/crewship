# Iteration 9 handoff: credential dependents

Issue #2674 implements A2 on `main` at `3f0efa4d8`. It adds a read-only
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

The resolver still uses legacy `credentials.crew_id`; the separate
`credential_crews` delivery model is not silently substituted. R3 must decide
the desired multi-crew semantics and then change resolver, probe, visibility
and sidecar together with parity tests. This iteration reports the current
runtime behavior, including its limit, and does not change secret delivery.

Validation: targeted Go tests cover scanner nesting, resolver/preview parity,
visible versus hidden routines, selection precedence, and deleted credential;
frontend tests cover dependency links, failed reads and changed-scope impact.
OpenAPI schema and strict documentation gate are part of the same PR.
