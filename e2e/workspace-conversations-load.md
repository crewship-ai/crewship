# Workspace conversations: 100 active humans

This opt-in acceptance test starts a real loopback HTTP server against a private,
fully migrated SQLite file. `testutil.MigratedSQLDB` uses production
`database.Open`: WAL, foreign keys, busy timeout and a five-connection pool. The
test asserts WAL and the pool size before proceeding. No live workspace, Dev2
server, agent runtime or model is used. Temporary databases are cleaned up.

Run from the repository root:

```bash
CREWSHIP_CHAT_LOAD_REPORT=/tmp/crewship-chat-http-100-active.json \
go test ./internal/api \
  -run '^TestWorkspaceConversationsHTTP100Active$' \
  -count=1 -v -timeout=5m
```

This acceptance test runs in the normal Go suite as well. The report path is
optional; JSON is always printed in the test log. Its directory must already
exist. The report contains timings and failure summaries, never session tokens.
Do not set `TMPDIR` to a RAM filesystem if comparing disk-backed SQLite runs;
record such a change if deliberately testing one.

## What it proves

- 100 separately authenticated humans, each with a real session row and issued
  access token, use production `RequireAuth`, `RequireWorkspace` and conversation
  handlers over TCP HTTP. There is no injected user/workspace request context.
- One shared channel has 100 humans; ten private groups have ten humans each.
  Missing credentials, cross-workspace requests and a same-workspace nonmember
  are rejected by the real middleware/handlers.
- A barrier releases all 100 workers. Each sends two messages to the channel and
  two to their group, then retries each identical author-scoped client ID:
  400 committed messages, 400 successful identical retries, stable IDs, authors
  and sequences, and no duplicate messages.
- The real outbox dispatcher projects inbox updates concurrently with sending.
  The test requests a drain every 10 ms instead of the production worker’s
  one-second polling interval to keep acceptance short; this is not a live
  notification-latency measurement.
  Before readers advance cursors, there are exactly 200 per-user/conversation
  inbox aggregates. Projection deliberately has no WebSocket hub.
- All 100 humans paginate their full channel and group histories (900 requests),
  verify continuous sequence numbers and unique IDs, then mark both rooms read.
  Stale read requests cannot move cursors backward.
- Replaying all outbox events simulates a crash between projection and
  acknowledgment. Already-read inbox activity does not reappear. Human messages
  create zero agent jobs.

## Interpretation

The JSON reports p50/p95/p99/max client-observed HTTP latency separately for
sends, identical retries, history pages and read operations. `outbox_batch`
measures the final explicit drain/replay batches; it is neither per-message
notification latency nor every concurrent background drain. Elapsed time also
includes fixture/migration/session preparation and the read/replay phases.

The send/retry phase and the history/read phase each release 100 workers, but
run sequentially. History reads do not overlap the sending phase; outbox
projection does. This is not sustained mixed read/write traffic.

This is a short burst acceptance scenario, not a sustained-load benchmark or a
corporate capacity guarantee. It excludes the browser, WebSocket fanout,
TLS/reverse proxy, full-router rate limiting/capability layers, other Crewship
workloads, model/container execution, backup and failover behavior. Run those
separately before making a production SLA. A passing result is evidence about
this authenticated human-message path on this host, not a promise about every
100-user deployment.

## Recorded run

On 2026-09-07, the first run on this development host (Go 1.27.0, 12 reported
logical CPUs) passed in 5.28 seconds with zero failures. Send latency was
p50 58.6 ms, p95 170.4 ms, p99 221.1 ms and maximum 421.2 ms; history-page p95
was 64.8 ms. The test completed all 400 unique sends and 400 identical retries.
The run's JSON artifact was written to
`/tmp/crewship-chat-http-100-active-2026-09-07.json`. These are observations of
one isolated run, not enforced performance thresholds.
