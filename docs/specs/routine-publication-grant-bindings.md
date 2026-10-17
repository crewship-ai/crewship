# Publication grant provenance

The host-only access helpers `CapturePublicationGrantIn` and
`ResolvePublicationGrantIn` prepare exact selected-right provenance for bounded
routine publication. Both use the caller-owned database transaction. They
perform no writes and expose neither an HTTP endpoint nor a new permission.

A binding records the workspace, publisher user and membership IDs, selected
resource/right, grant ID, issuer user ID and issuer membership ID. Capture
requires a live, non-suspended publisher with a restricted membership, an
existing resource in that workspace and the exact explicit share in
`live_access_grants`. Existing trusted `Check` behavior is unchanged; trusted
membership cannot substitute broader role authority for a frozen share.

Resolution requires every recorded identity to remain the same and live.
Removing/reissuing a right or recreating the publisher or issuer membership
cannot substitute a new identity, even when user and resource names match.
Unrelated grant saves and their mutable policy revisions do not change this
binding. No policy revision or display name is part of its identity.

Temporary issuer demotion or suspension denies resolution. Recovery with the
same membership and grant IDs permits a new resolution within the original
bound. It cannot revive already admitted work: execution must still validate
its separate admission fence. These helpers are read-only provenance checks,
not admission handles, a dispatcher or an irreversible run revocation fence.

The compiler must classify authoring receipts separately from execution
bounds. This preparation does not require a publisher root-routine Run grant,
add Publish validation, accept authority claims from task JSON, or expose IDs
through public `GrantDetail` or binding JSON. It does not compile a graph,
persist publication payloads, relax live issuer checks, activate a Guest broker
or establish runtime readiness. Those integrations remain separate work.

Existing [member resource policy](member-resource-policy.md) and
[restricted workflow](restricted-workflows.md) checks remain additional
boundaries. A provenance record alone grants no execution or result access.
