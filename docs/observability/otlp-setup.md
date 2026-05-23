# OTLP tracing setup

Crewship emits OpenTelemetry GenAI spans for every LLM call via
`internal/telemetry`. Any OTLP-compatible backend (self-hosted or
managed) can receive those spans by setting two env vars on the
Crewship process — no code change required.

## Prerequisites

- An OTLP/HTTP-capable tracing backend reachable from the Crewship
  process.
- Credentials supplied by your backend (typical formats: a bearer
  token, a Basic-auth pair, or an API-key header).
- `OTEL_EXPORTER_OTLP_*` env vars supported by Crewship since the
  `internal/telemetry` package was added.

## Configure

Set the env vars on the Crewship process (`.env.local` for `dev.sh`,
systemd unit `Environment=` for prod, container `-e` for Docker):

```sh
# Base OTLP HTTP endpoint — SDK appends /v1/traces automatically.
# Same-host backends should be reached over loopback so traces don't
# round-trip through the reverse proxy (TLS overhead + extra hops
# for high-volume telemetry).
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318

# Optional headers. Standard OTel env-var format:
# comma-separated key=value pairs. Pick the form your backend expects.
OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer <token>
# or:
# OTEL_EXPORTER_OTLP_HEADERS=Authorization=Basic <base64(user:pass)>
# or:
# OTEL_EXPORTER_OTLP_HEADERS=X-API-Key=<value>
```

If your backend documents a project-scoped path (e.g.
`/api/public/otel`), append it to `OTEL_EXPORTER_OTLP_ENDPOINT` —
the OTel SDK still appends `/v1/traces` to whatever you set here.

Compute a Basic-auth header once if needed:

```sh
echo -n "user:pass" | base64 -w0
```

Restart `crewship`. The init logs one of:

- `OTel GenAI telemetry enabled  endpoint=http://...` → working
- `telemetry init failed, falling back to noop tracer` → check
  endpoint reachability and that header values parse (no quotes, no
  newline in base64).

## Smoke test

Verify endpoint + auth without waiting for an LLM call:

```sh
curl -X POST "$OTEL_EXPORTER_OTLP_ENDPOINT/v1/traces" \
  -H "Content-Type: application/json" \
  -H "Authorization: <as above>" \
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

A 2xx response confirms the endpoint accepts spans. Most backends
expose a Traces / Spans UI where the `crewship-smoke` service should
appear shortly after.

## What you get

Crewship's `internal/telemetry` wires GenAI Semantic Convention
attributes on every LLM call (defined in `internal/telemetry/spans.go`):

- `gen_ai.system` — `anthropic`, `openai`, `ollama`
- `gen_ai.request.model` — model identifier
- `gen_ai.usage.input_tokens` / `output_tokens` / `cached_input_tokens`
- `gen_ai.usage.cache_creation_tokens`
- `gen_ai.cost.total_usd`

Crewship-specific correlation keys, also from `spans.go`:

- `crewship.agent.id`, `crewship.agent.type`
- `crewship.crew.id`, `crewship.mission.id`
- `crewship.tool.name`, `crewship.tool.args_hash`,
  `crewship.tool.side_effect`

Routine-step traces add (from `spans_routine.go`):

- `crewship.routine.slug`, `crewship.routine.run_id`,
  `crewship.routine.pipeline_id`
- `crewship.routine.step.id`, `crewship.routine.step.type`,
  `crewship.routine.step.attempt`

Filter on these in your backend for per-provider, per-crew, or
per-mission views.

## Operational notes

- **Same-host loopback** avoids the reverse-proxy hop. If the backend
  is on a different host, point the endpoint at its public DNS —
  Crewship uses HTTPS automatically when the endpoint URL starts
  `https://`.
- **Batching** is 5 s / 2048 spans / 512 per batch (see
  `internal/telemetry/provider.go`). Spans emitted shortly before a
  forced shutdown may drop; the shutdown hook flushes whatever is
  still queued.
- **No-op fallback** — empty `OTEL_EXPORTER_OTLP_ENDPOINT` keeps the
  binary running without an observability stack, which is the local
  dev default.
- **Per-deployment isolation** — if your backend supports per-project
  credentials, give every deployment its own pair so dev/staging/prod
  traces stay sorted. Only `OTEL_EXPORTER_OTLP_HEADERS` needs to
  change.

## Troubleshooting

| Symptom | Likely cause | Fix |
|--------|--------------|-----|
| `telemetry init failed` | Endpoint unreachable | `curl $OTEL_EXPORTER_OTLP_ENDPOINT/v1/traces` to confirm |
| Smoke test 401/403 | Wrong credentials | Re-check the header value your backend expects (Bearer, Basic, API key) |
| Smoke test 2xx but no traces in UI | UI looking at wrong project / wrong filter | Verify the credentials map to the project shown in your backend |
| Traces appear with no `gen_ai.*` attrs | LLM call went through code path that bypasses `telemetry.LLMMiddleware` | Add `caller = telemetry.LLMMiddleware(caller)` to the new code path (see `internal/llm/middleware.go` for the pattern) |
| Spans drop on graceful shutdown | Shutdown hook not running | Confirm the binary exits via `Shutdown()` not SIGKILL; `telemetryShutdown` is called from `server_lifecycle.go:Shutdown` |
