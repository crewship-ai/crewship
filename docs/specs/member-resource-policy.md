# Member resource policy: API and CLI

This is an administration surface for the authority introduced in #2717, not
activation of restricted model execution. An operator can explicitly restrict a
member and edit individual agent/project operations. Unintegrated routes remain
denied; the policy does not bypass roles, token scopes or conversation audience.

Only a **current trusted OWNER/ADMIN** of the target workspace may read or replace
these policies. Scoped CLI tokens additionally require `workspace:admin` (or its
wildcard). Restricted administrators cannot administer policy. OWNER/ADMIN members
cannot be changed to restricted mode by this endpoint. The current global
restricted-user boundary also applies to users with mixed trusted/restricted
memberships: adding a restricted membership closes unintegrated routes across
their session, including administration of another workspace.

Routes:

- `GET /api/v1/workspaces/{workspaceId}/members/{memberId}/access`
- `PUT /api/v1/workspaces/{workspaceId}/members/{memberId}/access`

The path uses the membership row ID. CLI commands accept either that ID or the
user ID and resolve it through the workspace roster:

```sh
crewship --workspace WORKSPACE --format json workspace member access get MEMBER > policy.json
# Edit only mode and rights, retaining membership_id and revision.
crewship --workspace WORKSPACE --format json workspace member access set MEMBER --file policy.json
# --file - reads the complete replacement from stdin.
```

The replacement body is the same shape as the GET response:

```json
{
  "membership_id": "MEMBERSHIP_ROW_ID",
  "mode": "restricted",
  "revision": 1,
  "rights": [
    {"kind": "agent", "id": "AGENT_ID", "operation": "chat"}
  ]
}
```

`rights: []` explicitly revokes all resource grants in restricted mode. Omitted
or null rights are rejected, as are unknown body fields. `trusted` requires an
empty rights array and restores ordinary workspace access; it is not a narrower
client setting. The actor always comes from authentication, not JSON.

Every accepted replacement advances the membership revision. Treat it as an
opaque version: grant/role triggers can advance it by more than one. A stale revision
returns 409 and the CLI neither refreshes it silently nor retries the update.
Removing/re-adding a member changes its membership ID; an old policy must not be
reused for the new membership. Membership, grants and administration authority
are checked transactionally by `access.Store`; old attempts do not regain rights
when the same grants are later restored.

This does not yet supply a Settings editor, a production provider, scoped
prompt/recall, persistent quotas, or restricted chat/CLI/routine dispatch. Granting
`run` does not make those unintegrated execution routes available. Release 1.0
acceptance remains open.

## What a restricted session's shell offers

For an account with a restricted membership, every row of `GET /api/v1/workspaces`
carries `restricted_session: true` and `restricted_surfaces`: the screens the
session may open, derived by the server from its route allowlist
(`internal/api/restricted_surfaces.go`). A surface is listed only when **every**
route it needs is allowlisted. Today the list is `chat`, `routines`, `pages` and
`account_security`. A trusted account's rows carry neither field.

The web app shows only those screens in the sidebar, the phone menu and the phone
tab bar. Any other URL shows "Not available with your access" without mounting
the screen, so none of its requests are sent. `/` goes to Chat. Settings shows
only account security: password, browser sessions and revoking CLI tokens.
Profile edits and issuing new tokens are not on the allowlist. A restricted row
without `restricted_surfaces` allows nothing (fail closed).
