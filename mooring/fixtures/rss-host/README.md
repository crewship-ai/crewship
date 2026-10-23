# Connected participant memory fixture

This explicitly tagged test tool connects a separate load-generator process over
real loopback TCP sockets to the Go host supervisor and the separate Rust
`rss_fixture` example through its inherited private Unix RPC channel. Ordinary
Go builds contain only an inert empty command; the fixture requires its tag. It never creates a Crewship route, database, durable
receipt or deployed service.

The Rust example uses a fixed synthetic read-only ticket, synthetic owner fence,
and immediate mock delivery grant. This intentionally bypasses real host
admission, checkpoint integrity and durable application semantics. It measures
actual generic yrs documents, per-participant bounded queues and delivery gates,
Go connection handling and real sockets; it does not establish production
permission, revocation or browser collaboration correctness. Buffered kernel
bytes and revocation barriers are not validated by this fixture.

Input is a generic paragraph projection, independent of any application model:

```json
{"paragraphs":[{"id":"paragraph-1","text":"Hello 🦀","metadata":{},"runs":[{"start":0,"length":5,"attrs":{"bold":true}}]}]}
```

Run offsets are UTF-16 code units. Importers must convert their original offsets
at valid Unicode boundaries. Metadata is retained in a separate shared map;
paragraph IDs, shared text and order are stored in yrs maps/arrays. This is a
measurement projection, not a complete application serialization or production
adapter. Use synthetic or locally authorized input; do not commit captured
application documents or measurement reports.

From a checkout containing both the Go supervisor and Rust fixture:

```sh
crewship-heavy cargo build --manifest-path mooring/Cargo.toml --config mooring/.cargo/config.toml --locked --offline -p mooring-collab --example rss_fixture --release
crewship-heavy go build -tags mooring_rss_fixture -o /tmp/mooring-rss-host ./mooring/fixtures/rss-host
crewship-heavy /tmp/mooring-rss-host -binary "$PWD/mooring/target/release/examples/rss_fixture" -source /tmp/generic-paragraphs.json -documents 1
```

The tool binds only `127.0.0.1:0`, admits at most 100 participants and loads at
most 100 document copies. It reports host/Rust RSS before/after document loading
and for 1, 10 and 100 settled connections, after three full read cycles, and
after disconnect processing. Each reported phase waits 200 ms; this is a bounded
settling window rather than a full steady-state or backpressure experiment. The
separate load-generator RSS is excluded.
Repeat with `-documents 100` to distinguish shared-document overhead from
per-document state. The document projection's SHA256 is included for comparison.

RSS has page/allocator granularity and Go garbage collection changes marginal
values. Zero or negative differences do not mean zero participant memory, and
retained RSS after disconnect does not by itself prove live participant leaks.
Compare absolute values and independent repeated runs; report mock authority and
projection limits with every result. Queues drain immediately through serialized
host calls: their four-frame/256 KiB caps are limits, not preallocated payloads
or worst-case backpressure memory. No fixed bytes-per-participant estimate or
production capacity guarantee follows from this harness.
