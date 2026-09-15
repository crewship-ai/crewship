# Incoming webhooks — issue #2557

User decision: Integrations owns incoming webhook configuration and outgoing
notifications. Activity remains the client-facing execution overview; Journal
is technical diagnostics. Do not add another primary navigation destination.
Do not replace CodeRabbit or publish repository reviews as part of this change.

## Implemented

- Incoming webhooks in Integrations and its Add integration dialog; outgoing
  webhook is named explicitly. Reuses routine, agent and Page management APIs.
- Routine and issue-via-routine selectors share the existing routine webhook
  editor. Selecting a routine does **not** insert an issue step. Existing
  `crewship` steps support `issue.create`, `issue.update`, `issue.comment`.
- Agent target configures an agent background trigger. It does **not** append
  a message to an existing chat conversation.
- Page configuration only offers webhook/script-produced panels. Token
  issuance remains subject to the existing Page permission checks.
- Explicit immutable `ingress_profile` on routine webhook creation:
  `crewship` (default, preserves existing senders) or `github`.
  GitHub SHA-256 validation uses the existing profiles verifier. A captured
  GitHub signature cannot be converted to a legacy Crewship signature to
  escape deduplication; both paths consult the stored profile.
- GitHub PR endpoint accepts opened, synchronize and reopened actions with
  a pull_request object. Verified ping and unsupported actions do not execute.
  The signed body determines the action; unsigned event headers do not route
  it to a different target. Routine author remains responsible for its mapping.
- GitHub receipts have an atomic unique content index: changing the unsigned
  delivery ID cannot spawn another run of identical bytes. Same delivery ID
  with a different body conflicts. Existing receipt retention remains 30 days.
- Receiving URLs/tokens remain show-once; no fake URL is constructed from a
  later list response where the original token is absent.

## Real dev2 verification on 6385ca70 (before new UI/GitHub endpoint deployment)

Artifacts: `/tmp/crewship-webhook-validation/` (contains local test credentials;
never commit its secret files or copy them into public issue/PR text).

- Public HTTPS → legacy signed routine → real `crewship issue.create` →
  completed run `run_cmu2kfoqq00049c4543a6` → issue
  `cmu2kfovf002f4862c180`. Routine `webhook-verification-issue-2557`.
- Page `webhook-verification-2557`: POST accepted seq 1, payload read back from
  the real Page API. An initial invalid schema was rejected with 400; that is
  not counted as the successful write.
- Disposable agent `webhook-verification-2557`: public signed POST and duplicate
  returned work `cmu2khc9l0002bdaccc31`; one attempt/run
  `cmu2khcao0003371a5b69`, terminal succeeded. Real orchestrator/agent execution.
- Actual GitHub-format request to the old routine endpoint returned 401.
  This reproduction motivated the configured GitHub profile. Do not present
  it as a successful GitHub integration on the deployed version.

## Validation discipline

- First full Go run used the default 10-minute per-package timeout; API timed
  out. It is not green. Full final validation needs an explicit longer timeout.
- First build failed on an external node_modules symlink in Turbopack. Installed
  dependencies in the isolated worktree. Next build found absent generated
  Prisma types; generated types (no database migration). Subsequent build passed.
- Initial selected API route tests ran against pre-generation OpenAPI and
  failed the new-route contract check. Regenerated spec and reran the gate.
- Frontend interaction tests cover choosing each target, clearing selection,
  only offering compatible Page panels and honest issue/chat wording.
- GitHub regression tests cover invalid signatures, legacy-profile downgrade,
  ping without execution, unsigned header replay, conflicting bodies and
  concurrent distinct delivery IDs reserving one run. Runtime is substituted
  in these tests; live routine/issue verification above is separate evidence.

## Still distinct work

This does not implement review publication, repository/branch allowlists,
revision-fenced review results, automatic updating of a PR's existing issue,
shared admission for all producers, or the proposed Work-to-Activity UI merge.
A GitHub webhook triggers the configured routine; configure a dedicated review
routine before connecting a repository. No GitHub webhook was created externally.

## Deployed verification on 1d30068e

Dev2 was reloaded successfully and `/api/v1/system/version` reported commit
`1d30068e`, schema `20260915111000`. No main merge or other instance deployment.

- A real public HTTPS request with GitHub-format headers and HMAC started the
  configured routine and completed `run_cmu2kvvk10001b2a1c4bd`. Its issue step
  executed. Receipt `cmu2kvvk1000201b594a2` survived identical delivery and
  changed unsigned delivery-ID retries. Invalid signature and legacy-profile
  downgrade returned 401; same delivery ID with changed bytes returned 409;
  ping returned 200 without execution. This was a locally constructed request,
  not a delivery emitted by an installed GitHub repository webhook.
- A browser opened Incoming webhooks, selected the real routine through the
  Issues target, and displayed the Page token configuration. Browser creation
  with the GitHub sender returned 201 with `ingress_profile: github`; that
  disposable browser-created endpoint was deleted (204). Mobile 390px had no
  horizontal overflow. Existing saved cookie refresh returned 401: this browser
  exercise used a valid dev2 CLI bearer token for all business APIs and supplied
  only the frontend `/api/auth/session` presentation from the validated identity.
  It does not prove the normal login flow.
- The production ntfy notification adapter POSTed the expected test message to
  a temporary local HTTP receiver. A separate attempt against a random public
  ntfy.sh topic returned an acknowledgement but no message was read back;
  public ntfy.sh end-to-end delivery is **not** claimed as verified.
- Final frontend tests on 1d30068e: 13 files / 137 tests passed. Full production
  frontend build and full Go vet exited 0. ESLint had 0 errors and 30 warnings.
  GitHub-only API race tests exited 0 (53.543s).
- The full Go run on 1d30068e found one API failure: the additional exhaustive
  public OpenAPI allow-list omitted the GitHub route. The API package finished
  in 1101.920s; other packages were still running when this entry was written.
  The allow-list was corrected with its HMAC reason; a full API rerun started.
- Manually dispatched CI 34963589302 on 1d30068e found missing request/response
  schema and endpoint documentation for the alias. Concrete OpenAPI schemas,
  headers, success statuses and an API guide section were added. Local strict
  docs-inventory then exited 0. These failures are not counted as green runs.

Evidence (whitelisted logs and screenshots, without signing secrets):
`/srv/crewship/backups/crewship_2/incoming-webhooks-2026-09-15/`.
PR #2558 is stacked on #2554; ordinary PR CI does not trigger against that base.
The manual CI run and local full API rerun must be checked for their actual final
results before merge. A bot walkthrough is not an approving review.

## Follow-up to the final validation

- Public ntfy.sh delivery is now verified independently of its history endpoint:
  an open JSON subscription on a fresh private-to-this-test random topic received
  the exact message sent through dev2's production notification adapter. Evidence:
  `ntfy-stream-result.json`. The earlier empty history poll remains inconclusive.
- The full Go run ended with exit 1: 142 package passes, the API allow-list
  failure above, a stale schema-count sentence in the OpenAPI guide, and a
  retention migration fixture that dropped `profile` while a later index still
  referenced it. Database additionally reached its 30-minute timeout. The fixture
  now removes that later index while reconstructing its pre-retention schema;
  its migration assertions are unchanged. The guide counts now match the actual
  generated schemas. The whole docs-inventory test package passed after this fix.
- A whole database-package rerun uses isolated temporary files on `/dev/shm`
  with Go executables still built under `/tmp`. It exercises file-backed SQLite
  on tmpfs, not disk durability. The whole API rerun continues on normal `/tmp`.
  Their completion results belong in the final evidence report/PR, not inferred
  from package progress. CI's Go Lint job on 99469cd2 passed including the strict
  documentation gate; other jobs were still running when this entry was written.

## UI opponent follow-up, 15 September

The UI review rejected the former standalone Incoming branch. Incoming now
uses the same `IntegrationsExplorer`, sidebar collapse/mobile backdrop and
`AnimatePresence` content as Outgoing. Its default overview has endpoint KPIs
and a cross-target table; target details and the create/reveal surface share
one vocabulary and existing mutation hooks rather than embedding the routine,
chat and Page settings editors.

| Findings | Implementation |
| --- | --- |
| F1 / F8 / F9 | Shared explorer, search, sections/counts, collapse, mobile overlay and transition. |
| F2 / F10 | KPI/table/empty/skeleton/error surfaces. 24h is unavailable, not zero. Page endpoints remain lazy per Page and are explicitly excluded from totals. Failed catalog loads do not claim complete counts. |
| F3 | One endpoint detail with back navigation, real supported actions and recent receipts. Agent receipts label admission rather than imply execution success. Page receipt history has no API and is stated unavailable. |
| F4 | Header counts/badge, Refresh for catalogs/endpoints/receipts, Add integration opens actual creation. |
| F5 | Sender uses FieldLabel + shared Select in the routine editor and the Incoming create surface. |
| F6 | Secret-bearing routine/Page URLs are revealed once, then masked. Agent URLs have no secret and remain copyable, with an explanation. Signing-key rotation does not recover a routine URL. |
| F7 / F11 | Removed duplicate Issues section; routine hint explains issue steps. Incoming and Tools Triggers link to one another and explain direct HTTP vs managed subscriptions. |
| F12 | URL records section and target, restores on reload. Workspace changes remount the layout to discard old catalog state. |
| F13 | Component tests render actual editors against API boundaries. PR browser contract includes real HTTP routine/GitHub create, reveal, table, detail, disable and Refresh plus phone drawer/search/overflow. |
| F14 | Updated layout architecture comment and `docs/api-reference/webhooks.mdx` user instructions. |

Validation on the deployed application revision **b8f9c2fb**:

- Live dev2 browser: real HTTP routine/GitHub create → reveal → table → detail
  → reload → disable → Refresh → cleanup; actual routine/agent/Page details;
  Page endpoint creation with an absolute URL; iPhone drawer/search/backdrop
  and no page overflow. The browser used CLI bearer auth with only session
  presentation supplied; the business APIs were not mocked.
- CI 34975673810: the real-login PR browser contracts passed (2 tests), as did
  the additional reveal (8) and account-group UI checks (5). The full frontend
  suite passed **760 files / 9,111 tests**. Other Go variants were still running
  when this entry was written; current check state belongs to the PR.
- Full current API passed **195.852s**, `-count=1`, with test databases on
  normal disk (`GOTMPDIR=/tmp` takes precedence over `TMPDIR` for Go 1.27
  `t.TempDir`). Full `go vet`, production export,
  ESLint (0 errors / 30 existing warnings), strict docs inventory and the test
  type baseline gate passed. The latter still records 183 existing diagnostics.
- The full local Go run started before the catalog change and exposed a flaky
  `TestSnapshotBeforeMigrate_WhileServerWrites`: its 50ms sleep did not prove
  the first writer commit had occurred. The log records an empty snapshot
  assertion; it is not evidence of a lost committed row. The exec wrapper
  ended with 143 without an overall exit marker, and the package output is
  explicitly FAIL, so this run is not reported green. Both hot-snapshot test
  writers now signal their first completed write instead of relying on time.
  Integrity, generated-column, mode and concurrent-writer assertions remain.
  A complete database rerun is recorded separately in the evidence report.

Final evidence and subsequent check outcomes are recorded in PR #2558 and
`/srv/crewship/backups/crewship_2/incoming-webhooks-ui-2026-09-15/REPORT.md`.
The snapshot synchronization follow-up changes test setup only; the deployed
application's runtime/frontend code is identical to b8f9c2fb.

Live verification found that `GET /agents` omitted `webhook_secret_set` even
though `GET /agents/{id}` supplied it. The catalog now computes the existing
boolean without returning the secret, and Incoming follows catalog pagination.
A SQLite regression failed before the fix for configured/unconfigured agents;
the UI refuses unknown configuration instead of offering an accidental key
rotation. This is the only backend change in the UI follow-up; no new route
or migration was added. Browser verification also caught same-page Next links
leaving the old tab mounted; the cross-links now navigate to the named tab.
Existing endpoint limitations remain: no agent disable/delete API, Page revoke
rather than a reversible switch, per-Page discovery and no Page receipt history.

### Final verification of the snapshot follow-up

On **31852e92**, CI 34977457689 passed the complete shuffled Go command
`go test ./... -count=1 -shuffle=on -timeout 18m`: all 155 packages, including
145 with test results and 10 without tests, with no exclusions. Frontend and
the real-login Playwright job also passed again. The remaining race/platform
checks were still running at this entry; the PR links their live status.

Locally, the snapshot family passed three repetitions on normal disk
(197.053s). The complete database package then passed with both synchronization
fixes, using a separately built test binary and `GOTMPDIR` pointing to a
private tmpfs directory; the binary returned **DATABASE_EXIT=0**. The test
files' actual open paths were checked under `/dev/shm`. This is database
functional verification, not a power-failure durability test. An intermediate
rerun was cancelled when it was found to still use disk and to predate the
companion fixture change; it is not counted as a pass.

The final documentation-only follow-up corrects the earlier API environment
description: setting `TMPDIR` did not override Go 1.27's `GOTMPDIR=/tmp` for
`t.TempDir`. Production code and tests remain byte-identical to 31852e92.

## Simplified creation follow-up

The default routine creation form now asks only for target and endpoint name.
`Sender` moved into a collapsed `Advanced settings` section as `Webhook format`.
The existing signed Crewship JSON contract remains the default; GitHub PR support
remains explicitly selectable. No new authentication scheme, unsigned fallback,
provider detection or payload translator was added. This is not a claim that all
third-party webhook protocols are compatible. Signature, size, rate and target
permission enforcement remain server-side and unchanged.

Validation: 140 Integrations tests passed, including default creation without
opening advanced settings and explicit GitHub creation. Frontend build and lint
passed (30 pre-existing lint warnings). Webhook and signature-profile Go packages
passed. A real dev2 browser flow passed default-field visibility, GitHub endpoint
creation through advanced settings, secret reveal, refresh, disable and cleanup;
only session presentation uses the CLI identity, not a normal browser login.
Screenshot: backups/crewship_2/incoming-webhooks-ui-2026-09-15/09-simple-create.png.

The earlier whole-tree Go run in /tmp/incoming-avatar-go.log ended GO_EXIT=1:
internal/api and internal/database hit the default 10-minute timeout. A repeat of
those entire packages with a 20-minute timeout and private tmpfs GOTMPDIR is
running in /tmp/incoming-simple-go-recheck.log. Do not call the whole suite green
until that exit result is observed. No Go source changed in this UI follow-up.

## Closing review follow-up (2026-09-15, after 9a45d1bb)

PR #2554 merged as f94a1ec3. #2558 is the remaining PR; main through
0122583b was merged cleanly before this follow-up. Its previous CI
34993639592 failed Go Shuffle in TestAcceptanceRoutineTypedDecisionHTTP:
the approved run remained waiting. That run is not a green verification.

Reviewed fixes: workspace switches clear entity-specific URL selection while
initial deep links survive; target selection uses stable IDs (legacy slugs are
accepted and canonicalized); agents without crews cannot create endpoint rows.
The CLI help describes generated HMAC secrets, and CLI URL construction retains
the GitHub profile suffix. GitHub endpoints reject the legacy route even with a
valid signature and ignore non-PR event headers before receipt acceptance.
Snapshot test writers wait for a successful commit, retry transient errors, and
cancel/join on a bounded timeout instead of failing on the first lock collision.

The shuffled acceptance failure exposed a lost wakeup: approval could read a
running row before MarkWaiting, or attempt to re-acquire a still-live run ID.
Approval/signal resume now waits on the shared registry's execution lifetime
before inspecting the persisted state. The HTTP acceptance fixture now wires
the shared registry as cmd_start does. A real SQLite regression forces approval
between the pending-status read and parking. With the old resume implementation
it fails; the fixed implementation completes the downstream transform. A second
case exercises a parked row whose lifetime fence is still held; only the first
case is mutation-discriminating.

At this entry, all 143 Integrations tests, targeted GitHub API tests and the race
pipeline resume/registry family passed. The full Go/vet and frontend/lint/build
runs are in progress; final exit results must be recorded separately. These are
local code tests, not new real-provider or dev2 delivery evidence.
