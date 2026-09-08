# Demo credential shapes and reveal lifecycle (#2461)

This increment retains the server authorization gates. The operator subsequently
approved changing the default capability grant for OWNER/ADMIN (see below);
workspace policy, classification, human-session and audit gates are unchanged.

## Approved administrator default (2026-09-08)

- OWNER/ADMIN memberships with NULL capabilities receive `credentials:reveal`.
  The admin preset also includes it. All existing explicit sets remain
  authoritative, including empty/malformed sets (fail closed) and revocations.
- The immutable v109 backfill is unchanged. Historical stored sets are not
  blindly rewritten: an old explicit set is indistinguishable from a deliberate
  restriction. An authorized operator can grant reveal to another ADMIN.
- This is a change from the original PRD's opt-in-only L2 grant and is recorded
  in that PRD and the changelog, not described as the old behavior.
- On dev3 the workspace switch was enabled through CLI with operator approval.
  A self-grant attempt returned 403; that guard was not bypassed. Read-only
  inspection confirmed the demo OWNER has NULL capabilities and will therefore
  use the default after deployment. OWNER capability edits remain prohibited.
- Tests cover role defaults, explicit denial/malformed data, real handler
  disclosure plus audit, and workspace API exposure to the frontend.

## Changes

- Demo JSON file with a non-secret filename and GCP brand; ID/secret pair with
  AWS brand. Existing login/certificate fixtures gain Mailgun/Cloudflare brands.
  Values are deliberately inert. Keeper tiers are protection, not an item type.
- A blocked reveal points to Settings → Access & Secrets (except SEALED).
- Secret-bearing dialog sessions unmount on close and credential/workspace
  switch. Pending requests are aborted and stale responses ignored.
- Revealed values are hidden after 30 seconds or tab hiding. Revealing again
  requires a new reason and another server request/audit event. This is display
  lifetime control, not cryptographic erasure or automatic clipboard clearing.
- Malformed successful responses are refused rather than displayed as an empty
  secret. Server step-up/session freshness and restricted four-eyes behavior
  are unchanged; this increment does not implement those deferred features.
- Dedicated isolated browser checks are wired into CI.

## Live dev3 data

Only two new inert credentials were added through CLI, without reseeding,
deleting anything or assigning them to agents:

- demo-v2-json-file: `cmtsnnt8403d3282da6f7`, L2, GCP, filename field.
- demo-v2-key-pair: `cmtsnnt9a03d51dd0c18a`, L3, AWS, access_key_id/region fields.

These live rows have names/types/brands/fields matching the new fixtures, but
the CLI create command does not set fixture description/tags. Existing demo
rows were not overwritten. Live reveal policy was initially disabled and then
enabled after operator confirmation. No real stored password was revealed.

## Validation

- 420 frontend component tests passed, including existing settings/role gates
  and new timeout, tab hiding, stale-context and malformed-response guards.
- Targeted seed and API reveal tests passed, including owner/admin permission,
  member/viewer denial, default-off, SEALED, cross-tenant and noninteractive auth.
- Production build passed after an initial ENOSPC failure. Only generated
  caches from this session's worktrees were removed; no source or live data.
- ESLint: zero errors, 32 repository warnings. Explicit lint of browser/config
  files reports them ignored by the existing ESLint configuration, not checked.
- Six browser scenarios passed using API fixtures and dummy values, not live
  authorization. They cover an allowed owner and member/viewer, missing
  capability, disabled-policy and SEALED refusals.
- Removing the dialog context key made the stale-response regression test fail;
  restoring it returned all 16 reveal-dialog tests to green.
- Full `go test -p=2 ./... -count=1 -timeout 40m` failed in `internal/api`:
  one Docker restart and three backup tests encountered `no space left on device`.
  Other packages passed; `go vet -p=2 ./...` passed. Full log:
  `/tmp/demo-reveal-full-go.log`. This is not a green full-suite result.
- An API retry initially used the invalid flag `-timeout10m` and did not execute
  tests. The corrected invocation uses `-timeout 10m` and is recorded separately
  in `/tmp/demo-reveal-api-retry-fixed.log`. The corrected full API package run
  passed after cache space was reclaimed. Together with the original run this
  covers every package, but is not a single all-green full-suite invocation.
- After the approved default change, a fresh full
  `go test -p=2 ./... -count=1 -timeout 40m` and `go vet -p=2 ./...` both
  passed (`/tmp/reveal-default-full.log`, `/tmp/reveal-default-vet.log`).
  The database package took 634 seconds. A final targeted API rerun includes
  the additional default-grant → API revoke → 403 regression test.
- Final UI verification: 420 component tests, an additional final settings-only
  run (18 tests), seven browser scenarios, production build and lint passed
  (0 lint errors, 32 existing warnings). Agents/migration invariants passed.
- Deployment and live browser acceptance will be recorded in the PR after
  rollout. This report does not claim complete PRD acceptance.

## Deployment acceptance and scanner follow-up

- Deployed application commit `c56b541c` to dev3 via
  `sudo systemctl reload crewship-ws@3`. Health returned `ok`; Go and Next.js
  are running. The root's unrelated operator WIP was preserved.
- A real browser login as the demo OWNER revealed only the inert JSON fixture
  and observed automatic hiding after 30 seconds. CLI audit confirms REVEAL by
  Demo User at `2026-09-08T13:23:21Z`. No real passwords were inspected.
- PR #2462 CodeQL reported a stat-before-read race in the test-only static
  server. Replaced that sequence with direct reads and checked every fallback
  path stays inside the export root. All eight final browser/helper tests pass,
  including traversal denial, root serving and a missing-file response.
- The follow-up changes only test infrastructure and this report, not deployed
  application behavior. CodeQL re-analysis and PR review are still required;
  the PR has not been merged.
