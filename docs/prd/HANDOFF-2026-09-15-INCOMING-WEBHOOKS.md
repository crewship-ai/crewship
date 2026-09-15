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
