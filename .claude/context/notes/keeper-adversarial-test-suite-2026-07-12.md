# Keeper — sophisticated adversarial + performance test suite

**Date:** 2026-07-12 · **Target:** dev3 (`https://crewship-dev3.unifylab.cz`) · **Auth:** OWNER, non-destructive recon only.

The keeper is the credential/authorization **chokepoint** (access request → gatekeeper LLM
allow/deny/escalate → credential injection into a container exec). Existing unit tests cover the
**static single-shot** surface well (peer-cred IDOR, cross-workspace/crew spoof, SQLi/path-traversal/
null-byte in `credential_id`, `requesting_agent_id` not overrideable, `%q` prompt-injection escaping).
The core gate is genuinely **fail-closed**: deny-on-evaluator-error (`keeper_request.go:199`,
`keeper_execute.go:254`), deny-on-nil-provider (`gatekeeper.go:270`), and a **TOCTOU re-validation**
of status+assignment *after* the seconds-long LLM round-trip (`keeper_execute.go:323`).

So the sophisticated tests must hit what unit tests can't: **stateful, temporal, concurrent,
cross-layer, and deployment/config** surfaces. Priority = where the chokepoint doctrine predicts
per-code-path divergence and fail-open/fail-silent edges.

---

## Tier 0 — Live findings already validated on dev3 (non-destructive)

- **L0.1 — Internal keeper endpoints are internet-reachable; the network gate is a no-op behind the
  reverse proxy.** Per `internal.go:198`, a non-internal source IP should get **404**. From the public
  internet dev3 returns **403** (token reject) for `POST /api/v1/internal/keeper/execute` and
  `/skill-review`, but **404** for an unregistered route — proving the request **passed the network
  gate**. Caddy presents all traffic to crewshipd as local; `X-Forwarded-For` is ignored (RemoteAddr =
  proxy = localhost). **Net: the only guard on the keeper internal surface is the static shared
  `X-Internal-Token`.** This makes finding **C** (below) a live, not theoretical, precondition.
- **L0.2 — Baseline chokepoint holds.** No token / bogus 32-hex token / XFF-spoofed all → 403. Method
  mismatch → 405. No auth bypass on the naive surface.
- **L0.3 — Read path stable under 50-way concurrency.** 50 parallel `escalation pending-count`:
  all succeeded, p100 ≈ 1.72 s, wall 1.78 s, zero rate-limit rejections at this level.
- **Token leak vector:** `X-Internal-Token` is **not** in the local `.env.local` (server-side only,
  injected into each sidecar). Leak path is therefore *any agent reading its own container env* — not
  a local-file exposure. Combined with L0.1, that agent can replay the token from anywhere.

---

## Tier 1 — Highest-value adversarial tests (build these first)

### T1 · Static internal-token blast radius (confirmed precondition)
**Hypothesis:** `X-Internal-Token` is a single static shared secret distributed to every sidecar;
the execute handler trusts body `requesting_agent_id` + `container_id` (`keeper_execute.go:58,357`,
map §2/§5). With the network gate defeated (L0.1), a token holder from *anywhere* can impersonate any
agent and exec in any container.
**Mechanism:** malicious/compromised agent reads `X-Internal-Token` from its env → replays
`POST /api/v1/internal/keeper/execute` from off-host with `requesting_agent_id` = victim, `container_id`
= victim container.
**Run:** two crews with distinct assigned creds; from a shell holding the token, request victim's
credential naming attacker's identity vs victim's identity. **Fail signal:** any 200/ALLOW where the
body identity ≠ the token's provisioning agent. **Fix direction:** bind the transport token to the
acting agent (per-agent HMAC, like F24 workspace binding), or move `container_id` out of the body.
*Stop short of real secret exfiltration on dev3 — a single accepted request proves the radius.*

### T2 · Deferred-approval TOCTOU (the doctrine's twin-path prediction)
**Hypothesis:** the **synchronous** execute path re-validates status+assignment post-eval
(`keeper_execute.go:323`). The **escalate → human-approves-later → resume** path is its untested twin.
If resume trusts the stored decision without re-running that JOIN, an approval granted at T0 is still
honored after the credential is rotated/unassigned at T1.
**Mechanism:** request an L3/L4 cred → forces ESCALATE (`keeper_request.go:282`) → inbox item. Rotate
or unassign the cred. Approve the escalation. Observe whether the resumed injection uses the stale
secret.
**Run (harness):** drive a real agent to escalate; between escalate and resolve, `crewship credential
rotate`/`unassign`; then approve; assert the executed command never sees the pre-rotation value.
**Fail signal:** stale secret injected. This is the single most likely real hole.

### T3 · Command-denylist bypass + scrubber gap → secret exfiltration
**Hypothesis:** `containsDangerousShellChars` (`keeper_helpers.go:63`) is a **denylist** and the output
scrubber (`keeper_execute.go:382`) only masks a fixed encoding set (base64/url/hex/reversed). A command
that (a) passes the denylist and (b) transforms the secret through an encoding **not** in the scrub set
exfiltrates it in cleartext-recoverable form.
**Mechanism:** with a legitimately assigned cred, submit commands whose *approved* purpose is benign but
that emit the secret via: `tr` substitution, `gzip|base64` split across lines, `od`/`xxd` byte dump,
per-char echo, DNS/HTTP timing side channel, or a homoglyph/locale interpreter name.
**Run (harness):** for each transform, execute and grep the returned output for the recoverable secret.
**Fail signal:** the secret is reconstructable from unscrubbed output. **This surface has no unit test.**

### T4 · Risk-laundering by chunking (L1 auto-allow aggregation)
**Hypothesis:** the L1 fast path auto-allows with **no LLM and no human** when 5 heuristic conditions
hold (`gatekeeper.go:255`). A privileged effect split into N individually-benign L1 steps each
auto-approves; the aggregate is an escalation the gatekeeper would deny as one request.
**Mechanism:** decompose "read+exfil secret X" into N self-service L1 accesses/commands, each passing
`hasMinDistinctChars`/`looksLikeIntentInjection`, reassembled agent-side.
**Fail signal:** aggregate capability exceeds any single approved decision. **Fix:** rate/aggregate
budget per agent per credential across a window; the gatekeeper sees single requests, not the trajectory.

### T5 · L1 auto-allow heuristic evasion
**Hypothesis:** `looksLikeIntentInjection` only *diverts to the LLM*, it does not deny; and it is a
keyword blocklist (`gatekeeper.go:56-88`). An intent that is manipulative in effect but avoids the
markers and clears `hasMinDistinctChars(≥5)` auto-allows.
**Run:** fuzz L1 intents that carry an embedded instruction using paraphrase/unicode-confusables/
zero-width chars absent from the blocklist. **Fail signal:** auto-ALLOW on an intent a human would
flag. Pairs with `gatekeeper_inject_sec_test.go` but targets `buildAccessPrompt`, which those tests
do **not** cover.

---

## Tier 2 — Fail-silent / audit-integrity (correctness under load)

### T6 · Audit suppression under DB write pressure
**Hypothesis:** the access/execute paths **swallow** `keeper_requests` INSERT failures and continue
("Non-fatal — continue", `keeper_request.go:152`, `keeper_execute.go:233`) — the opposite of the F4
path, which 500s. Under write contention, an ALLOW+exec can happen with **no audit row**.
**Run:** sustained concurrent write load + a stream of legitimate assigned-cred executes; diff
`crewship system keeper` decision count vs the number of injections actually performed.
**Fail signal:** injections > audit rows. **This is the sharpest fail-silent in the subsystem.**

### T7 · Returned-vs-persisted decision mismatch
**Hypothesis:** decision UPDATE failures are logged-and-swallowed (`keeper_request.go:229`,
`keeper_execute.go:287,418`); the response returns the live decision but the stored one can lag.
**Run:** induce UPDATE failure window; compare API response decision to the row later read via
`keeper request GET`. **Fail signal:** divergence (an ALLOW recorded as PENDING/DENY or vice-versa).

---

## Tier 3 — Concurrency / availability

### T8 · Escalation-inbox flooding (advisory loss)
**Hypothesis:** ESCALATE→inbox and all F4 advisory inserts are **fire-and-forget**
(`keeper_request.go:282`, phase2 `_ = inbox.Insert`). Flooding escalations can drown or drop a
legitimate one; operator bell lost. Availability, not access.
**Run:** burst N ESCALATE-inducing requests, then submit one "real" escalation; assert it still surfaces
in `crewship escalation list`. **Fail signal:** the genuine escalation is missing or unattributed.

### T9 · Evaluator saturation → prove fail-closed under load
**Hypothesis:** deny-by-default is proven on evaluator *error*; verify it also holds on evaluator
*timeout/ctx-cancel* under saturation (`gatekeeper.go:23` flags local-provider blocking). Doubles as a
measurement of the **TOCTOU window width** (T2's exploitability = eval latency).
**Run:** saturate the gatekeeper LLM slot with concurrent requests to force ctx deadline; assert the
decision is DENY, never ALLOW; record p99 eval latency = the T2 race window. **Fail signal:** any ALLOW
emitted on a timed-out evaluation.

### T10 · Double-execute race on one approved requestId
**Hypothesis:** re-validation is per-execute; two concurrent executes of the same approved `requestId`
both pass the ACTIVE+assignment check. Idempotency of side effects unverified.
**Run:** fire two simultaneous `/execute` for one requestId. **Fail signal:** the command runs twice /
double side effect / two audit rows for one approval.

---

## Tier 4 — Cross-tenant, policy, and deployment fail-opens

### T11 · `resolvePolicySafe` fail-open (strict → warn)
**Hypothesis:** on policy-resolve error the F4 path falls back to **guided/warn**, not blocked
(`keeper_phase2.go:179`). A policy-store outage silently downgrades a strict crew to non-blocking on
skill/behavior/memory surfaces.
**Run:** break/point policy resolution at a bad store; submit a behavior event for a strict crew;
observe non-blocking. **Fail signal:** strict crew action proceeds without the expected inbox block.

### T12 · Token-less boot-identity fallback (legacy fail-open)
**Hypothesis:** `actingAgentID` falls back to the **boot identity** when no per-agent tokens are
provisioned (`identity.go:100`). In such a deployment any local caller is attributed to the boot agent.
**Run:** stand up a sidecar with `tokensProvisioned()==false`; call `/keeper/request` from a second
process; assert it is **refused**, not attributed to boot. **Fail signal:** attribution to boot agent.

### T13 · `AgentMemoryDir` path injection (negative-learning)
**Hypothesis:** the negative-learning F4 body carries `AgentMemoryDir` straight into
`consolidate.WriteLesson` on the auto-apply path (`keeper_phase2.go:587,672`). If it isn't validated
against the agent's real dir, a traversal writes a lesson into another agent's memory (needs
`self_learning_enabled=1`).
**Run:** submit negative-learning with `AgentMemoryDir` = `../<other-agent>/` ; assert rejection.
**Fail signal:** cross-agent memory write — poisons the memory moat.

---

## What "sophisticated performance test" means here

Not RPS. The valuable perf tests are **correctness-under-load**: T6 (audit drops under write pressure),
T9 (fail-closed holds + window measurement), T10 (idempotency under concurrency), T8 (advisory loss
under flood). Each asserts a **security invariant survives saturation**, which is exactly where
per-code-path enforcement tends to diverge from the intended guarantee.

## Suggested build order
T1 (precondition proven) → T2 (most likely real hole) → T3 (no coverage, high impact) → T6 (sharpest
fail-silent) → T9 (cheap, measures T2) → rest. T1/T6/T9 are runnable against dev3 today; T2/T3/T13 want
a harness script driving a real agent container.
