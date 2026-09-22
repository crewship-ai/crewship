# Z.AI GLM Coding Plan — P1 implementation record

Date: 2026-09-18. Issue: #2621 (created per the expansion analysis's
one-issue-per-slice rule). Branch `feat/opencode-zai-coding-plan`, based on
`feat/opencode-go-zen` at `555ab2573` — a stacked slice, because the OpenCode
runner plumbing it builds on lives in PR #2619 and is not merged. Companion
documents: [integration PRD](opencode-go-zen-integration-2026-09-16.md),
[acceptance handoff](opencode-go-zen-handoff-2026-09-18.md),
[provider expansion analysis](opencode-provider-expansion-2026-09-18.md).

## What this slice is

The user's purchased **Z.AI GLM Coding Plan** subscription as a first-class
Crewship product, end to end: Add provider → encrypted storage → access
grants → agent/model selection → OpenCode run → streaming → usage/errors.

## Verified product facts

- Native identity `zai-coding-plan`, endpoint
  `https://api.z.ai/api/coding/paas/v4`, SDK `@ai-sdk/openai-compatible`
  (Bearer-only auth). Verified against the deployed host CLI's own runtime
  catalog: opencode 1.18.31, `~/.cache/opencode/models.json`. The catalog
  lists glm-5.3, glm-5.3-flash, glm-5.3-highspeed, glm-5.2, glm-4.7,
  glm-5-turbo. Catalog presence is not account entitlement.
- Upstream reads `ZHIPU_API_KEY` for all four z.ai/zhipu products (inventory
  JSON), so Crewship uses its own slot `ZAI_CODING_PLAN_API_KEY`. Existing
  metered `ZAI` credentials are untouched.

## Implementation

- Canonical identity `ZAI_CODING_PLAN` (alias `ZAI-CODING-PLAN`) in
  `internal/providerlogin`; slot `ZAI_CODING_PLAN_API_KEY`; plan label
  "GLM Coding Plan"; mode `api_key` (a subscription key, like OpenCode Go).
- `internal/llmroute`: spec `ZAI_CODING_PLAN` → route `/llm/zai-coding-plan`,
  upstream `api.z.ai` + `/api/coding/paas/v4`, Bearer auth replacement,
  RequireCredential. One register() — not part of the opencode.ai gateway
  loop, because the host, base path and auth surface differ.
- Orchestrator: `openCodeProviderIDs` + the explicit-payer guard in
  `qualifyOpenCodeModel` (a bare model on a Coding Plan agent can never be
  re-stamped to a vendor provider); native prefix rewrite in
  `resolveRoutedProvider`; options-only `OPENCODE_CONFIG_CONTENT` override
  (preserves the native SDK/model metadata); auth-file slot → `zai-coding-plan`
  with dummy key when routed.
- API: agent/onboarding/model-list provider surfaces, curated
  `zai_coding_plan` models in `config/models.json` (glm-5.3 default),
  partial-suggestions rule preserved (custom native model IDs stay valid).
- Paymaster: no rate row and none invented — `zai-coding-plan` resolves
  SourceNone; subscription limits are not token-derived prices.
- Frontend: provider card, connection guide (console + docs links,
  subscription-not-metered note), brand/icon, wizard presentation,
  provider/model pickers, `ZAI_CODING_PLAN` LLM provider enum everywhere,
  billing facts on the login detail.
- Metered↔subscription separation is fail-closed: a `ZAI` credential never
  routes the Coding Plan spec and readiness reports it missing (pinned in
  tests).

## Automated evidence

Executed on the stacked branch after the changes (commit f67ed83a1; the binary's version string renders it with git-describe's g prefix as nightly-…-gf67ed83a1;
local log for the full suite: `/tmp/opencode/zai-coding-plan` worktree,
`/tmp/opencode/zai-full-go.log` on the build host):

- `go test ./... -count=1 -timeout 40m` — **exit 0, 146 packages `ok`, zero
  `FAIL` lines**, verified with `set -o pipefail` and `${PIPESTATUS[0]}`
  (an unpiped echo of `$?` after a `| head` would report `head`'s status,
  not the suite's — earlier summaries in the PR thread were corrected to
  this form).
- `go vet ./...` — clean. `scripts/agents-invariants` — 4/4 hold.
- `go test ./internal/orchestrator ./internal/sidecar` — ok, including the
  GLM-catalog model set (`glm-5.3`, `glm-5.3-flash`, `glm-5.2`) walked by
  `TestOpenCodeGatewaysRouteWithoutExposingKeys` for `ZAI_CODING_PLAN` plus
  one bare cross-vendor name pinning the explicit-payer guard;
  `TestOpenCodeZAIMeteredKeyDoesNotRouteCodingPlan`;
  `TestZAICodingPlanProxyChatCompletions` (JSON and SSE chat-completions
  bodies passed through verbatim, usage observed on the `zai-coding-plan`
  ledger; the Responses API is deliberately not exercised — the coding
  endpoint is openai-compatible chat completions and nothing claims more);
  `TestZAICodingPlanProxyRejectsOtherProductKey` (503, nothing forwarded).
- `go test ./internal/api -count=1` — ok, including the extended
  provider-login/agent-update tables (encrypted storage, slot
  `ZAI_CODING_PLAN_API_KEY`, plan label, custom model IDs).
- `pnpm test` — 772 files, 9219 tests passed. `pnpm lint` — 0 errors
  (30 pre-existing warnings). `pnpm build` — static export ok.

The authoritative record remains the checks on the pushed commit; CI for the
final head is linked in the PR.

## dev3 deployment findings (2026-09-18)

An attempt to deploy this build through the standard unsigned launcher
**failed and was rolled back within ~2 minutes**; the cause is a live
conflict on instance 3, not a defect in this slice:

- `crewship-ws@3` runs prebuilt binaries from
  `/srv/crewship/dev3-pages-release/start-unsigned.sh` (drop-in
  `zz-unsigned.conf`), against `/srv/crewship/crewship_3/crewship.db`.
- That database is at migration **v20260916140000** — the incoming-webhooks
  work (#2558, branches `dev3/webhook-receiver` / `feat/webhook-unsigned-opt-in`)
  deployed on Sep 17 and migrated the shared DB. `origin/main` (a99a82122)
- This branch's base (fc2ebb841, via #2619) only knows migrations to
  v20260916090151, so the binary refused to start (forward-only guard —
  correct behaviour). Rollback: `.revert-907c57e-20260918` binary copies in
  the release dir, `systemctl reload`, service verified active; the staged
  sidecar under `/tmp/crewship-3-data/.runtime` was also restored to the
  live binary's copy after the aborted start had overwritten it.
- Consequence: **no PR #2619-based build can run on dev3's current DB** until
  the webhook migration reaches main and the branch is rebased (or the
  webhook session hands the instance over). The prior handoff's acceptance
  plan ("deploy #2619 on dev3") silently assumed the Sep-16 DB state.

### Acceptance side-instance (no impact on the live service)

- **Currently deployed code: `f31318aa4`**, restarted 2026-09-20 09:24 UTC
  after the takeover audit and real-container lifecycle fixes. UI/static export
  was rebuilt on `bbc7ea0b9`; subsequent changes are Go-only. The observed QA
  container runs OpenCode **1.18.30**. Older `75343b541` / `f67ed83a1` references
  describe deployment history, not current state.
- `https://crewship-dev3.unifylab.cz:8443` (Caddy block appended to
  `/etc/caddy/Caddyfile`, backup `Caddyfile.bak-zai-acceptance-20260918`;
  note: `caddy reload` is broken on this host — admin API disabled — so
  config changes require `systemctl restart caddy`, and the new block
  deliberately has no custom access log because `/var/log/caddy` is not
  writable for new files by the caddy user).
- Runs build f31318aa4 (`crewship.zai` + `crewship-sidecar.zai` in the
  release dir) on port 8093, socket `/tmp/crewship-zai.sock`, isolated
  `CREWSHIP_DATA_DIR=/tmp/opencode/zai-data`, isolated
  `CREWSHIP_STORAGE_BASE_PATH`/`CREWSHIP_LOG_PATH`/`CREWSHIP_BOLT_PATH`,
  own container network `crewship-zai-agents` and prefix `crewship-zai`,
  against `/tmp/opencode/zai-acceptance.db` — a copy of the pre-migrate
  snapshot `crewship.db.pre-migrate-v20260916090151-to-v20260916140000-*.bak`
  (schema exactly at this build's head version; dev3 data as of Sep 17
  08:43). Launcher: `/tmp/opencode/zai-run.sh`; log:
  `/tmp/opencode/zai-instance-f31318aa4.log`.
- **Isolation is partial by design**: database, storage, logs, state and
  container network are separate, but the instance shares dev3's persisted
  ENCRYPTION_KEY (deliberately — the snapshot copy's credentials must
  decrypt; the first boot minted a fresh key under the isolated data dir and
  broke the Keeper secrets store: 7 credentials failed to decrypt and
  `/keeper/execute` would have returned 500 at ALLOW. The launcher now
  sources `/home/ubuntu/.crewship/secrets.env` explicitly; boot logs show
  zero decrypt errors and a healthy secrets store). Pages origins are set to
  `https://crewship-dev3.unifylab.cz:8443` (studio and runtime), so generated
  page URLs point back at this instance, not at main dev3.
- The live `crewship-ws@3` (webhook build) was untouched and verified
  serving before and after; its staged sidecar under
  `/tmp/crewship-3-data/.runtime` was restored twice after aborted starts
  had overwritten it and verified byte-identical after the final deploy.

## Live acceptance checklist (owed — needs the user's key via UI)

On `https://crewship-dev3.unifylab.cz:8443`: add provider **Z.AI Coding
Plan** (key entered by the user in the UI — never via chat), then: streamed
completion with `zai-coding-plan/glm-5.3`; a tool-calling run; custom model
ID; invalid key → actionable error without secret leakage; second
same-product account → explicit selection; grant revocation → next run fails
closed; concurrent run on the live metered product if available; mobile
viewport pass. Record observed results, deployed code commit, CLI
version and nonsecret run IDs back into this document and the PR.

## Not done here (explicitly)

- Live acceptance with the real subscription key — NOT PERFORMED (see the checklist above; the side-instance is ready and waiting on the user, currently serving code commit f31318aa4).
- Zhipu/BigModel regional variants (`zhipuai`, `zhipuai-coding-plan`) —
  separate products, out of scope until an account exists.
- Z.AI vision/search/reader MCP services — a separate tools decision.
- The P2+ shared provider registry — this slice stays a vertical cut that the
  registry can later absorb; `zai-coding-plan` is one declarative identity
  away from it, not a precedent for unbounded enum growth.

## Takeover audit (2026-09-20)

See [security, performance and PRD audit](opencode-security-performance-audit-2026-09-20.md).
It reproduces and fixes delayed SSE delivery with a real HTTP regression,
adds scoped-account/revocation/cancellation coverage, measures parsing allocations,
and records remaining live acceptance and broader P2–P6 gaps. These source fixes
are not yet deployed to the acceptance instance.

## Live continuation results (2026-09-20)

See [acceptance evidence](reports/zai-acceptance-2026-09-20/results.json) and the
[audit continuation](opencode-security-performance-audit-2026-09-20.md#continuation-deployed-acceptance-and-runtime-defects-2026-09-20).

PASS on isolated dev3: desktop/mobile connection wizard, empty-key rejection,
masked entry, honest unverified label, encrypted persistence, list redaction,
separate account grants, account B→A restart, selected key absent from auth.json,
real Z.AI rejection of deliberately invalid key, custom native ID persistence,
removal of a grant from the running sidecar (55.45 s), and next-run refusal.
The sidecar restart failed before the owner-UID fix and passed afterward.

Still BLOCKED: successful paid GLM stream, actual tool execution, real token usage
and subscription entitlement/quota. No real Z.AI credential has been supplied.
All dummy credentials were deleted and the QA crew stopped; the acceptance UI
remains available. Account equality still uses existing pooling semantics; the
verified selection contract is one explicitly granted account per agent.

The final `f31318aa4` deployment also contains incremental SSE usage observation
and the two resolved CodeRabbit notes on stop diagnostics/UID guidance. Binary
and released-sidecar hashes match the build; public credentials page returns 200,
boot has zero ERROR events, and main dev3 artifact hashes remain unchanged.
The account-backed negative scenarios above ran on `a695a6dd3`; incremental usage
is verified by real-HTTP fixture/race/package tests, not a paid live stream.


## Current acceptance state — 2026-09-21

This section supersedes earlier current-deployment and key-missing statements.
The saved key was used with the user's explicit authorization; no re-entry is
needed. Current server code f4bd6b45c, sidecar hash ce3457d4ad83, public :8443
HTTP200. Persistent acceptance service/data live under
`/srv/crewship/zai-acceptance` (not /tmp). Workspace ZAI acceptance, crew
ZAI ověření, agent Správce záloh — test GLM (`spravce-zaloh-glm-test`).
The original credential owner has ADMIN access to this isolated workspace.
Main dev3 still has its original agent/configuration; no backup job was executed.

Real model response and bash tool call PASS. Token usage attributed to the
correct provider/credential PASS; billing flat_rate / GLM Coding Plan with
unknown monetary confidence (not a fabricated per-token price). Key retention
across a 66-second reaper watch PASS; grant removal reaped key in45.26s and
blocked the next run PASS; regrant automatically refreshed the sidecar and
completed another paid run PASS. Final binding is restored and readiness ready.
See [machine-readable evidence](reports/zai-paid-acceptance-2026-09-21.json).

Four live-discovered bugs were fixed in separate commits: solo-agent IPC,
subscription classification, provider-login reaper metadata, and replenishing
an empty sidecar after regrant. Each has a red-before/green-after regression.
Full affected sidecar/paymaster/orchestrator packages, targeted API race tests,
final vet and static frontend export passed. The broad Go run on the earlier e66da9a3b snapshot completed with exit0,
146 packages OK and0 FAIL; final CI and posted review remain gates. Two-account and real quota
exhaustion tests are not claimed. P2–P6 remain separate backlog. No merge yet.

### 2026-09-22 — connected-provider sidebar follow-up

The main dev3 screenshot shows the supported-provider catalogue inside a five-row, independently scrolling sidebar viewport. Z.AI was below the visible rows; Gemini and Zen were supported catalogue entries with zero connections. The filter now uses workspace provider facets with positive account counts and relies on the outer sidebar scroll. Add provider keeps the supported catalogue. The facet source is the complete provider-login list, before user filters. Regression coverage verifies connected Z.AI selection, exclusion of unused providers, and an empty workspace.

The GLM test agent remains in the separate `ZAI acceptance` workspace on `https://crewship-dev3.unifylab.cz:8443/chat/spravce-zaloh-glm-test`; the main dev3 database contains the original backup agent. Browser verification on September 22 confirmed the test agent name and Coding Plan credential are visible on the acceptance instance. This source change does not by itself update main dev3. Do not deploy the integration binary against main dev3's newer webhook schema.
