# Langfuse + Crewship — Observability setup

Crewship already emits OpenTelemetry GenAI spans for every LLM call via
`internal/telemetry`. This guide wires those spans into Langfuse so they
show up in the trace explorer + cost dashboards.

## Prerequisites

- Langfuse running and reachable from the Crewship process.
- A Langfuse project + its `pk-lf-*` / `sk-lf-*` key pair.
- `OTEL_EXPORTER_OTLP_*` env vars supported by Crewship since the
  `internal/telemetry` package was added; no Crewship code change
  needed to switch a deployment to Langfuse.

## Configure

Set two env vars on the Crewship process (`.env.local` for `dev.sh`,
systemd unit `Environment=` for prod, container `-e` for Docker):

```sh
# Base OTLP HTTP endpoint — SDK appends /v1/traces automatically.
# Same-host deployments should hit Langfuse via loopback so traces
# don't round-trip through the reverse proxy (TLS overhead +
# extra hops for high-volume telemetry).
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:3000/api/public/otel

# Auth header. Langfuse expects HTTP Basic with pk-lf:sk-lf base64'd.
# Standard OTel env-var format: comma-separated key=value pairs.
OTEL_EXPORTER_OTLP_HEADERS=Authorization=Basic <base64(pk:sk)>
```

Compute the auth value once:

```sh
echo -n "pk-lf-xxx:sk-lf-yyy" | base64 -w0
```

Restart `crewship`. The init logs one of:

- `OTel GenAI telemetry enabled  endpoint=http://...` → working
- `telemetry init failed, falling back to noop tracer` → check endpoint
  reachability and that the header value parses (no quotes, no
  newline in the base64).

## Smoke test

Verify the endpoint + auth without waiting for an LLM call:

```sh
curl -X POST "$OTEL_EXPORTER_OTLP_ENDPOINT/v1/traces" \
  -H "Content-Type: application/json" \
  -H "Authorization: Basic <base64>" \
  --data-binary '{"resourceSpans":[{"resource":{"attributes":[
    {"key":"service.name","value":{"stringValue":"crewship-smoke"}}]},
    "scopeSpans":[{"scope":{"name":"smoke"},"spans":[{
      "traceId":"5b8efff798038103d269b633813fc60c",
      "spanId":"eee19b7ec3c1b173",
      "name":"smoke-handshake","kind":1,
      "startTimeUnixNano":"'"$(date +%s)"'000000000",
      "endTimeUnixNano":"'"$(date +%s)"'000000000",
      "attributes":[{"key":"gen_ai.system","value":{"stringValue":"smoke"}}]
    }]}]}]}'
```

Expect HTTP 200 + a JSON envelope with `name:"otel-ingestion-job"`.

## What you get

Crewship's `internal/telemetry` already wires GenAI Semantic Convention
attributes on every LLM call:

- `gen_ai.system` — `anthropic`, `openai`, `ollama`
- `gen_ai.request.model` — model identifier
- `gen_ai.usage.input_tokens` / `output_tokens` / `cached_input_tokens`
- `gen_ai.cost.total_usd`
- `crewship.workspace_id`, `crewship.crew_id`, `crewship.agent_id`

In Langfuse the same trace surfaces under Traces → filter by
`gen_ai.system` for per-provider views, by `crewship.crew_id` for
per-crew cost breakdowns.

## Operational notes

- **Same-host loopback** avoids the reverse-proxy hop. If Langfuse is
  on a different host, point the endpoint at the public DNS — Crewship
  uses HTTPS automatically when the endpoint URL starts `https://`.
- **Batching** is 5 s / 2048 spans / 512 per batch (see
  `internal/telemetry/provider.go`). Spans emitted shortly before a
  forced shutdown may drop; the shutdown hook flushes whatever is
  still queued.
- **No-op fallback** — empty `OTEL_EXPORTER_OTLP_ENDPOINT` keeps the
  binary running without an observability stack, which is the local
  dev default.
- **Per-deployment isolation** — give every deployment its own Langfuse
  project (separate `pk-lf-`) so dev/staging/prod traces stay sorted.
  The OTel headers env is the only thing that needs to change.

## Troubleshooting

| Symptom | Likely cause | Fix |
|--------|--------------|-----|
| `telemetry init failed` | Endpoint unreachable | `curl $OTEL_EXPORTER_OTLP_ENDPOINT/v1/traces` to confirm |
| Smoke test 401 | Wrong base64 / wrong key pair | Re-compute with `echo -n "pk:sk" \| base64 -w0` |
| Smoke test 200 but no traces in UI | UI looking at wrong project | Verify `pk-lf-*` matches the project shown in Langfuse |
| Traces appear with no `gen_ai.*` attrs | LLM call went through code path that bypasses `telemetry.LLMMiddleware` | Add `caller = telemetry.LLMMiddleware(caller)` to the new code path (see `internal/llm/middleware.go` for the pattern) |
| Spans drop on graceful shutdown | Shutdown hook not running | Confirm the binary exits via `Shutdown()` not SIGKILL; `telemetryShutdown` is called from `server_lifecycle.go:Shutdown` |
