# Fixed-operation broker: dev2 evidence

Issue #2714. This is a stacked follow-up to #2710; application authority and all
production dispatch remain #2711. No deployment, service reload, daemon restart
or merge was performed. The existing offline branch and dev1 were not edited.

## Exact source and scope

- Red-first contract/tests: `757d774e6`.
- Broker/relay implementation: `aea1c53b2`.
- Parent branch update: `b9de849f2` incorporates #2710 head `e82972ad1`.
- Final clean live-tested source: `e58a17bb3aec1ffaf2c20b4c5507513e4222cc6b`.
- `c62b25342` fixes the delegation test's child expiry to match its parent.
  `e58a17bb3` fixes positive-control fixtures: wait for neighbor readiness,
  parse/encode DNS with dnsmessage, bind the UDP responder to the tested gateway
  so responses have the correct source address, and exercise expired authority.
  These are fixture corrections, not fixes to observed broker authorization bugs.

The agent remains UID 1001 with network=none. UID 1002 runs the private loopback
operation broker; a private Docker-exec stream reaches the trusted host relay.
This is **bounded fixed-operation HTTPS**, not a general model/provider network.
No SSE, streaming, arbitrary URL/method/header proxy, CONNECT, automatic retry or
redirect, OAuth refresh, persistent RW mount or production entrypoint is offered.

## Reproduction and gates

```sh
go test -race ./internal/restrictedruntime -count=1
go vet ./...
go run ./scripts/agents-invariants
scripts/restricted-runtime-probe/run.sh -race
```

- Final real-Docker gate: **11/11 pass**, 60.139 seconds, host harness race enabled,
  zero skips. The log begins with the exact source SHA and `dirty=false`.
- Unit/race tests and whole-repository vet pass. All four checked invariants pass.
- Full Go suite: **148 tested packages pass**, exit 0 (`go test ./... -count=1 -p 2 -parallel 4 -timeout 25m`, owned tmpfs scratch). The run began at `9b76496b1`; the only subsequent source change through `e58a17bb3` is the separately verified build-tagged live test fixture. Remote CI/review are separate gates and remain pending at draft creation.
- Red-first tests failed on missing profile enforcement, public-to-local DNS
  rebinding and relay EOF without stop/remove. A later Go source-overlay mutation
  disabled the DNS address rejection and relay-stop action without editing the
  worktree; both negative gates failed again. Mutation failure is expected.

Raw evidence: [red-first](reports/restricted-http-broker-red-2026-09-28.txt),
[mutation](reports/restricted-http-broker-mutation-2026-09-28.txt),
[clean live run](reports/restricted-http-broker-live-2026-09-28.txt),
[full Go suite](reports/restricted-http-broker-go-2026-09-28.txt).

## What the tests actually establish

| Gate | Positive control | Denial/failure evidence |
|---|---|---|
| Fixed operation | Real TLS upstream receives exact POST/path and synthetic broker-held account credential | Wrong operation/token/account and oversized payload produce no unauthorized call; oversized response, redirect, SSE and post-response revocation release no response data |
| Two attempts | Two humans with the same agent each call their own broker successfully | Cross-attempt and previous-generation tokens do not increment upstream count |
| DNS rebinding | A synthetic routable DNS result is pinned to a real owned TLS endpoint through the private test dial adapter | Change that same hostname to loopback: no additional TLS request; unit cases include mapped IPv6, metadata/gateway, mixed answers and host interface IP |
| Direct network | Owned host IPv4/IPv6 listeners are reachable; actual synthetic DNS answers on the host gateway; service in B returns its own count | A's network=none cannot reach host IPv4/IPv6/gateway, DNS responder or B's service; only loopback interface exists |
| Revocation/expiry | A valid token first increments upstream count | Current revoked/expired authority denies another call and terminates the attempt; a fresh generation accepts only its new token |
| Relay failure | Agent completes an authorized call before the channel is closed | Real broker sees stdin EOF; manager stops/removes container and verifies absence; count remains unchanged |
| Credentials | Synthetic key reaches TLS upstream through host relay | The key is absent from agent env/files/proc; literal echoed key is redacted in unit response test |
| Prior offline guarantees | All eight original live tests run unchanged | Isolation, provenance/recovery, sidecar routes, outage, independent service and Manager SIGKILL expiry remain green |

The **private** test transport maps a validated routable address to an owned
loopback TLS server and installs its test CA. This makes tests reproducible
without real provider accounts or external internet. Production New does not
expose that override: it uses system DNS/interface addresses and normal TLS.
This is not a claim that a production provider adapter was exercised.

## Measurements

Warm-image connected starts, n=5, with the full Go suite also running on the host:
558.885, 559.845, 566.428, 672.199, 687.555 ms.
**p50 566.428 ms; p95 nearest-rank 687.555 ms.**

- One bounded TLS operation including test `docker exec` overhead: 86.933 ms.
- Relay EOF to confirmed container removal: 360.482 ms.
- Connected attempt cgroup memory current: 4,792,320 bytes; peak: 6,619,136 bytes.
- Original independent guard after Manager SIGKILL: 14,934.096 ms to stop.

These are observations of synthetic fixtures, not latency/expiry SLOs. Cgroup
memory excludes the trusted host relay, test upstream, Docker daemon and actual
model/agent workloads. The host kernel and Docker administrator remain trusted.

## Remaining release gates

No production Authority/BrokerAuthority/Catalog implementation, authenticated
HTTP/WS A2/B validation, provider/model streaming adapter, provider refresh or
upstream credential revocation, persistent disk quota, host reboot/failover or
rollout is included. Broker failures deny further use and stop the owned attempt;
no newly admitted upstream call follows observed revocation. Already-started
external effects cannot be undone, and literal redaction is not a classifier or
protection against encoded disclosure. Release 1.0 is not complete.
