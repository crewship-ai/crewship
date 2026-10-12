# Demo users and RBAC acceptance

`crewship seed --with-users --server URL` provisions four baseline accounts and
40 named capability variants in the seed's selected workspace. The bootstrap
account is that workspace's OWNER. Accounts are real password users, provisioned
through the authenticated membership API; public signup can stay disabled.

To add the English baseline accounts and variants to an existing demo, without resetting resources:

```sh
crewship seed rbac-matrix --server https://your-dev.example --workspace WORKSPACE_ID
```

Both server and workspace must be explicit for this additive command. It needs
an authenticated OWNER/ADMIN and no provider credentials. It creates accounts,
memberships and capability grants, but no crews or model runs. Use it only on a
demo installation. Deployment of a binary alone does not run the seed.

## Accounts

| Email | Initial password | Role |
| --- | --- | --- |
| `admin-baseline@crewship.local` | `rbac-demo-1234` | ADMIN |
| `manager-baseline@crewship.local` | `rbac-demo-1234` | MANAGER |
| `member-baseline@crewship.local` | `rbac-demo-1234` | MEMBER |
| `viewer-baseline@crewship.local` | `rbac-demo-1234` | VIEWER |

For each of `viewer`, `member`, `manager` and `admin`, the seed adds these suffixes:

| Suffix | Explicit capabilities in addition to `chat` |
| --- | --- |
| `routine-run` | `routine.run` |
| `routine-create` | `routine.create` |
| `issue-create` | `issue.create` |
| `page-create` | `page.create` |
| `skill-create` | `skill.create` |
| `credential-create` | `credential.create` |
| `credential-rotate` | `credential.rotate` |
| `credentials-reveal` | `credentials:reveal` |
| `memory-write` | `memory.write` |
| `all` | All nine capabilities above |

The address is `ROLE-SUFFIX@crewship.local`, for example
`viewer-routine-run@crewship.local`. All new RBAC accounts use
`rbac-demo-1234`. Names in Members start with `RBAC` and identify the role and
profile. Baseline accounts retain role-derived capability defaults.

Existing accounts must authenticate with their documented fixture password.
The seed never resets an existing password, changes an existing role or
reapplies capabilities to an existing account. A mismatched role or variant
capability set makes the seed fail visibly; it does not overwrite manual edits.
The full seed stops before crew creation on an incomplete user fixture. The
additive command leaves already-created accounts in place on partial failure.

The roster uses English role/profile labels throughout. The former four
personal-name fixtures are no longer created. Existing legacy memberships are
not automatically deleted by an additive seed; remove them explicitly from
Members when replacing an older demo.

## Workspace visibility

A user sees only workspaces where they have a membership. Older signup-based
fixtures also created personal workspaces where each fixture user is OWNER;
these belong to that user and need not appear for the demo OWNER. The current
provisioning path creates no personal workspace. It preserves historical ones.

After signing in, select the workspace targeted by the seed. Testing inside a
personal workspace tests OWNER access instead of the intended role. Existing
instances can have manually edited roles; a name such as an administrator label does not
establish the user's current authority.

## What the matrix proves

Run the authenticated acceptance test on an isolated migrated database:

```sh
go test ./cmd/crewship -run '^TestAcceptance_DemoRBACMatrix$' -count=1 -timeout 10m
```

On the shared development host, prefix build/test commands with
`crewship-heavy`. The test provisions the shipped account roster, redeems setup
links, logs in with passwords and uses revocable sessions through the production
router and middleware. It verifies workspace visibility, baseline and explicit
variant roles, successful and refused API operations, additive/idempotent
provisioning, cross-workspace denial and live grant/revoke without a new login.
It uses a real chained journal for reveal and transform-only routines without
Docker or provider calls.

Successful cases actually create chats, issues, Pages, credentials and imported
skills, save disabled routine schedules, complete a routine, rotate a credential
or reveal its fixture value. Denied cases require HTTP 403. Skill generation's
authorization is checked with an invalid payload (HTTP 400 after admission);
model generation is deliberately not executed.

Capabilities supplement role gates. A MANAGER with only `chat` still has the
creation/run rights granted by MANAGER. `routine.create` and `routine.run` are
independent for MEMBER/VIEWER accounts. Rotation requires ADMIN or an explicit
`credential.rotate`. Reveal additionally requires a MANAGER-or-higher role,
the workspace reveal switch, an interactive fresh session, scope, a valid
reason and a synchronous audit write; a VIEWER holding `credentials:reveal`
is still refused. The fixture enables reveal only in its isolated test database.

`memory.write` remains a reserved grant: a public memory write
endpoint and `/remember` command are not implemented. The test asserts the
endpoint is absent instead of treating the grant as proof of working memory
writes. Model execution, browser rendering and restricted resource policies are
separate acceptance boundaries.

## Data visibility is a separate policy

The 44 accounts above exercise **action permissions in trusted mode**. A VIEWER
role or a single capability does not mean an account has a resource allowlist.
Trusted workspace members can read ordinary workspace catalogs, including
issues, routines and credential metadata, subject to each resource's own read
policy. Credential metadata access does not authorize revealing secret values.
Chat audience and Page visibility have additional checks.

For a least-privilege visibility test, configure a separate VIEWER membership
with the member resource policy API/CLI. Read its current policy, retain
`membership_id` and `revision`, and replace `mode` with `restricted` and `rights`
with an explicit array. An empty array grants no resources. To test one agent's
private chat directory, grant only `agent.discover` and `agent.chat` using the
wire format below (the operation is the unprefixed name):

```json
[
  {"kind": "agent", "id": "AGENT_ID", "operation": "discover"},
  {"kind": "agent", "id": "AGENT_ID", "operation": "chat"}
]
```

See [member resource policy](../specs/member-resource-policy.md) for the versioned
read/edit/replace commands. Do not overwrite an existing user's manually edited
policy as part of a seed rerun. Restricted membership affects the whole session,
including other workspaces in which the user is trusted.

Verify both the sidebar and direct API URLs: broad issues, credential, Page and
routine endpoints must return 404 in restricted mode. The dedicated restricted
routine/Page catalogs expose only authorized projections. Agent discovery must
exclude ungranted agents; a chat grant must not expose another person's private
conversations. Revoking a grant must deny subsequent requests in the same
session. `TestRestrictedAuthorityAcrossAuthenticatedRoutes` covers these route
boundaries with real authenticated VIEWER sessions, private chat canaries and
live grant revocation. It does not claim granular issue or credential grants:
those broad routes are unavailable to restricted members.
