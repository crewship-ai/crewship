# OpenCode / Z.AI P1 takeover audit — 2026-09-20

Scope: PR #2622 on `48022fdd0`, including its dependency on Go/Zen PR #2619. The user explicitly requested takeover, security/performance audit and PRD validation. This report distinguishes fixes, observations and unverified acceptance. This is a focused integration audit, not a penetration test of every Crewship subsystem or certification of all 222 catalog providers.

## Findings and disposition

### Fixed: SSE delivery buffered by the HTTP server (high user impact)

`copyAndObserveLLM` used `io.Copy` directly to `http.ResponseWriter` without a flush. Small SSE chunks could remain in net/http's buffer until enough output accumulated or upstream closed. The old recorder-based tests saw Write calls immediately and therefore missed actual client latency.

A new regression uses a real `httptest.Server` and an upstream pipe that remains open until the client reads its first event. Before the fix it fails at the two-second context deadline; after the fix it succeeds. Both usage-observed and unobserved paths are covered. The proxy now flushes each SSE write using `http.ResponseController`; unsupported flush implementations retain their previous behavior, and actual write/flush errors terminate copying. Non-SSE responses retain their existing path. This fixes the common helper used by Go/Zen/Z.AI, so the regression surface includes other native SSE providers.

### Reduced: unnecessary SSE line-array allocation

The usage parser split the entire retained response into a slice of strings before parsing events. It now iterates lines using `strings.SplitSeq`, trimming a terminal CR per line. This preserves multiline event and CRLF handling without allocating the whole line-index array. Existing parser tests and the new proxy tests pass.

### Original finding: large-stream usage capture and parsing cost (subsequently fixed below)

Observation retains only the first 10 MiB and parses after EOF. If final usage is later than that boundary, the ledger can miss it. The cap limits retained payload, not cumulative allocations or the sum over concurrent requests. Many tiny JSON events cause substantial parser allocation churn. These behaviors predate Z.AI and are not solved by adding a provider card or by the flush fix.

Follow-up design: incremental bounded SSE event parsing with last/cumulative usage state, explicit truncation/unknown-usage telemetry, and compatibility fixtures for Anthropic message starts, OpenAI Responses envelopes, Google usage, multiline SSE and malformed/truncated streams. Do not replace absent usage with a claimed zero-cost success. No unmeasured concurrency or production latency SLO is claimed here.

### Account selection is priority/pool semantics, not explicit per-run identity

`CredStore.Select` chooses the lowest numerical priority and round-robins within that tier per acting agent. `credentialFor` predicts a member of the tier for configuration; fixed Z.AI endpoints do not differ between accounts. A new test verifies agent scope and priority preference. This is adequate for an agent granted one account (or a uniquely preferred account), but it is not a dedicated immutable connection-ID binding when multiple equal-priority accounts are granted. Automatic template assignment links all matching active product credentials.

PRD acceptance must disclose this behavior and test selection through actual grants. If the product requires pinning one account while several remain equally granted, implement an explicit connection binding rather than asserting determinism from these tests. Do not globally change existing credential-pool semantics as an incidental audit fix.

### Secret isolation has a defined boundary

The selected routed Coding Plan credential is replaced with dummy auth in agent configuration/auth.json; the backend still receives/decrypts it and the sidecar holds it for injection. “Only ever in sidecar heap” is false as a system-wide statement. Existing auth rendering can deliver other granted, non-routed providers' keys to the agent. Selected-product isolation does not establish isolation of every credential granted to that agent; live acceptance must use minimum grants and inspect delivery without printing secret values.

The new direct proxy test distinguishes actual metered `ZAI` from Coding Plan, then loads the Coding Plan key, then clears the store and verifies only the granted request reached upstream. This establishes next-request behavior on a refreshed store, not end-to-end revocation propagation timing or termination of an already active run. A cancellation test confirms the inbound context reaches the upstream transport.

## Security coverage and limits

| Boundary | Evidence | Limit |
|---|---|---|
| Product/key split | Fixed Coding Plan host/base path; metered ZAI rejected; selected secret omitted from agent env/config tests | Does not validate account entitlement |
| Agent isolation | Store selects only eligible grantees; priority test includes an ungranted agent | Full route identity/grant propagation uses existing machinery and still needs live acceptance |
| Revocation | Empty refreshed store refuses next request without forwarding; API loaders filter ACTIVE/nondeleted | Existing containers and in-flight runs require live measurement |
| Cancellation | Blocked transport exits after request context cancellation | No paid upstream call was made |
| SSRF/redirect surface | Z.AI has a fixed upstream; proxy uses RoundTripper, not redirect-following Client; existing custom endpoint policy unchanged | Not an exhaustive network-policy audit |
| Storage | Existing encrypted provider-login path and regression tests retained; no crypto-layout change | No real Z.AI key available in audit |
| Billing | Subscription remains unpriced; provider ledger is distinct | UI unknown-cost semantics and real usage still need live acceptance |
| Acceptance environment | Read-only status checks; no ACTIVE/nondeleted credentials at audit start | Copy retains ciphertext plus shared decryption key; not physical secret separation |

At the initial audit stage no production account was accessed, no secret printed, no billing/subscription changed and no service restarted. Acceptance then served `75343b541`. The user subsequently authorized the isolated deployments recorded below; these do not alter the main dev3 service.

## Performance measurements

Commands: `go test ./internal/sidecar -run '^$' -bench 'BenchmarkZAI' -benchmem -benchtime=100ms -count=3`. Host reports 12 logical CPUs. Runs occurred on a shared host while other tests compiled; timing is noisy, allocations are more useful. Synthetic payload is many tiny `data: {}` events, not a representative model completion. Benchmark setup excludes payload construction for the observation measurements but includes request/recorder setup for the full proxy measurement. No network/provider latency is included.

| Measurement | Observed result |
|---|---|
| Small complete Coding Plan proxy request | 12.6–14.3 microseconds/op; about 42.8 KB allocated/op; 76 allocations/op |
| ~1 MiB tiny-event stream, before line iteration change | ~24.96 MB cumulative allocation/op |
| ~1 MiB, after | ~21.60 MB/op (about 13% lower) |
| ~12 MiB stream, capped observation, before | ~250.67 MB cumulative allocation/op |
| ~12 MiB, after | ~217.11 MB/op (about 13% lower) |
| First small SSE event | Before: not delivered within 2 seconds while upstream open. After: delivered before EOF in both observer modes |

These are cumulative allocations, **not peak RSS**. Observed timing ranges did not establish a throughput improvement; no such improvement is claimed. The large synthetic allocation count remains a follow-up concern despite the reduction. `BenchmarkZAIStreamObservation` and `BenchmarkZAICodingPlanProxy` are committed so another reviewer can repeat the measurement. Logs under `/tmp/zai-audit-*` are transient supplementary evidence; the tests/benchmarks are the durable reproduction.

## PRD acceptance status

| Stage | Status |
|---|---|
| Go/Zen P0 code and fixture coverage | Present; final account-backed acceptance not established here |
| Z.AI P1 connect/store/route/model/UI | Implemented; security and streaming coverage extended in this audit |
| P1 actual GLM stream + tool call | BLOCKED: user must enter Coding Plan key through acceptance UI |
| Real grant/revocation, account selection, phone workflow, costs | OPEN: mock/store tests are not substitutes |
| P2 shared registry and all inventory IDs classified | Not implemented by this PR |
| P3 long-tail API/plan providers | Not implemented by this PR |
| P4 Bedrock/Vertex/Azure identity | Not implemented by this PR |
| P5 custom/local/special plugin expansion | Follow-up scope; existing generic support is not full acceptance |
| P6 OpenCode V2 | Evaluation backlog, no migration performed |
| Final CI/review/merge | Recheck on audit commit; no merge authorized by this audit request |

## Completion gates

1. Validate the final audit commit with full Go tests/vet and CI; obtain actual posted review rather than the status badge.
2. Deploy only to the isolated acceptance service after checking its current launcher/build/DB compatibility. Do not deploy this old branch against the main dev3 database with the newer webhook migration.
3. User enters a real Coding Plan key through `/credentials` on :8443. Prove stream, actual tool execution and usage with nonsecret run IDs, then grant/invalid-key/revocation/selection cases. Do not consume quota deliberately; multiple real accounts only if supplied.
4. Resolve the account-binding and large-stream usage limitations according to acceptance scope; preserve them explicitly if deferred. Do not claim the whole expanded PRD is complete.
5. Merge order remains #2619 then #2622 only after permission and review. Retest the final integration with current main as required.
6. Keep acceptance available while the user tests. Retire only its resources after agreed completion, preserving main dev3 and the shared key. Release issue claims on handoff.

## CI security follow-up

CodeQL on integrated head `9f1c707e7` flagged the new raw SSE Write as a possible
XSS sink. The path is intended for event-stream data, but this invariant is now
explicit: proxy response headers are canonicalized to `text/event-stream` with
`X-Content-Type-Options: nosniff` before committing the status, and the flush
writer maintains that invariant at its byte-write boundary. A regression sends
HTML/script text as SSE data and verifies unchanged bytes plus inert headers;
the real-HTTP first-event test checks the headers as received by a client.
No CodeQL suppression was added. On `a27858dbd`, both CodeQL language analyses passed and the CodeQL result reported "No new alerts in code changed by this pull request" (check `106048459157`, zero annotations). Subsequent changes only improve test diagnostics and this report; their final CI/review remains a separate gate.

A serial read-only live availability sample (20 HTTPS GETs to the acceptance
credentials page) measured median 4.09 ms, p95 5.85 ms, max 18.46 ms. This measures
static UI availability from the same host, not inference or authenticated API
performance. SQLite EXPLAIN of auto-assignment on the acceptance copy used
`idx_credentials_ws_created` and the credential-fields unique index; no full
credentials-table scan was observed.

## Final verification details

The production-code head `a27858dbd` passed the complete sidecar suite (80.765 s), focused race tests and vet. Current-main integration also passed `go vet ./...` plus focused API/orchestrator/sidecar tests. The full local repository suite began before integration with current main and before the final MIME hardening; its result must not be described as a complete final-SHA run. Final-SHA repository-wide CI is the separate authoritative gate.

After MIME hardening, repeated synthetic benchmarks measured 13.6–20.3 microseconds/op, 42.8 KB/op and 79 allocations/op for the small proxy request, ~21.63 MB for 1 MiB observation and ~217.43 MB for 12 MiB observation. The earlier table records the parser-change comparison; the final figures still support approximately 13% less cumulative allocation, not a throughput claim.

CodeRabbit posted a real review for `55af87b2c` with two trivial test-quality notes (helper annotation and named subtests); both were addressed. No newer completed review is claimed. Final-head rate-limit status is not approval.

## Continuation: deployed acceptance and runtime defects (2026-09-20)

User authorized deployment and continued integration testing. A complete `make build`
of `bbc7ea0b9` was deployed to the isolated :8443 / :8093 service at 08:46 UTC.
The main `crewship-ws@3` remained active; SHA-256 of its server and staged sidecar
matched before and after. A SQLite online backup and both previous binaries are
retained under `/tmp/opencode/zai-deploy-bbc7ea0b9/` (directory mode 0700).

Playwright against that deployed UI passed desktop and 390×844 touch wizard
checks: blank-key validation, masked input, honest unverified-connection label,
save, visible mobile actions and cancel. A dedicated `zai-audit-20260920` workspace
was created through the CLI using an existing `.invalid` audit account whose
password was reset **only in the acceptance copy**. No operator password changed.
Two deliberately invalid test credentials demonstrated encrypted persistence,
redacted list responses, separate grants for agents A/B, and slot removal on
unassignment. These checks do not prove vendor authentication.

Actual runtime testing uncovered two defects hidden by an empty acceptance setup:

1. UFW did not allow the new Docker bridge to reach the acceptance backend.
   Sidecar IPC timed out, including credential reaping. Added only:
   `ufw allow in on br-cd3f08b97944 proto tcp from 172.26.0.0/16 to 172.17.0.1 port 8093`
   (comment `ZAI acceptance sidecar IPC #2621`). Container-to-backend health now
   returns 200. Remove this exact rule when retiring the acceptance network.
2. On account/configuration changes, sidecar stop ran as UID 0 despite containers
   dropping all capabilities. A live signal-0 probe failed with `Operation not
   permitted` as root and succeeded as UID 1002. Replacement then failed to bind
   port 9119, while health mistakenly accepted the old process. The fix stops
   as UID 1002, waits for a confirmed successful exit, and aborts on failure.
   A regression verifies both the owner identity and refusal to launch after
   a failed stop. This is a shared lifecycle defect, not a vendor outage.

Managed OpenCode products now reject a missing model credential before any CLI
exec, with instructions to connect and grant the provider. This avoids the
native CLI's unhelpful UnknownError when authentication is absent. Other
adapters and unmanaged/custom OpenCode products retain their existing behavior.

The actual fresh QA runtime contains **OpenCode 1.18.30**, not the earlier
agent's reported 1.18.31. Every future live result must name the runtime actually
used. Lifecycle fixes were deployed as `a695a6dd3` at 09:02 UTC. Repeat live runs
passed: missing grant returned an actionable error before CLI exec; an invalid
key reached the real Z.AI endpoint and returned `token expired or incorrect`;
B→A account switching selected the expected credential ID without a bind error.
The real test key was absent from the run's auth.json and backend log.
After unassignment, the running sidecar dropped the key within 55.45 seconds,
and the next run failed closed. This measures revocation after a completed run,
not cancellation of a request already forwarded to the vendor. Custom native
model IDs persisted through the update API (verified with a read-only DB query).
All dummy credentials were deleted and the QA runtime stopped. Evidence and
screenshots: [acceptance report](reports/zai-acceptance-2026-09-20/results.json).
Successful paid stream/tool/usage checks still require the user's real Coding
Plan key. The earlier failed runs remain failed diagnostic evidence.

## Incremental SSE usage follow-up

Replaced first-10-MiB capture with a bounded per-event observer. Delivery/flush
happens before observing each chunk. Completed events are parsed incrementally
with reusable line scratch and cumulative usage state, so a final usage event
after more than 10 MiB is still observed, including Anthropic input-at-start and
output-at-end. Chunk boundaries, multiline CRLF and an unterminated final event
are covered. The individual event limit remains 10 MiB; oversized events are
drained without retaining more payload or interrupting response delivery. If any
event is skipped, the observer logs a warning and omits the usage/billing callback
(including quota-triggered callbacks), rather than recording incomplete usage as
a zero-token bill. Quota headers/status still reach the client; their usual
observer update is also omitted in this exceptional oversized-event case.

Focused race tests passed, as did the complete sidecar suite (65.939 s) and vet.
The final oversized/quota suppression branch was additionally race-tested.
A new regression preserves initial and final usage across a >10-MiB stream.
This closes the total-response capture limitation; malformed/oversized individual
events still cannot provide verified usage and remain explicitly unknown.

Updated synthetic benchmark (same shared host; no network): 1 MiB tiny events
allocate ~17.84 MB cumulatively, versus ~21.63 MB before this change (~17.5% less).
Allocation **count increases** (~629k versus ~525k); elapsed time does not establish
a throughput gain. The 12 MiB test allocates ~213.94 MB while now parsing all 12 MiB
instead of the old first 10 MiB, so those timings/counts are not like-for-like.
The small mocked proxy uses ~11.25 KB/op; its string-reader fast path differs from
a real HTTP response, so this is not a claim of equivalent production savings.
Retained event bytes are bounded in tests; no production peak-RSS claim is made.

The previous full repository Go run covers the lifecycle fix; this additional
sidecar change has its own full package/race/vet checks and requires final-head CI.
Paid streaming acceptance is still blocked on the user's key.

## Current deployment and review

Current code deployment is `f31318aa4` (2026-09-20 09:24 UTC), including the
incremental observer and CodeRabbit follow-ups. Public :8443 returns 200; the
running binary and released sidecar match their build hashes. Main dev3 server
and staged sidecar hashes remain unchanged and its service is active. Acceptance
boot has zero ERROR events; there are zero active credentials after dummy-key
cleanup. Paid completion/tool acceptance remains BLOCKED.

Final complete sidecar package: PASS (17.554 s); focused SSE race tests PASS
(1.616 s); lifecycle/guard race tests after review PASS (1.096 s); full vet PASS.
The full repository run started before the incremental observer; its result is
reported separately in the PR. Final-SHA CI is a separate gate.

Actual CodeRabbit review `5260205800` covers `51c980121` (posted 09:20:53 UTC):
one minor error-context finding and one UID-guidance nitpick, both addressed.
It does not cover the subsequently added incremental observer. Do not infer
final approval from that review or from a rate-limited green check.


## Paid acceptance follow-up, 2026-09-21

The user supplied a real Coding Plan key on main dev3 as `ZAI`, bound to
Správce záloh while that agent still selected OpenCode Go/Kimi. At the user's
explicit request to use the saved key, the operator migrated only that key
in memory into the isolated acceptance API as `ZAI_CODING_PLAN` (stdin, no
plaintext file/argv/output). Main credentials, binding, agent and DB remain
unchanged. The acceptance copy uses its own at-rest encryption key.

Acceptance workspace `zai-acceptance`, crew `zai-verification`, agent
`spravce-zaloh-glm-test` (regular AGENT, OPENCODE,
`zai-coding-plan/glm-5.3`). The original credential owner has ADMIN membership
in this test workspace. The separate QA login was reset only in the acceptance
DB; no human login was reset.

On f31318aa4, real runs completed with exit 0:
- `msg_1789986117576327952_bd8ca6d877975c39`: "GLM připojení funguje."
- `msg_1789986145336788019_ed8c2e36248d664d`: journal captured an actual bash
  tool call `printf GLM_TOOL_OK`, output GLM_TOOL_OK, exit 0.
The reverse proxy log confirms the ZAI_CODING_PLAN credential and coding route.

These runs exposed a real gap: solo non-lead agents omitted IPC at sidecar boot.
Consequently usage was silently discarded and credential reaping lacked its
internal API configuration. The new regression
`TestRunAgent_SoloAgentCarriesIPC` failed before the fix, and passes with race
instrumentation after it. Initialize IPC for every agent when its base URL is
configured; role-specific action authorization and scoped tokens remain intact.
Post-fix deployment and paid usage/revocation results will be recorded separately.


Post-IPC live run `msg_1789986828345723275_0e30ac511a349f4b` completed
with a real bash tool call and three usage rows attributed to the correct
credential. Those rows exposed a second issue: API-key auth inherited metered
billing defaults, yielding precise $0 instead of subscription/unknown cost.
The ZAI coding route now emits flat_rate / GLM Coding Plan for both JSON and
SSE observations; ordinary ZAI remains metered. The new table-driven billing
regression failed before the correction for both coding response formats.

Deployment requires recreation of existing crew containers to replace their
bind-mounted sidecar. The acceptance systemd unit's overly restrictive umask
also prevented server access after Docker changed crew directory ownership to
UID/GID1001. Corrected only the isolated unit to UMask0002 plus supplementary
GID1001, and made only its crew directories group-accessible. The top-level
acceptance directory remains0700; DB and secrets remain0600. Agent1001 and
sidecar1002 UIDs are unchanged. No shared main-dev3 directories were changed.


A >60-second inspection exposed another pre-existing gap: the internal
credential metadata listing excluded PROVIDER_LOGIN entirely. The reaper
therefore removed the still-granted key at its first tick. The first attempted
revocation measurement is INVALID (key was already gone); it is not evidence
of revocation latency. Added provider-login metadata, scoped to live legacy
grants or workspace/crew/agent bindings. Plaintext include_values/global pool
keeps its previous type filter. Workspace visibility alone does not keep a
provider login after its grant is removed. Tests cover both grant mechanisms,
soft-deleted agents, other crews, workspace bindings, revocation, and exclusion
from the plaintext global pool. Existing legacy credential scoping is unchanged.
