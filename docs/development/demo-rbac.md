# Demo RBAC accounts

`crewship seed --with-users --server URL` creates four baseline accounts plus
40 named capability profiles in the selected demo workspace. The bootstrap
OWNER remains `demo@crewship.ai`, with default password `password123` unless
overridden. Public signup can stay disabled: accounts are provisioned through
authenticated member administration and new-account setup redemption.

To add the roster to an existing demo without reseeding its resources:

```sh
crewship seed rbac-matrix --server https://your-dev.example --workspace WORKSPACE_ID
```

Both target flags must be explicit. The command requires workspace OWNER authority because it includes ADMIN
profiles, and no provider credentials. Deploying a binary alone
does not run it. Use these documented credentials only on demo installations.

## Initial credentials

All 44 new accounts use password `rbac-demo-1234`. The four baseline emails are
`admin-baseline@crewship.local`, `manager-baseline@crewship.local`,
`member-baseline@crewship.local` and `viewer-baseline@crewship.local`. Their names
identify their role and baseline profile; capabilities retain role defaults.

For each role, nine single-capability profiles and one full profile use
`ROLE-SUFFIX@crewship.local`, for example `viewer-routine-run@crewship.local`.

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

Capabilities supplement role authority; profile names do not restrict the
rights a role already grants. Resource sharing, runtime installation, reveal
policy and other admission gates still apply. `memory.write` is reserved and
does not establish a public memory-write operation.

## Re-running safely

Only confirmed newly created accounts receive a password and explicit profile.
Existing accounts must authenticate with the documented fixture password; their
passwords, roles and grants are preserved. Drift causes a visible failure. The
full seed stops before crew creation if the roster is incomplete; the additive
command keeps already-created accounts on partial failure. Explicit HTTP 429
responses retry with the same body and honor cancellation; ambiguous network
or server failures are not retried.

Legacy personal-name accounts are no longer created and are not automatically
removed. Existing personal workspaces are preserved. Select the target demo
workspace after signing in; a personal OWNER workspace tests different rights.

The roster inventory, explicit target guards, request replay and preservation
checks run with the CLI unit tests. Production-router acceptance coverage is described below.

## Authenticated acceptance boundaries

```sh
go test ./cmd/crewship -run '^TestAcceptance_DemoRBACMatrix$' -count=1 -timeout 15m
go test ./internal/api -run 'TestTenWorkspaceHTTPIsolation|TestSettingsAuthorityAndLiveMembershipHTTP|TestRestrictedAuthorityAcrossAuthenticatedRoutes' -count=1 -timeout 15m
```

On the shared development host, prefix these commands with `crewship-heavy`.
The matrix logs in with real passwords and revocable sessions through the
production router. It exercises all 44 fixture profiles plus OWNER, successful
and denied API operations, the additive CLI, idempotency and existing grant
drift. Success cases create chats, disabled schedules, issues, Pages, credentials
and imported skills, complete a transform-only routine, rotate a fixture value
and reveal it through the actual audit path. Generation authorization is checked
with invalid input; no model generation or Docker execution is claimed.

The HTTP isolation suite creates ten workspaces and checks all 90 directed
foreign-workspace pairs with positive own-workspace controls. Settings coverage
reuses the same authenticated sessions after capability revocation, role
changes and membership removal. Restricted VIEWER and MEMBER fixtures retain
private chat isolation, exact granted discovery, closed broad catalogs and
live revocation.

These tests establish API authority. They do not establish isolation between
processes sharing UID 1001 inside a runtime: credential directory ownership
allows the intended agent to read it; actual containment must come from
filesystem and mount isolation. See the [credential vault contract](../specs/credentials-vault.md).
