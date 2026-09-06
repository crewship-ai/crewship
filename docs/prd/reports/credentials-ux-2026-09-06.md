# Credentials UX — 2026-09-06

Scope: the user's dev3 Credentials screenshots and follow-up request, after
the provider-first onboarding checkpoint. This is an incremental UX change,
not completion of all provider-login PRD phases.

## Changes

- Separate, always-available `Add secret` and `Add provider` header actions
  under the existing create permission. Secret creation has six shapes;
  provider creation starts with the provider catalog and keeps provider-specific
  connection methods.
- Removed the legacy `Connect via OAuth` entry points and dialog mount from
  Credentials. Other integrations' OAuth APIs, provider device sign-in and
  token refresh remain intact.
- Provider Access no longer asks for Keeper tier or manually supplied expiry.
  Creation omits `security_level`, retaining the existing server default L1
  like device-code creation. This does not introduce a stricter policy.
- Overview's secret filter count excludes provider accounts. The provider
  navigation in Overview no longer falsely highlights `All providers`.
- Edit uses the shared Routines `CreateSurface`: mobile bottom sheet,
  independent scrolling body, fixed footer and discard protection. Description
  is visible without opening advanced settings. Replacement must be explicitly
  enabled; disabling it clears the draft value. Metadata edits omit `value`.
- Active editing permits readable display names, as creation already does,
  and explains the implications for legacy name-based environment delivery.
- Detail reduces the prominence of rotation, omits empty Fields cards and
  explains that readiness checks tool availability, not upstream authentication.

## Additional bug found in browser acceptance

`page.tsx::handleEdit` did not forward `security_level`. Editing an L3 secret
therefore initialized the dialog at L1 and PATCH sent L1, even for a name-only
change. Forwarding the stored tier fixes this. For older responses without a
tier, unchanged metadata edits now omit the tier instead of writing a default.
The desktop/mobile browser tests assert that an L3 metadata edit retains L3.

## Decisions still open

The proposed OWNER/ADMIN-only provider account management and metadata
visibility has not been applied. Current CASL allows MANAGER creation/update
and MEMBER/VIEWER metadata reads; server visibility also depends on crew scope.
A real restriction needs consistent API enforcement, including lists, detail,
bindings and account metadata exposed elsewhere, not just a hidden tab.
Requested user confirmation: ordinary users would see only the assigned provider
identity at an agent, without provider-account details.

The provider PRD's pool/failover and remaining native adapters are unchanged.
No real secrets were modified and no real provider request was issued by these
tests. Browser acceptance intercepts API requests with fixtures.

## Verification

Logs for this increment: `/tmp/credentials-ux-{ui,e2e,public-e2e,go,vet,sidecar,orchestrator,lint,build}.log`.

- Vitest, including the Credentials page suites: 23 files / 557 tests passed.
  Command: `pnpm exec vitest run 'app/(dashboard)/credentials' components/features/credentials lib/credentials lib/credential-providers`.
- Playwright: 4 tests passed locally and 4 passed through public dev3 (13.7s),
  at 1280px and 390px. These cover separate creation flows, missing legacy
  OAuth entry, provider-specific fields, simplified Access and metadata-only
  edits preserving L3. API traffic is intercepted, including PATCH: no live
  credential writes. Temporary isolated config: `/tmp/crewship-provider-first.config.mjs`;
  public invocation sets `CREDENTIALS_TEST_URL=https://crewship-dev3.unifylab.cz`.
- `pnpm lint`: 0 errors, 33 existing warnings. `pnpm build`: passed.
- `go vet ./...`: passed after both concurrent MCP commits.
- Separate final sidecar tests passed (41.954s), orchestrator tests passed
  (32.479s). Full `go test ./... -count=1 -timeout 30m` finished with exit 0;
  its run started before the two concurrent MCP commits, which is why their
  packages were additionally retested afterwards.

Public `/credentials` returned HTTP 200 and dev3 `/api/health` returned
`{"status":"ok"}`. The concurrent session restarted dev3 on `c80fa8d0` with
this UX work still uncommitted; the public browser checks above verify the
new UI is actually served. This session did not initiate another restart or
push/merge a PR. The UX commit follows that verified deployment.

During testing another session committed `f4e92074` (MCP notification responses)
and restarted dev3. A browser run failed on connection refusal during that
restart; it was not a pass. Subsequent browser failures exposed the tier
forwarding bug above and test-selector issues; these were fixed before reruns.
That concurrent change is not attributed to this UX work. Its sidecar package
was separately retested because it landed after the full Go run started.
