# OpenCode Go / Zen — PRD validation and next-agent handoff

Checked 2026-09-18, approximately 13:03 UTC. PR: [#2619](https://github.com/crewship-ai/crewship/pull/2619); issue: [#2618](https://github.com/crewship-ai/crewship/issues/2618).

## Verdict

The implementation covers the bounded integration described in [the integration PRD](opencode-go-zen-integration-2026-09-16.md). It is ready for real-account acceptance testing on dev3, **not yet signed off as end-to-end production complete**. Successful paid inference, actual subscription entitlement, real billing/limit behavior and final review of the latest changes remain unverified. This handoff is a code/evidence audit, not a new paid test or an independent security audit.

The original user request was provider-first Go/Zen integration with stored tokens and a usable agent workflow. The narrower implementation scope and its exclusions were documented during implementation; do not silently reinterpret those exclusions as newly completed features or user-accepted production acceptance criteria.

## Exact state

- Implementation head audited: `907c57e6f204dfcc03202b0c59a4119fc65652d6`, branch `feat/opencode-go-zen`. This handoff adds documentation after that code head.
- All executed GitHub checks on that head passed, including Frontend Test, Go, Go Shuffle, all three Go Race jobs, macOS/ARM64, Linux/ARM64, lint, build, Playwright and onboarding. [CI run](https://github.com/crewship-ai/crewship/actions/runs/35144908969). Seven optional checks were skipped; skipped is not evidence they ran. Some successful checks have annotations; inspect relevant annotations before final sign-off.
- CodeRabbit reviewed `25b369d16` and requested two minor changes. `54ce07c29` uses `QueryRowContext(t.Context())` in the test and clarifies the official provider-logo exception in `.coderabbit.yaml`. The latter reuses the existing OpenCode brand component; it does not waive security review. `907c57e6f` fixes two stale provider-count assertions.
- No actual posted review covers the latest code head. `scripts/review-status.sh 2619 --checks` reports ABSENT, although the status says Review completed. GitHub review decision remains CHANGES_REQUESTED. Do not merge on that green status.
- Dev3 was deployed on September 16. On September 18 its service is active, `/credentials` returns HTTP 200, the served JavaScript includes both provider names, and `.web-build-marker` is `907c57e6f`. This proves served UI availability, not authenticated inference.
- The main working directory now has branch `dev3/webhook-receiver` at the same code commit, with unrelated untracked PRD work. Preserve it. This handoff was prepared in `/tmp/crewship-opencode-handoff-2619` on the PR branch; do not assume a temporary worktree or old `/tmp` logs survive.

## PRD coverage matrix

| Requirement | Implementation / evidence | Remaining acceptance work |
|---|---|---|
| Discover and connect both products with API keys | Credential provider registry, guides, wizard tests and desktop/mobile fixture Playwright | Create real separately named accounts; verify save/reload, hidden secret and plan labels |
| Encrypted storage and assignment | `internal/api/provider_login_opencode_test.go`: encrypted provider login, independent slots, agent update; existing access-grant flow | Test real grants at workspace/crew/agent scope and denied access from an ungranted agent |
| Keep Go and Zen separate | Canonical `OPENCODE_GO` / `OPENCODE`; native `opencode-go/` / `opencode/`; separate sidecar routes and slots | Both credentials present: confirm selected product owns the run; multiple same-product accounts must select intended grant |
| Agent/model workflow | Agent create/edit, onboarding, templates, curated catalog and custom model IDs; API/UI regression tests | Save/reload and run through actual dev3 UI, including custom entitled model and agent edit |
| Preserve native protocols | `TestOpenCodeGatewayProxy` covers chat, Responses, Anthropic and Google paths, headers and codec selection; real CLI 1.18.31 mock transport probes | Successful streaming response with real account across available protocol families; tool call and usage journal |
| Keep selected secret out of agent env/config | `TestOpenCodeGatewaysRouteWithoutExposingKeys` checks dummy auth/config, sidecar delivery and revoked request state | Inspect actual container delivery without printing secrets; next run after revoke; concurrent Go/Zen runs |
| Wrong/missing key fails closed | Proxy test rejects other-product credential without forwarding; readiness tests | Actual no-grant, invalid key, revoked key and upstream denial; no silent billing-product fallback |
| Honest usage and pricing | Zen models.dev snapshot; `TestOpenCodeRatesKeepBillingProductsSeparate` makes Go `SourceNone` | Check UI does not portray unknown Go cost as verified free usage; verify token journal against console, without claiming dollar equivalence |
| Setup and billing guidance | PRD, CLI guide and provider-specific connection guides | User can find console and understand subscription versus metered usage without manual config |

Important test limits: dummy-key CLI probes intentionally returned local HTTP 401. They validate routing, native endpoint shape and session headers, **not a successful completion or entitlement**. Fixture Playwright does not prove live API grants. The revoke unit test removes credentials from a new request; it does not establish what happens to an already running container. Verify those distinctions explicitly.

## Next agent: proceed in this order

1. Read `AGENTS.md`, `CODEX.md`, this handoff and the integration PRD. Recheck live claims with `scripts/claim-issue.sh 2618 --check`, then claim before making commits. Fetch PR/main state and check whether another session has moved the branch or dev3. Preserve all WIP; use an isolated worktree for edits. Do not blindly switch or reload the current main checkout.
2. Review the implementation against the matrix, especially selected credential identity when both products or multiple accounts are granted, model qualification, usage parsing and auth propagation. Upstream source links are recorded in the PRD; refresh official docs/catalog before changing the product contract or models because the recorded research is dated September 16.
3. Use `https://crewship-dev3.unifylab.cz/credentials`, instance 3 only. Ask the user to enter real API keys through the UI if no suitable test accounts are available; never request keys in chat or commit them. Use a disposable test agent/crew and small bounded prompts. Do not buy subscriptions, change upstream billing/Use balance, or intentionally exhaust quota as part of acceptance.
4. Run the manual checklist below and record observed result, deployed commit, CLI version, nonsecret run ID and evidence. Mark unavailable product/protocol coverage as untested, not passed. If a failure is reproducible, fix it with a regression test; distinguish unrelated platform flakes from integration defects.
5. After code changes run `go test ./... -count=1`, `go vet ./...`, and relevant frontend lint/build/tests. The original full database suite took about 28 minutes on this host; use a documented timeout such as `-timeout 30m` if necessary, not disabled tests. Inspect CI on the exact pushed SHA. Documentation-only handoff does not invalidate the recorded code-head tests, but new-head CI still needs checking.
6. Obtain actual posted review covering the final changes. Use the repository's single-PR review retrigger procedure when authorized to request it; avoid the whole-queue retrigger. Confirm the posted review body/commit, not just a green check. Inspect/resolve outstanding findings based on current code.
7. Update this matrix and the PR body with final evidence and remaining gaps. Release the issue claim with the outcome. Do not declare paid E2E complete without successful account-backed runs. Merge only with authorization and completed review; testing on dev3 does not authorize production deployment.

## Dev3 acceptance checklist

- Desktop and phone: Credentials → Add provider → Go / Zen → enter key → save → assign access. Verify naming, product label, secret masking and persistence after reload.
- OpenCode runner: create agent with Go, select an entitled `opencode-go/...` model, run a minimal prompt, observe streamed successful completion and journal usage. Repeat with Zen and `opencode/...`. Record which models really worked; catalog availability is not account entitlement.
- Edit provider/model and reload. Exercise a custom native model ID supported by that account. Confirm a wrong or unavailable model yields a useful error rather than a fallback to another payer.
- Both keys granted, and separately multiple named same-product accounts: confirm intended credential selection. Use nonsecret credential/run identifiers for evidence; do not dump environment variables, auth files or request headers containing actual keys.
- No grant / wrong-product grant: run must not successfully use another account. Revoke the selected grant and run again; test active-run revocation separately and document actual semantics.
- Invalid/revoked upstream key: actionable failure and no secret in user-visible error/log output. Test naturally occurring quota denial only if available; do not exhaust a subscription to manufacture it.
- Native protocol coverage: at least one real successful completion per product; then exercise available chat/Responses/Anthropic/Google families as account access permits. Include one tool-using run. Verify auxiliary traffic uses the selected gateway and usage events are sensible.
- Billing display: Zen is an estimate based on its snapshot; Go usage must not imply known free marginal billing or remaining quota. Check actual UI handling of unknown costs. Upstream console is authoritative.

## Deployment caveat

`crewship-ws@3` originally used `pages-demo.conf` and a rebuild-on-start launcher, but **September 18 inspection found an additional `zz-unsigned.conf` override** setting ExecStart to `/srv/crewship/dev3-pages-release/start-unsigned.sh`. The running executable is `/srv/crewship/dev3-pages-release/crewship.unsigned`. The marker and live assets match the expected frontend, but the backend commit has not been independently established from that executable. Do not infer backend identity solely from `.web-build-marker` or the working tree. Before further acceptance, inspect the current effective unit/launcher and confirm the running backend build through authenticated version information. A reload terminates the process and invokes the current launcher; do not assume it rebuilds or deploys PR #2619. Coordinate checkout and deployment on instance 3, then verify both frontend and backend identity. The current task did not redeploy, alter billing, merge, or create real credentials.

## Deliberate exclusions, not unfinished promised features

No upstream workspace provisioning/member synchronization, subscription purchase, live balance/quota API, automatic quota failover, or Go token-derived rate card. No gateway-specific support for Claude Code/Codex/Gemini runners or Keeper's own LLM. Models are curated plus custom IDs, not live account entitlement discovery. If product acceptance requires any of these, define a separate explicit scope before implementing it.

## Newly requested expansion

The user subsequently requested broad OpenCode provider coverage, especially a newly purchased Z.AI product. Read [the supplemental provider analysis](opencode-provider-expansion-2026-09-18.md) and its complete catalog inventory. This is follow-up scope, not part of the completed Go/Zen implementation. The user confirmed GLM Coding Plan subscription; prioritize integrating `zai-coding-plan`; preserve Codex and Claude Code.
