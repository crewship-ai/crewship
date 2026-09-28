# Fixed-operation HTTP broker prototype

Issue #2714, stacked follow-up to #2710. Application authority and
all production entrypoints remain #2711. This work does not deploy or merge.

`brokered-http-v1` supports fixed HTTPS GET/POST operations with bounded bodies.
It is not general provider/model networking: SSE, WebSocket, CONNECT, arbitrary
URLs/headers, automatic redirects/retries, refresh and persistent RW storage
are unsupported. It does not complete Release 1.0.

## Authority boundary

The exact proposed/approved Go types are in `internal/restrictedruntime/broker.go`.
`Plan.Profile` selects the profile; empty preserves the existing offline mode.
`Plan.Network` has a version, attempt-bound audience, exact operation grants,
and broker-only credential references with revision/provider/account binding.
`BrokerAuthority` extends the existing authority with current credential
resolution. It may not accept authority from an agent request. All network fields
participate in the plan fingerprint and provenance; operation and credential grants also constrain delegation narrowing.
`Audience` is a server-owned per-attempt broker tag, not an A2/B data/output grant: a delegated child may receive its own tag and token, but changing the tag within an admitted attempt changes its fingerprint and denies further upstream admission and response delivery.

Agent UID 1001 remains in network=none. A protected UID 1002 loopback broker
uses a private framed Docker-exec stdin/stdout connection to the host relay.
Only the relay dials HTTPS upstreams. An agent calls a fixed local operation ID
with a runtime-generated attempt token and body; it cannot supply a destination,
method, credential/account or upstream headers. No shared host firewall changes.

At most 16 operations and 16 broker credentials; one active call per attempt;
1 MiB maximum request/response; 10 second maximum upstream deadline. Responses
are buffered and reauthorized before delivery. No partial oversized response.
Host DNS resolution rejects any disallowed address in an answer plus connected host
interface subnets; dial uses the validated IP, TLS uses the original name.
Every new call resolves DNS anew. No redirects, environment proxies or retries.

## Revocation and lifecycle

Authorize each operation and exact credential binding anew. No NEW upstream call
is admitted after observed revocation. An already-started external effect cannot
be undone. Recheck before delivering a response; cancel in-flight work and stop
the attempt after detected revocation. Existing 5 second renewal / maximum
15 second lease and independent UID 1002 guard remain. Unknown profile/fields,
authority failure, broken relay or broker startup failure deny and clean up;
never fall back to shared execution. Retry is a new attempt/generation/token,
new credential resolution and DNS validation. Broker readiness precedes launch.

Connected v1 rejects persistent RW mounts. Hard byte/inode quotas and backend
attestations require their own host contract and PR; metadata alone is no quota.
Existing bounded tmpfs and approved RO snapshots remain available.

## Red-first checkpoint

Before implementation, `go test ./internal/restrictedruntime -run TestBroker
-count=1` fails for unsupported authority, DNS changing from routable to local
(including IPv4-mapped IPv6), and relay EOF leaving the attempt running.
The initial commit contains intentional enforcement stubs and is not mergeable.
Later commits must replace them and pass the complete verification loop.
