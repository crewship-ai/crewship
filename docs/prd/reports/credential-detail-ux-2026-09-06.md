# Credential and provider detail UX — 2026-09-06

Follow-up to the user's detail/Edit screenshots for release 1.0. This is a
frontend increment, not completion of the outstanding provider-runtime PRD.

## Design and behaviour

- A single reading column replaces the narrow main/right-card split. Identity,
  primary actions, four summary facts, connection health and assignments remain
  prominent. Properties/protection, activity history and provider delivery details
  use keyboard-accessible disclosures. Deletion is in advanced details for secrets;
  provider accounts retain their existing Revoke confirmation.
- Providers show Account and Connection health, not the generic Value/reveal/
  random-secret-rotation controls. Edit is metadata-only: name, description, tags
  and existing access controls. It cannot change provider identity, raw auth value
  or provider-reported expiry. Re-login remains the connection replacement flow.
- Unknown quota no longer occupies an entire primary card. Its absence is stated
  in Connection details. Actual reported limits/errors remain prominent.
- Removed claims that a second account automatically forms a failover pool or
  that a 429 automatically parks an account. Those PRD phases remain unfinished.
- Edit uses the existing styled checkbox and descriptive icons. Tags have an
  explicit accessible name, count, instructions, suggestions, removal and
  deduplication. A pending tag is included even when saving without blur and
  participates in discard protection.

## Correctness fixes

- The open detail now resolves its current row from refreshed lists. Previously
  Edit refreshed the lists but retained the old selected object, so a successful
  save left the detail showing the old name/tags until it was reopened. Browser
  acceptance now verifies updated metadata in the open detail after save.
- Metadata-only secret edits no longer truncate an existing expiry timestamp to
  midnight. PATCH sends expiry only when the date actually changes.
- Provider summary expiry follows `login.expires_at`, including the response to
  Refresh now, instead of potentially stale/null generic token metadata.
- Provider identity cannot be reassigned accidentally using the decorative
  brand picker in Edit. Provider edits omit `provider`, `value` and expiry.
- Existing Keeper tier preservation and role/capability gates remain intact.
  No admin-only RBAC policy or backend authorisation change is introduced.

## Verification

Commands and final outcomes are recorded below after completion. Browser checks
intercept all API traffic with fixtures; no real tokens are read, changed or sent
to providers. Desktop/mobile acceptance covers disclosure behaviour, absence of
provider rotation, provider metadata PATCH, tag entry/removal/deduplication and
secret edits preserving L3. Earlier browser attempts hit a stopped Next dev
server, then an old embedded export; final local acceptance serves the freshly
built `out/` export on loopback instead. A tag locator also exposed whitespace
in the accessible label; the input now has the stable accessible name `Tags`.

- `pnpm exec vitest run 'app/(dashboard)/credentials' components/features/credentials lib/credentials lib/credential-providers`: 577 passed, 23 files.
- `pnpm lint`: no errors; 33 pre-existing warnings.
- `pnpm build`: passed (static export and TypeScript).
- `go vet ./...`: passed.
- `pnpm exec playwright test --config=/tmp/crewship-provider-first.config.mjs`:
  6 passed at 1280px and 390px (17.3s), against the fresh local export. Includes
  explicitly enabling and cancelling secret replacement before metadata save.
- Logs: `/tmp/credential-detail-{ui,lint,build,vet,go,e2e}.log`.
- The first full Go run terminated with exit 143 without a test failure or
  completion summary. Its cause was not established; it is not a pass. A fresh
  full run uses an isolated `mktemp -d /dev/shm/crewship-credential-detail.XXXXXX`
  directory as TMPDIR (temporary test DBs only), logged separately at
  `/tmp/credential-detail-go-retry.log`.
- The fresh `TMPDIR=... go test ./... -count=1 -timeout 30m` run completed
  successfully with exit 0, including the full API and database suites.
