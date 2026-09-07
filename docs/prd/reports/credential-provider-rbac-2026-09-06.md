# Credential/provider RBAC follow-up — 2026-09-06

User-approved follow-up to the RBAC review. This changes authorization policy,
not provider authentication protocols, Keeper levels or encryption.

## Policy and enforcement

- OWNER/ADMIN can view and manage provider accounts. Other roles cannot,
  even with ordinary credential create/rotate/reveal capabilities. Includes
  PROVIDER_LOGIN and the existing model-provider API_KEY/AI_CLI_TOKEN rows.
- Ordinary secrets keep the existing policy: MANAGER lifecycle editing,
  MEMBER/VIEWER workspace or own-crew metadata visibility, and separate layered
  reveal/rotation permissions. This does not grant additional secret access.
- `credentialVisibilityFilter` removes provider accounts from lower-role list,
  detail and related queries. `providerAccountPolicy`, installed on public
  credential-ID reads and workspace mutations (including rotation-ID paths),
  adds an independent server boundary. Create/Update also reject provider
  creation and ordinary-secret-to-provider conversion by lower roles.
- Device sign-in requires OWNER/ADMIN. Status checks current membership in the
  original workspace, so demotion/removal invalidates access to pending codes.
- Agent credential listings and resolved bindings redact metadata using the
  same visibility set, including deleted/foreign-workspace records. Resolution
  happens before redaction: hidden winners do not resurrect visible fallbacks.
  Collision warnings cannot name an invisible credential occupying the slot.
- Agent `pays_with` returns only `{provider, restricted: true}` to lower roles;
  no account ID/name, owner, plan or refresh details. Admin shape is preserved.
- Paymaster's account-level subscriptions endpoint is admin-only too. The UI
  hides this panel and does not request it for lower roles. Other spend views
  retain their existing rules.
- Credentials hides provider tabs, filters and Add/re-login actions for lower
  roles; the agent config renders a read-only provider brand, not an account
  picker. Workspace/permission changes close open editing/onboarding state.

Runtime credential delivery is unchanged. These public management permissions
do not revoke existing agent assignments or alter which credentials a run gets.

## Verification scope

Regression coverage: all five roles, legacy provider rows, direct HTTP-method
policy guard, create/conversion denial, explicit capability bypass denial with
an ordinary-secret success control, agent metadata and binding precedence,
cross-workspace/deleted records, crew membership, collision notices, device
sign-in demotion and minimal agent provider identity.

Frontend tests cover the identity-only row and disabled subscription loading.
Browser tests cover administrator onboarding/editing at desktop/mobile sizes
and absence of provider administration for MANAGER/MEMBER/VIEWER. Browser APIs
are intercepted fixtures; no real provider tokens or live paid calls are used.

This is a scoped Credentials/provider authorization change, not a claim that
every endpoint, historical journal entry or RBAC flow in Crewship was audited.
Verification logs are `/tmp/provider-rbac-*.log`.

- Full `go test ./... -count=1 -timeout 30m`: passed (API 189.299s,
  database 541.312s), using an isolated TMPDIR under /dev/shm.
- Final targeted RBAC/delivery regressions: passed (3.303s), including the
  added capability control and structured collision-warning redaction.
- Final `go vet ./...`: passed. `pnpm lint`: 0 errors, 33 existing warnings.
- Credentials/provider Vitest selection: 529 tests passed; subscription hook
  and panel tests: 17 passed. `pnpm build`: passed.
- Exported-UI Playwright acceptance: 9 passed (22.8s), including all three
  lower roles and administrator desktop/mobile flows, with intercepted APIs.
- Additional full `go test ./internal/api -count=1 -timeout 15m` after the
  delivery-warning change and extra regression cases: passed (475.700s).

## Deployment

Feature commit `b369c8dd` deployed only to dev3 via
`sudo systemctl reload crewship-ws@3`, completed 20:42:09 UTC. The running binary
is `/tmp/crewship-3-dev` (not the older `./crewship` file); its version reports
`b369c8ddfa406b2cd5db36b740f8a60dd064f6bb`, built 20:41:59 UTC. It is marked dirty
because unrelated workspace WIP was intentionally preserved.

`GET /api/health` returned `{"status":"ok"}` and public `/credentials` returned
HTTP 200. The public fixture-based browser run passed all 9 scenarios (15.5s):
`CREDENTIALS_TEST_URL=https://crewship-dev3.unifylab.cz pnpm exec playwright test --config=/tmp/crewship-provider-first.config.mjs`.
Log: `/tmp/provider-rbac-public-e2e.log`. No real credentials were mutated by
these tests. No push, PR merge or deployment to another instance was performed.
