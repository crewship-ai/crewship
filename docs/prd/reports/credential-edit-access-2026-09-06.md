# Typed credential Edit, truthful checks and access — 2026-09-06

Follow-up to the approved release-1.0 UX proposal. This increment changes the
frontend, not authentication protocols, stored encryption or RBAC policy.

## Implemented

- Edit derives its replacement label and input shape from stored type/field
  metadata: USERPASS password plus editable username; multiline SSH/PEM and
  certificate inputs; file/secret contents; API key and endpoint URL. Existing
  access-key-ID fields identify a key pair. Unknown generic secrets are not
  guessed from secret contents. Provider editing remains metadata-only.
- Existing additional fields are editable via their own PUT operations. Secret
  values never prefill inputs, secret flags are preserved, errors keep drafts,
  and field saves are explicitly separate from metadata Save. Metadata submission
  is blocked while field drafts remain. Provider auth fields are not exposed here.
- Typed hints explain what Crewship does NOT do (issue certificates, install SSH
  public keys or change an external service). Scope edits show an impact notice.
  Successful metadata saves show confirmation and refresh the open detail.
- Tool availability is labelled as such, not simply Ready. A separate connection
  verification sentence uses a supported probe and its actual check timestamp/result;
  token expiry, tool installation and a stored subscription do not imply a working
  login. Historical successful checks explicitly do not guarantee the next run.
- Access shows management policy, the viewer's capabilities, credential scope,
  technical bindings and server-reported direct/crew/binding grant provenance.
  Workspace and agent delivery bindings now contribute candidate agents. Binding
  precedence is read from GET /agents/{id}/credentials, not reimplemented in UI.
  Missing grant-source data is marked unconfirmed, not invented as explicit.
- Failed access loads no longer render as "nobody has access". The bounded
  provenance lookup reports checked/total visible candidates and failures. It
  remains capped at 12 agents and is NOT a complete effective-access audit; hidden
  agents, dynamic Keeper decisions and runtime policy cannot be inferred here.
- Rotation is described as replacement with a Crewship grace period, not minting
  an externally valid credential. Removed its auto-probe: TestStored in
  internal/api/credentials_test_endpoint.go reads the old encrypted value and
  does not validate a replacement sent in the body. The old UI could therefore
  falsely label a new draft Valid. Replacements are now explicitly unverified.

## Boundaries

This does not migrate legacy name-based delivery. Explicit bindings already
separate the display name from the technical slot, and are shown clearly; users
of legacy delivery still get the rename warning. Additional field creation/deletion
and atomic multi-part replacement are not added; each existing field is saved
independently through its existing API. No admin-only provider visibility rule
or backend provider pool/failover work is included.

## Verification

All browser API calls are intercepted fixtures. No stored real credential was
read, changed or sent upstream. Desktop/mobile scenarios cover typed certificate
replacement, CA-field PUT and refresh, empty private-key replacement input,
cancellation, tags, metadata-only PATCH, immediate detail refresh, separate
provider editing and the existing onboarding flows.

Logs: `/tmp/credential-ux-{ui,go,vet,lint,build,e2e}.log`.
The full Go run uses an isolated mktemp directory under /dev/shm as TMPDIR.

- Credentials Vitest suites: 587 tests / 25 files passed.
- Full `go test ./... -count=1 -timeout 30m`: passed, including API (121.574s)
  and database (317.866s). `go vet ./...`: passed.
- `pnpm lint`: 0 errors, 33 existing warnings. `pnpm build`: passed.
- Local exported-UI browser acceptance: 6 passed (34.8s), at 1280px and 390px,
  served on loopback port 43183 with intercepted APIs. Final deployment performs
  another build and the same acceptance against the public dev3 URL.

## Deployment

Feature commit `dd99756c` deployed only to dev3 via
`sudo systemctl reload crewship-ws@3`, completing 20:04:55 UTC. The deployment
rebuilt the frontend and server successfully. Health returned `{"status":"ok"}`;
public `/credentials` returned HTTP 200.

The public fixture-based Playwright run passed all 6 scenarios (13.9s):
`CREDENTIALS_TEST_URL=https://crewship-dev3.unifylab.cz pnpm exec playwright test --config=/tmp/crewship-provider-first.config.mjs`.
Log: `/tmp/credential-ux-public-e2e.log`. No real provider requests, secret changes,
push or PR merge were performed.
