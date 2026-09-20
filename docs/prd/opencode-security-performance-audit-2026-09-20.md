# OpenCode / Z.AI P1 takeover audit — 2026-09-20

Scope: PR #2622 on `48022fdd0`, including its dependency on Go/Zen PR #2619. The user explicitly requested takeover, security/performance audit and PRD validation. This report distinguishes fixes, observations and unverified acceptance. This is a focused integration audit, not a penetration test of every Crewship subsystem or certification of all 222 catalog providers.

## Findings and disposition

### Fixed: SSE delivery buffered by the HTTP server (high user impact)

`copyAndObserveLLM` used `io.Copy` directly to `http.ResponseWriter` without a flush. Small SSE chunks could remain in net/http's buffer until enough output accumulated or upstream closed. The old recorder-based tests saw Write calls immediately and therefore missed actual client latency.

A new regression uses a real `httptest.Server` and an upstream pipe that remains open until the client reads its first event. Before the fix it fails at the two-second context deadline; after the fix it succeeds. Both usage-observed and unobserved paths are covered. The proxy now flushes each SSE write using `http.ResponseController`; unsupported flush implementations retain their previous behavior, and actual write/flush errors terminate copying. Non-SSE responses retain their existing path. This fixes the common helper used by Go/Zen/Z.AI, so the regression surface includes other native SSE providers.

### Reduced: unnecessary SSE line-array allocation

The usage parser split the entire retained response into a slice of strings before parsing events. It now iterates lines using `strings.SplitSeq`, trimming a terminal CR per line. This preserves multiline event and CRLF handling without allocating the whole line-index array. Existing parser tests and the new proxy tests pass.

### Open limitation: large-stream usage capture and parsing cost

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

No production account was accessed, no secret printed, no billing/subscription changed and no service restarted by this audit. The running acceptance build remains the earlier `75343b541` until an explicit controlled deployment of these audit fixes; passing source tests is not proof they are deployed.

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
