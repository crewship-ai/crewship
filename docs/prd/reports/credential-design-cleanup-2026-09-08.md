# Credential design cleanup — 2026-09-08

User-approved follow-up to the credential design proposal. Builds on the local
form clarity work; retains Crewship's shared detail/create surfaces, semantic
colour tokens, provider icons and agent avatars. No database migration or
backend policy change.

## Customer-facing changes

- Detail uses a bounded reading width, a stronger identity header and a primary
  Edit credential / Edit provider button. The summary shows scope and creation
  rather than repeating tool readiness and an incomplete assignment count.
- Used by leads with compact avatars and names. Grant provenance, delivery
  variables and coverage diagnostics remain under Delivery bindings & access
  details. Loading, incomplete results and expired leases remain visible.
  These are configured assignments, not proof of successful runtime use.
- Keeper tier and reveal classification remain in Access & security. Both have
  enforced backend behaviour; neither is obsolete. Tool availability only
  appears there for reported, crew-specific gaps. It is not connection health.
- Tier filtering moves into the shared filter popover and participates in its
  count/reset. Per-secret tier badges and existing provider icons are retained.
- The customer replacement flow accepts a value obtained from the issuing
  service and sends grace_seconds=0 through the existing rotate capability gate.
  It neither generates nor revokes provider credentials. Advanced grace controls,
  duplicate rotation cards and the rotations-history fetch are removed from the
  detail. Backend overlap support is retained. Existing audit events remain.
- Replacement shares the create/edit shell, discard protection, fixed footer,
  hidden-value defaults and type-aware multiline input. Whitespace in supplied
  values is preserved. Managers can still replace via Edit; a member holding
  credential.rotate retains their independent replacement action.
- Reveal no longer promises provider issuance or grace overlap, and only offers
  replacement when the viewer has the corresponding permission.
- Add secret reads Type → Details → Access. Identity precedes secret entry;
  Login asks for username before password. Optional labels/tags and extra fields
  are collapsed. Keeper/expiry options are under Access & security. Scope copy
  no longer claims that workspace visibility assigns the secret to every agent.
- Generic VAULT_* shape icons are still selectable but are not treated as detected
  issuing providers or sources of automatically suggested delivery variables.
- Add provider keeps the supported provider-specific sign-in modes and uses
  roomier branded tiles. Provider Edit has no editable Keeper tier and omits
  security_level from metadata updates, preserving the existing server policy.

## Verification

- Credential/page/provider unit suites: 608 passed across 26 files.
- Full ESLint: zero errors, 32 existing warnings. Targeted ESLint also passed
  after the final wizard/sidebar/detail refinements.
- Final production static export and TypeScript: passed. Export staged using
  scripts/embed-web-out.sh; the tracked .placeholder.html is preserved.
- go vet -p 2 ./...: passed.
- Public Playwright acceptance: 11 passed at 1280px and 390px against
  https://crewship-dev3.unifylab.cz. Covers provider RBAC/sign-in, metadata-only
  editing, typed replacement opt-in/cancel, additional fields, tags, visible
  agent names and Login creation with collapsed advanced protection. API calls
  use intercepted fixtures; no actual credentials or provider accounts changed.
- go test -p 2 ./... -count=1 -timeout 40m: passed with exit 0, including
  the full API and database suites. Migration tests used isolated /tmp databases.

## Deployment

Reloaded only crewship-ws@3 after the successful frontend checks. Service is
active, localhost:8083/api/health reports status ok, and the public credentials
page returns HTTP 200. Public browser acceptance passed after the reload.
No commit, push or PR was created; existing local work is preserved.

Logs: /tmp/credential-redesign-tests5.log, /tmp/credential-redesign-lint.log,
/tmp/credential-redesign-build-final.log, /tmp/credential-redesign-vet.log,
/tmp/credential-redesign-go.log, /tmp/credential-redesign-public.log.
Screenshots: /tmp/credential-detail-{1280,390}.png,
/tmp/credential-add-login-{1280,390}.png, /tmp/credentials-edit-{1280,390}.png.

## Follow-up: coloured tags and Test now explanation

Credential tags now use stable name-derived tints from the existing ACCENT
palette, consistently in detail, Edit and Add secret. Colours are decorative;
they do not encode validity, permission or classification. No new colour tokens
or schema fields were introduced.

Verified from source: TestStored decrypts the saved value on the backend and,
for GitHub, calls GET https://api.github.com/user with Bearer authentication and
a 10-second context timeout. It records a TEST audit event. This verifies the
provider probe, not repository-specific access or an agent's installed CLI.
The test does not replace the stored value or rotate anything at GitHub.

Tag follow-up verification: 407 credential component tests passed; targeted
ESLint and production build/TypeScript passed. Reloaded dev3, checked backend
health and inspected /tmp/credential-colour-tags.png from a public browser run
with fixture API responses. No backend source changed in this follow-up;
the preceding full Go test/vet results remain the backend verification.
