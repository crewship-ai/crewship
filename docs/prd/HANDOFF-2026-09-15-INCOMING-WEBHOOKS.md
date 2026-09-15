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
