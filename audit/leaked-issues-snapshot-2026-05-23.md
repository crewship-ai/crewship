# Leaked Public Issues — Local Snapshot (deleted from GitHub 2026-05-23)

These were public issues on the crewship-ai/crewship repo that referenced internal infrastructure (crewship-dev1.unifylab.cz, /opt/crewship_N, audit-stack paths, dev1/2/3 slot names). User asked to delete from GitHub to stop the leak; bodies preserved here for follow-up bug-fix work.

Each section is the verbatim issue body as it existed before deletion.


---

## Issue #554

**Title:** CLI: unknown subcommand silently dispatches to 'ask' instead of erroring

**Labels:** bug, from-audit-2026-05-23

**Created:** 2026-05-23T12:03:10Z  **Author:** Srbino

---

## Summary

Typing any non-existent subcommand on `crewship` doesn't error — it falls through to `ask` (with the bare command name as the prompt) and exits 1 with a misleading `\"no default agent set\"` message.

The user thinks they typed a wrong command. The CLI thinks they want to ask the default agent the word `status` (or `definitely-not-a-real-subcommand`).

## Repro (against the built binary on dev2 — `/opt/crewship_2/crewship`, `v0.1.0-beta.4-155-gc7ee739d`)

```
$ crewship status
no default agent set. Use --agent <slug>, --agents <list>, or run 'crewship config set default-agent <slug>'
$ echo $?
1

$ crewship definitely-not-a-real-subcommand
no default agent set. Use --agent <slug>, --agents <list>, or run 'crewship config set default-agent <slug>'
$ echo $?
1
```

No `status` subcommand is registered (it isn't in `crewship --help`). Same for the gibberish second case. Both reach the `ask` command's \"need default agent\" error path.

## Why this matters

1. **Footgun for users typoing common commands.** `status`, `ls`, `start`, `up`, `show`, `version` are all plausible typos a user could make — and instead of \"unknown command\", they get a message that talks about agent slugs they have nothing to do with.
2. **If a default agent IS set**, the typo silently fires a real LLM run against the agent with the typo as the prompt — a small cost charge for what was supposed to be a no-op.
3. **Scripting / CI**: shell scripts that pipeline `crewship` won't fail on typos in any way that distinguishes \"unknown command\" from \"command failed\".

## Likely cause

Looks like Cobra root has `Args: cobra.ArbitraryArgs` and a `Run` that forwards positional args to the `ask` command. Cobra's default behavior of \"unknown command → error before falling through\" was overridden somewhere to make `crewship \"my question\"` work without typing `ask` explicitly — which is a nice ergonomic, but it shouldn't override known-subcommand-shape inputs.

## Suggested fix

In root command's Run:
- If `args[0]` is a registered subcommand name → never reached anyway (Cobra resolves it).
- If `args[0]` looks like a subcommand (alphanumeric-only, no whitespace, no English question-shape) → return `errors.New(\"unknown command '\" + args[0] + \"'; try 'crewship --help'\")`.
- Otherwise → keep the current ask-fallback.

A more conservative version: require `ask` to be explicit when the first arg looks like a single bare word matching `^[a-z][a-z0-9-]*$` (the slug shape of every registered subcommand).

## Repro artifact

`~/audit-stack/iterations/2026-05-23--dev2-deep/cli/nonexistent.err` and `cli/status.err` on the dev2 VM (audit run on 2026-05-23).


---

## Issue #555

**Title:** CLI: 'now' and 'me' exit 0 even when every backend call returns 401 — breaks scripting/CI

**Labels:** bug, from-audit-2026-05-23

**Created:** 2026-05-23T12:03:30Z  **Author:** Srbino

---

## Summary

`crewship now` and `crewship me` aggregate several API calls and render a dashboard. When the CLI token is invalid / expired, every underlying call returns 401 — but both commands print error lines to stderr and **still exit 0**. A script wrapping these commands can't tell that the session is dead from the exit code alone.

## Repro (dev2 build `v0.1.0-beta.4-155-gc7ee739d`)

```
$ crewship now
Now  11:57:08 UTC

Running missions/runs: 0

Agents: 0 idle, 0 busy

Pending approvals: 0

$ crewship now 2>&1 1>/dev/null
[partial] approvals: API error (401): session_invalid
[partial] runs: API error (401): session_invalid
[partial] agents: API error (401): session_invalid

$ crewship now; echo \"exit=\$?\"
... (same as above)
exit=0
```

Same shape for `crewship me`:

```
[partial] runs: API error (401): session_invalid
[partial] missions: API error (401): session_invalid
[partial] approvals: API error (401): session_invalid
$ echo \$?
0
```

The dashboard then displays `0 running / 0 agents / 0 approvals` — **identical to a healthy quiet state**. A user (or a cron / status-bar widget) glances at it and assumes \"all clear\" when in fact they've been silently logged out.

## Why this matters

1. **Looks healthy when it isn't.** The empty dashboard is indistinguishable from \"actually quiet\" — the user has no signal to re-auth.
2. **Status widgets / shell prompts that pipe `crewship now` or `crewship me`** can't gate on `\$?` to detect dead sessions; they have to scrape stderr for `session_invalid`, which is fragile.
3. **Other `[partial]`-style aggregators in the CLI** likely have the same shape (`history`, `cost`, `audit` …) — worth a sweep.

## Suggested fix

Two options, pick one:

- **(A) All-401 → exit non-zero.** If *every* aggregated sub-call returned the same 401 (`session_invalid`), the whole command should exit 1 (or a dedicated code like 4 for \"unauthenticated\") and the rendered dashboard should be replaced by a one-line \"session expired; re-run `crewship login`\" message. This is what `gh`, `kubectl`, `aws` all do.
- **(B) Any-401 → still render, but exit 2.** Keep the partial render so the user sees what *did* work, but signal failure via the exit code. This matches the existing `[partial]` warning shape.

Option A is cleaner UX; option B is the lower-risk change.

## Repro artifact

`~/audit-stack/iterations/2026-05-23--dev2-deep/cli/now.{out,err}` and `cli/me.{out,err}` on the dev2 VM.


---

## Issue #556

**Title:** build provenance lost in dev.sh hot-swap build — `crewship version` reports commit:none

**Labels:** from-audit-2026-05-23, dev3, perf

**Created:** 2026-05-23T12:05:00Z  **Author:** Srbino

---

## Problem

The dev hot-swap build path (`dev.sh` line 283) strips all build-time
provenance from the resulting binary. Operators running `crewship version`
against the deployed binary get back:

```
crewship dev
  commit:  none
  built:   unknown
  go:      go1.26.0
  os/arch: linux/amd64
```

— so they cannot identify which source commit is actually running.

`crewship --version` is also missing entirely (only `crewship version` works);
the standard flag returns `Error: unknown flag: --version`.

## Repro on dev3

```
$ ssh crewship-dev
$ /tmp/crewship-3-dev version
crewship dev                     # ← should be v0.1.0-beta.4-NNN-gXXXXXXXX
  commit:  none                  # ← should be the source commit
  built:   unknown               # ← should be the build timestamp
  ...

$ /opt/crewship_3/crewship version   # canonical (built via `make build`)
crewship v0.1.0-beta.4-155-gc7ee739d
  commit:  c7ee739d
  built:   2026-05-23T11:23:52Z
  ...
```

The two binaries on the same VM differ by **15 MB** (98 MB vs 83 MB)
because dev.sh's invocation also omits `-trimpath` and `-s -w`.

## Root cause

`dev.sh:283`:

```bash
go build -o "$binary" ./cmd/crewship
```

vs. `Makefile:20`:

```makefile
GO_BUILD = go build -trimpath $(LDFLAGS)

LDFLAGS = -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE) -X github.com/crewship-ai/crewship/internal/license.publicKey=$(LICENSE_PUBKEY) -X github.com/crewship-ai/crewship/internal/crashreport.DSN=$(SENTRY_DSN)"
```

`dev.sh` doesn't import these LDFLAGS — by design probably (faster
iteration during `dev:go` reload), but the consequence is that the
hot-swapped binary at `/tmp/crewship-N-dev` (which is what actually
runs after `apply-file`) is build-info-blind.

## Impact

- **Incident response**: when an operator finds a misbehaving prod
  binary, they can't tie it to a commit / PR / change-window. Their
  only option is sha256 + `find` across builders.
- **Crash reports**: `internal/crashreport.DSN` is also omitted →
  Sentry events from a dev-build binary land with no version tag.
- **Operator confusion**: `crewship version` reporting "commit: none"
  looks like a bug in `crewship version` itself, not in the build
  script.

## Suggested fix

In `dev.sh:283`, mirror the Makefile pattern:

```bash
VERSION=$(git -C "$PROJECT_DIR" describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT=$(git -C "$PROJECT_DIR" rev-parse --short HEAD 2>/dev/null || echo none)
DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS="-X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE"
( cd "$PROJECT_DIR" && go build -trimpath -ldflags "$LDFLAGS" -o "$binary" ./cmd/crewship )
```

Skip `-s -w` for dev builds if symbol-strip slows iteration noticeably;
the version/commit/date flags alone are <1 KB.

Separately: register `--version` as an alias for the `version` subcommand
in `cmd/crewship/main.go` (one-liner with cobra's
`cmd.Flags().BoolP("version", "v", false, "")` + dispatch).

## Out of scope

Not a runtime/perf bug; reporting per `/loop` directive to log
operator-impact issues found while testing other use cases.



---

## Issue #557

**Title:** OTel HTTP server middleware not wired — Tempo has zero API spans

**Labels:** from-audit-2026-05-23, dev3, perf

**Created:** 2026-05-23T12:05:02Z  **Author:** Srbino

---

## Problem

Crewship's HTTP server is started in `internal/server/server_lifecycle.go:53` via
`s.httpServer.ListenAndServe()` — the mux/handler is **never wrapped with
`otelhttp.NewHandler`**, so no spans are emitted for incoming API requests.

The OTel dependency is present but as an indirect-only entry:

```
go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.65.0 // indirect
```

A repo-wide grep for `otelhttp.NewHandler`, `otelmux`, or `otelchi` returns
zero hits. The only spans actually produced by Crewship are GenAI/agent
internal spans (`agent.invoke`, `tool.execute`, `llm.call`, `routine.run`,
`routine.step` in `internal/telemetry/spans.go`).

## Repro on dev3

1. Process running: `/tmp/crewship-3-dev start` (commit `b640d3f6`)
2. OTel exporter env present: `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4319`
3. Drove ~32k HTTP requests against `/healthz`, `/api/v1/runs`, `/api/v1/crews`,
   `/api/v1/skills`, `/` over ~5 min (k6, 30 VUs).
4. Queried Tempo for the same window:

   ```bash
   curl 'http://127.0.0.1:3200/api/search?tags=service.name%3Dcrewshipd&start=…&end=…'
   # → {"traces":[], …}
   ```

The only `service.name=crewshipd` traces Tempo retains are **outbound Docker
daemon calls** Crewship's own HTTP client makes (`GET /v1.51/containers/json`
etc.) — those go through a Docker SDK that uses `otelhttp` transparently.

## Impact

- p95/p99 latency-by-endpoint dashboards in Grafana can never populate.
- Trace-based debugging of API regressions is impossible — a slow
  `/api/v1/runs` request leaves no breadcrumb.
- The Tempo footprint is dominated by spurious Docker-client noise, which
  inflates blocks/index without operator value.

## Suggested fix

Single-line wrap at server construction:

```go
s.httpServer.Handler = otelhttp.NewHandler(mux, "crewship.http")
```

Optionally, configure `otelhttp.WithFilter` to drop `/healthz` and
`/debug/*` from spans to keep volume sane.

## Out of scope

This is independent of PR #552 (which adds opt-in pprof + Langfuse setup)
— that PR doesn't touch HTTP-server tracing. The two are complementary.



---

## Issue #558

**Title:** Tempo trace resource attrs missing `slot` label — can't filter per dev slot

**Labels:** from-audit-2026-05-23, dev3, perf

**Created:** 2026-05-23T12:05:04Z  **Author:** Srbino

---

## Problem

Pyroscope labels every profile with `slot=dev1|dev2|dev3` (via the
push-agent label set), so operators can filter profiles per slot.

Tempo does **not** receive the same `slot` resource attribute on traces.
Listing all known resource-attribute tag values on the running Tempo
returns empty:

```bash
curl http://127.0.0.1:3200/api/search/tag/slot/values
# → {"tagValues":[],"metrics":{}}
```

And searches by `slot=devN` return zero traces, even when the same
process is actively producing other traces.

## Why it matters

The Proxmox dev box runs three crewship slots (dev1/dev2/dev3) against
the same Tempo. Without `slot` on trace resource attrs, a Grafana
dashboard cannot show "all errors on dev3 in the last hour" — it
returns traces from all three slots, indistinguishable.

This will get worse on prod when multiple production tenants share an
observability backend.

## Suggested fix

When the OTel TracerProvider is initialised in `internal/telemetry`,
include `slot` in the `resource.Resource`:

```go
res, _ := resource.New(ctx,
    resource.WithAttributes(
        semconv.ServiceName("crewshipd"),
        attribute.String("slot", os.Getenv("CREWSHIP_SLOT")),  // dev1, dev2, dev3, prod
    ),
)
```

The push-agent already reads `CREWSHIP_SLOT` (visible in the Pyroscope
label set) — re-use the same env var.

## Out of scope

Reporting this independently of #552 (pprof) and the otelhttp-wrap
issue — those address different observability gaps.



---

## Issue #560

**Title:** security: remove CSP 'unsafe-eval' from prod build (followup from PR #551)

**Labels:** security

**Created:** 2026-05-23T12:08:13Z  **Author:** Srbino

---

**Tracking ticket for the deferred CSP item from PR #551.**

## Context

The Content-Security-Policy header on SPA routes currently includes
`script-src 'self' 'unsafe-inline' 'unsafe-eval'` (see
`internal/server/security_headers.go:79`).

`'unsafe-eval'` permits `eval()` and `new Function()` at runtime,
which materially weakens the CSP. Removing it is one of the highest-
leverage hardening steps for the SPA surface.

It's still in the policy because Next.js's inline boot script + some
chart / MDX runtime currently rely on it. Removing without testing
will likely break the dashboard.

## Reproduction (audit detection)

ZAP baseline flags it on every run as `CSP: script-src unsafe-eval
[10055] x 9` — the count of 9 = once per page that ships HTML
(/ + /robots.txt + /sitemap.xml + each crawled route).

## Plan

1. Identify which Next.js / library actually calls `eval` —
   browser DevTools Console will throw on first violation when
   `'unsafe-eval'` is dropped.
2. Common culprits in 2025-2026 Next.js: legacy MDX runtime, some
   chart libraries (recharts older versions), some lottie players.
3. Either swap the library to a non-eval variant or carve out a
   per-route relaxed CSP (better: nonce-based CSP for the affected
   route only).
4. Verify in DevTools Network tab that the CSP header dropped
   'unsafe-eval' AND no SPA functionality broke.

## Acceptance

- ZAP baseline against any dev slot returns `WARN-NEW: CSP: script-src
  unsafe-eval` = 0
- Dashboard UI still loads on https://crewship-dev1.unifylab.cz/
  in Chrome stable

## Why now

EU CRA enforcement (2026-2027) makes documentable CSP hardening
part of the supply-chain story. Removing `unsafe-eval` is the
canonical move OWASP recommends for ASVS Level 1.

## Raw output

`~/audit-stack/iterations/2026-05-23--release-battery/P2-dev1-zap-baseline/stdout.log`
on `crewship-dev` VM.


---

## Issue #561

**Title:** `crewship completion bash` panics — flag shorthand `-f` redefined on `create`

**Labels:** from-audit-2026-05-23, dev3

**Created:** 2026-05-23T12:15:51Z  **Author:** Srbino

---

## Problem

`crewship completion bash` panics on startup with a cobra flag-spec
collision. This breaks shell-completion installation for any user
following the standard cobra-generated bash flow.

## Repro on dev3

```
$ /tmp/crewship-3-dev completion bash
panic: unable to redefine 'f' shorthand in "create" flagset: it's already used for "file" flag

goroutine 1 [running]:
...
```

Exit code is non-zero; no stdout produced, so the typical
`crewship completion bash > /etc/bash_completion.d/crewship`
ends up writing an empty file.

## Likely cause

Two flags on the `create` subcommand both register `-f` as their
shorthand — most likely the global `--format -f` (visible on every
subcommand) collides with a `--file -f` registered on a child
command's `create` action (e.g. `crewship credential create -f
manifest.yaml`, `crewship crew create -f spec.yaml`, etc).

Cobra's completion generator walks every command and re-binds its
flagset, which is where the collision surfaces. Normal invocation
of `crewship credential create -f ...` doesn't necessarily trip it
because flags are bound lazily per-subtree.

## Suggested fix

Repo-wide grep for `ShorthandLookup\("f"\)` and any
`*.Flags().*BoolP\(..., "f", ...\)|*.Flags().*StringP\(..., "f", ...\)`
on `create` subcommands. Either:

1. Rename the offending shorthand (`-F` or no shorthand) on the
   colliding `create` flag, OR
2. Drop the global `-f` shorthand for `--format` (less ergonomic
   but resolves all latent collisions across the whole tree).

## Impact

- Shell completion install fails silently for every operator.
- Empty completion file masks the bug — users blame their shell,
  not Crewship.

## Out of scope

Reporting per `/loop` directive while testing CLI surfaces. Separate
from #556 (build provenance), but same audit run.



---

## Issue #562

**Title:** api: no public OpenAPI spec — blocks automated API testing + integration tooling

**Labels:** documentation

**Created:** 2026-05-23T12:28:24Z  **Author:** Srbino

---

**Detected by:** release-battery loop iter #2 (OpenAPI drift detection)
**Phase:** Release-readiness check
**Severity:** MEDIUM (test surface gap, integration friction)
**Repo SHA:** b640d3f6

## Finding

Crewship exposes **399 HTTP route handlers** (353 public `/api/v1/*` +
46 internal `/api/v1/internal/*`, registered in
`internal/api/router*.go`), but **zero of them are described in an
OpenAPI / Swagger spec** served by the binary.

Every probe path returns either the SPA fallback or 404:

| Path | Status | Content |
|------|-------:|---------|
| `/openapi.json` | 200 | HTML (SPA fallback) |
| `/openapi.yaml` | 200 | HTML (SPA fallback) |
| `/swagger.json` | 200 | HTML (SPA fallback) |
| `/docs` | 200 | HTML (SPA fallback) |
| `/api/openapi.json` | 404 | "404 page not found" |
| `/api/v1/openapi.json` | 404 | "404 page not found" |
| `/api/v1/spec` | 404 | "404 page not found" |
| `/api/docs` | 404 | "404 page not found" |

## Why this matters

**1. Blocks automated API testing.** Property-based fuzzers
(Schemathesis, RESTler, Dredd) all need OpenAPI as input. Our
release battery's Phase 2 Schemathesis run dies immediately because
the spec URL returns HTML, not JSON.

**2. Blocks SDK + client codegen.** Anyone integrating with
Crewship's API has to read the Go source. No openapi-generator-cli,
no auto-generated TypeScript types for the UI, no Postman
collection import.

**3. Hides surface from external auditors.** When a security review
asks "what endpoints does this app expose", the answer is "grep
the Go source."

**4. SPA fallback on doc-shaped paths is itself a bug.** PR #551
hardened dotfile / backup paths to 404; `openapi.json`,
`swagger.json`, `docs` should join that list either as 404 OR by
serving the real spec. Currently they 200 with HTML, which scanners
read as "endpoint exists, just returns garbage" — worse than a
clean 404.

## Reproduction

```bash
curl -s https://crewship-dev1.unifylab.cz/openapi.json | head -c 50
# <!DOCTYPE html><html lang="en">    ← SPA, NOT JSON

grep -hE 'mux\.Handle.*"[A-Z]+ /' internal/api/router*.go | wc -l
# 399
```

## Suggested fix

**(a) Annotation-driven generation (lowest friction)** — add
`swaggo/swag` comments to handler funcs and `swag init` in CI.
Outputs `/swagger/doc.json`. No handler rewrite needed.

**(b) Framework swap to huma / fuego** — these generate the spec
from typed handler signatures. Spec guaranteed to match code,
but means rewriting all 399 handlers.

**(c) Hand-written spec with codegen verification** — write
`api/openapi.yaml` manually, CI test asserts every
`r.mux.Handle(...)` has a matching path entry.

Recommend **(a)** for fastest unblocking,
**(c)** as long-term posture.

## Followup work this unlocks

- Phase 2 Schemathesis becomes meaningful (property-based fuzz)
- RESTler stateful chain-fuzz on `/api/v1/internal/keeper/*`
- Garak / PyRIT wired against documented chat endpoint
- SDK codegen for the UI to drop hand-written client code

## Raw data

Reproducible from any checkout at `b640d3f6`; 399 handler grep +
8 doc-path probes against any dev slot.


---

## Issue #563

**Title:** IPC `GET /agents/{id}/status` masks all error paths as `{status: idle}`

**Labels:** from-audit-2026-05-23, dev3

**Created:** 2026-05-23T12:45:07Z  **Author:** Srbino

---

## Problem

`GET /agents/{id}/status` on the IPC Unix socket (per-slot e.g.
`/tmp/crewship-3.sock`) returns `200 {"agent_id":"<input>","status":"idle"}`
**for any string** — including IDs that don't exist, malformed IDs, and
strings that aren't UUIDs.

The same handler also masks legitimate state-store failures as `idle`,
so callers cannot distinguish:

1. Agent exists and is idle (legitimate "idle")
2. Agent does not exist (should be 404)
3. State store query failed (should be 5xx — silent bug)
4. State store returned garbage data (should be 5xx — silent bug)
5. State store not configured (should be 503 — config bug)

## Repro on dev3

```
$ curl --unix-socket /tmp/crewship-3.sock http://localhost/agents/foo/status
{"agent_id":"foo","status":"idle"}

$ curl --unix-socket /tmp/crewship-3.sock http://localhost/agents/'<script>alert(1)</script>'/status
{"agent_id":"<script>alert(1)</script>","status":"idle"}

$ curl --unix-socket /tmp/crewship-3.sock http://localhost/agents/00000000-0000-0000-0000-000000000000/status
{"agent_id":"00000000-0000-0000-0000-000000000000","status":"idle"}
```

The companion endpoint `POST /agents/{id}/start` returns 500 for any of
the same inputs (an opaque 38-byte JSON body) — inconsistent with `status`
returning 200.

## Root cause (in code)

`internal/server/routes_agent.go:19-42` — `handleAgentStatus`:

```go
func (s *Server) handleAgentStatus(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")

    if s.state == nil {
        writeJSON(w, http.StatusOK, map[string]any{"agent_id": id, "status": "idle"})  // (1) bug
        return
    }

    data, err := s.state.Get(r.Context(), "agent_runs", id)
    if err != nil || data == nil {
        writeJSON(w, http.StatusOK, map[string]any{"agent_id": id, "status": "idle"})  // (2) bug
        return
    }

    if !json.Valid(data) {
        writeJSON(w, http.StatusOK, map[string]any{"agent_id": id, "status": "idle"})  // (3) bug
        return
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    _, _ = w.Write(data)
}
```

All three error branches collapse to a happy 200. The handler should:

1. Look up the agent in the agents table first; 404 if missing
2. If state store is nil → 503 with body `{"error":"state store unavailable"}`
3. If state.Get returns an error → log + 500 with generic body
4. If data is nil (no run record yet) → 200 with `status:"idle"` ONLY when the agent was confirmed to exist in step 1
5. If data is not valid JSON → log + 500 (state corruption)

## Impact

- **Operator UX**: `crewship agent status <typo>` reports "idle" — looks
  like everything is fine; user proceeds to take actions against a
  non-existent agent.
- **Silent failure**: state-store outage shows as "every agent is idle,
  all good." Monitoring dashboards built on this endpoint would miss
  a real production incident.
- **Reflective output (cosmetic)**: the handler echoes the user-supplied
  ID into the JSON without sanitisation. Not a stored-XSS vector
  (the IPC socket is 0600 owner-only, not internet-reachable), but
  worth noting if this handler is ever copied to the public HTTP mux.

## Repro on a fresh checkout

```
go test ./internal/server -run TestHandleAgentStatus    # currently passes —
                                                        # add cases:
                                                        # - nonexistent ID → expect 404
                                                        # - nil state     → expect 503
```

## Out of scope

`/agents/{id}/start` 500-on-bad-input is related but probably a missing
input-validation branch; out of scope for this issue but worth fixing
in the same PR. Reporting per `/loop` directive testing IPC surface.



---

## Issue #565

**Title:** Langfuse: agent.invoke spans arrive but lack `gen_ai.*` semconv — 0 GENERATIONs, no cost/token tracking

**Labels:** from-audit-2026-05-23, dev3, perf

**Created:** 2026-05-23T14:17:53Z  **Author:** Srbino

---

## Problem

Agent runs reach the OTel pipeline and Langfuse receives the
`agent.invoke` root span — but **zero `gen_ai.*` attributes** are
emitted, so:

- Langfuse displays the trace as a plain `SPAN`, not a `GENERATION`
- `totalCost: 0` — no per-call cost tracking possible
- No token counts (input / output / cached)
- No `gen_ai.request.model` — operator can't see which model ran
- No prompt / completion preview — Langfuse's flagship LLM diff view stays empty
- No `langfuse.session.id` → every run is a sibling, no thread grouping
- No `langfuse.user.id` → no per-user cost dashboards

## Repro (verified just now on dev3, commit b640d3f6)

1. Provisioned a CLI token for `demo@crewship.ai` on workspace
   `cmpgzqq2500020193c936`.
2. Baseline Langfuse:
   ```
   traces: 46  generations: 0  sessions: 0
   ```
3. Ran:
   ```
   crewship ask --agent eva "Reply with just the word PONG and nothing else."
   ```
4. Output: `PONG`, latency ~4 s.
5. Langfuse 5 s later:
   ```
   traces: 65  generations: 0  sessions: 0
   ```
   ⇒ +19 traces, **+0 generations**.

The new `agent.invoke` trace (id `c1a8ea09a426a27d6ad0f1a770a6f86b`)
groups 37 child spans, all of them either `agent.invoke` itself or
Docker SDK calls (`POST /v1.51/containers/create`,
`POST /v1.51/exec/.../start`, etc.). Full payload of the root span:

```json
{
  "name": "agent.invoke",
  "metadata": {
    "attributes": {
      "crewship.agent.id":   "cmpgzy4j600099f8e65a5",
      "crewship.agent.type": "LEAD",
      "crewship.crew.id":    "cmpgzxxwo0008a30fb8e5"
    },
    "scope": { "name": "github.com/crewship-ai/crewship/internal/telemetry" }
  },
  "userId": null, "sessionId": null,
  "input": null, "output": null,
  "totalCost": 0, "latency": 4.074
}
```

## Root cause

For the `CLAUDE_CODE` adapter (the most common case), Crewship
orchestrates an ephemeral Docker container and the actual LLM call
happens **inside that container** via the Claude Code CLI talking to
Anthropic directly. Crewship's Go process sees:

- Docker SDK calls (containers/create, exec, wait, delete) — these
  produce spans via the indirect `otelhttp` dep on the Docker client
- One root `agent.invoke` span from `internal/telemetry/spans.go`

…but never the actual LLM request/response, so it has no way to
emit `gen_ai.*` semconv attributes on its side.

The other defined spans in `internal/telemetry/spans.go`
(`tool.execute`, `llm.call`, `routine.run`, `routine.step`) also
don't appear — `llm.call` in particular would be the natural place
to attach GenAI semconv, but it's only emitted when Crewship itself
is the LLM client (not via a CLI adapter).

## Suggested directions (pick one or combine)

### A. Propagate OTel env into the container

If `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_RESOURCE_ATTRIBUTES`
(with `service.name`, `crewship.agent.id`, `crewship.crew.id`,
`langfuse.session.id`, `langfuse.user.id`), and the W3C tracecontext
of the active `agent.invoke` span are injected into the agent
container, then any inside-container instrumentation lands in the
same trace tree.

Anthropic's Claude Code CLI ships with OTel exporters since the
2025 line — confirm version pinned in
`internal/provider/cliadapter/claude_code.go` and enable via
`CLAUDE_CODE_ENABLE_TELEMETRY=1`.

### B. Parse Claude Code's JSON stream output

`crewship ask` consumes Claude Code's `--output-format=stream-json`
which already carries `usage` blocks per assistant turn. Crewship
could synthesize a `GENERATION` span per turn from that stream and
emit it server-side with the right semconv:

```
gen_ai.system            = "anthropic"
gen_ai.request.model     = <from streamed model field>
gen_ai.usage.input_tokens
gen_ai.usage.output_tokens
gen_ai.usage.cache_creation_input_tokens
gen_ai.usage.cache_read_input_tokens
```

Lower implementation cost than (A); higher latency to telemetry.

### C. Minimum viable: enrich `agent.invoke` itself

Even without GenAI semconv, adding three attributes to the existing
`agent.invoke` span would unblock most Langfuse use cases:

```go
span.SetAttributes(
  attribute.String("langfuse.session.id", chatID),
  attribute.String("langfuse.user.id",   userID),
  attribute.String("crewship.adapter",   "CLAUDE_CODE"),
)
```

The first two are the magic strings Langfuse looks for to group
traces into sessions and per-user dashboards. Pure metadata; no
token accounting yet, but the UX is dramatically better.

## Out of scope / sibling issues

- #557 covers HTTP-server side spans (otelhttp wrapping) — orthogonal
- #556 (`service.version: dev`) is visible on every trace including
  these — same root cause, separate issue
- PR #552 wires Pyroscope; doesn't address Langfuse

## Suggested labels

`from-audit-2026-05-23`, `dev3`, `observability`, `langfuse`



---

## Issue #566

**Title:** `crewship admin` operates on wrong DB on multi-slot installs — silently lies about users

**Labels:** from-audit-2026-05-23, dev3

**Created:** 2026-05-23T14:18:30Z  **Author:** Srbino

---

## Problem

`crewship admin` always reads from `~/.crewship/crewship.db` (the
default data dir). On a multi-slot dev box where each slot has its
own DB at `/opt/crewship_N/crewship.db`, this means **the admin
commands operate on the wrong database** unless the operator knows
about a hidden env var.

Concrete failure mode discovered today on dev3:

```
$ crewship admin list-users
EMAIL  NAME  CREATED  LOCKED  ROLES
(no users — run `crewship seed` or hit POST /api/v1/bootstrap)

$ sqlite3 /opt/crewship_3/crewship.db "SELECT email FROM users"
demo@crewship.ai      ← actually exists, admin command lied
```

Same hidden-failure on `reset-password`:

```
$ crewship admin reset-password --email demo@crewship.ai --password X
no user with email "demo@crewship.ai"

$ CREWSHIP_DATA_DIR=/opt/crewship_3 crewship admin reset-password --email demo@crewship.ai --password X
✓ Password reset for Demo User (demo@crewship.ai).
  3 active session(s) revoked.
  Lockout (if any) cleared.
```

## Why this matters

The user-facing purpose of `crewship admin` is exactly the "operator
can't log in" recovery case (per its own `--help`). On every
multi-slot dev box and every prod install where the data dir isn't
the default, the recovery command currently silently no-ops.

Worse — `admin list-users` returning "no users" matches the message
the server prints **right after a fresh bootstrap**. An operator
running it on the wrong DB might incorrectly conclude the server
needs re-bootstrapping and POST `/api/v1/bootstrap` — which would
fail (403 setup-token-closed), but the wasted-cycle confusion is
real.

## Root cause

`cmd/crewship/cmd_admin.go:98` calls `resolveDataDir()` (or
equivalent). The resolver consults `CREWSHIP_DATA_DIR` env first,
then falls back to `~/.crewship`. **There's no `--data-dir` flag
exposed on `crewship admin`** and no mention of the env var in
`crewship admin --help` or any subcommand's `--help`. From the
operator's perspective the data path is undiscoverable.

## Suggested fix (small)

1. Add `--data-dir` global flag to `crewship admin` (and its
   subcommands) — promotes the env to a first-class option.
2. Document `CREWSHIP_DATA_DIR` in `crewship admin --help` text.
3. Make `admin list-users` print **the data-dir it's reading from**
   at the top, so "no users" becomes self-explanatory:

   ```
   $ crewship admin list-users
   data dir: /home/ubuntu/.crewship/crewship.db   ← add this line
   (no users — DB is empty)
   ```

4. (Optional) If we can detect that the active crewship daemon is
   running with `CREWSHIP_STORAGE_BASE_PATH` set to a different
   path than the admin command's data-dir, warn:
   ```
   ⚠ Detected running crewshipd uses /opt/crewship_3 as data dir.
     This admin command operates on /home/ubuntu/.crewship.
     Use --data-dir=/opt/crewship_3 to target the live database.
   ```

## Out of scope / related

- Same UX gap on `crewship seed` (also writes to default data dir
  silently) — same fix applies.
- The "no users" false-negative is also a content bug — DB query
  may be reading wrong table or column; could not reproduce in
  isolation without the wrong-DB confounding factor. Worth re-test
  after #1-2 land.



---

## Issue #569

**Title:** security: ZERO rate limiting on auth endpoints — mass signup + credential stuffing wide open

**Labels:** security

**Created:** 2026-05-23T15:10:22Z  **Author:** Srbino

---

**Detected by:** release-readiness loop iter #9 (rate-limit probe)
**Severity:** HIGH — release blocker for any internet-facing deployment
**Repo SHA:** b640d3f6

## Finding

No rate limiting exists on **any** Crewship auth endpoint. Measured on
dev1 today:

| Endpoint | Flood test | Result |
|----------|-----------|--------|
| `POST /api/v1/auth/signup` | 200 signups in 5s, all unique emails | **200 × HTTP 201** (200 throwaway accounts created in dev1 DB as collateral) |
| `POST /api/auth/callback/credentials` | 200 wrong-password attempts on real account | **200 × HTTP 403** in **0s**. No lockout, no 429, no captcha trigger |
| `GET /api/v1/system/setup-status` | 200 reqs in 5s | **200 × HTTP 200** (40 req/s sustained on public unauth endpoint) |

No `Retry-After`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`, or
`RateLimit-Limit` (RFC 9239) headers appear on any response —
indicating no rate-limiting middleware is installed.

## Attack vectors this opens

1. **Mass account creation** — script with random emails fills `users`
   table arbitrarily. Pollutes DB, breaks `crewship admin list-users`
   visualisation, potentially exhausts disk via bcrypt rows.
2. **Credential stuffing / brute force** — script with `(email, [top
   1000 passwords])` runs to completion in seconds against any known
   email. Account lockout (per `crewship admin reset-password` hints
   at) only fires on failed-login DB triggers — but no per-IP or
   per-target rate limit means an attacker pivots emails freely.
3. **Public-endpoint DoS amplification** — 40 req/s × 100 attackers
   = 4000 req/s. With current dev hardware Crewship handles it (we
   measured 184 req/s with no degradation in k6 soak), but a leaky-
   bucket gate avoids the question entirely.

## Reproduce

```bash
# 200 signups in 5 seconds:
for i in $(seq 1 200); do
  curl -s -o /dev/null -w "%{http_code}\n" -X POST \
    -H "Content-Type: application/json" \
    -d "{\"email\":\"flood-$i@example.com\",\"password\":\"x\",\"full_name\":\"f\"}" \
    https://crewship-dev1.unifylab.cz/api/v1/auth/signup &
  [ $((i % 20)) = 0 ] && wait
done; wait | sort | uniq -c
# expect: 200 × 201    (i.e. all accepted)
```

## Suggested fix

Standard middleware stack — pick one of:

1. **`go-redis/redis_rate`** + Redis backend (already have Redis from
   Langfuse). 30 lines of middleware, IP-keyed leaky bucket. Best
   long-term posture.
2. **`ulule/limiter`** in-memory token bucket. No new dep, works
   single-node, breaks on multi-instance horizontal scale.
3. **Caddy rate-limit module** at the reverse proxy. Zero Go code,
   but Caddy has to be in the path (true for prod, true for dev
   slots — already satisfied).

Recommend **(3) Caddy `rate_limit` directive** as the immediate fix
(0 Crewship code change, ships TODAY) plus **(1) redis-backed
middleware** as the durable fix once we're sure of the per-route
limits.

Recommended initial limits:
- `/api/v1/auth/signup`: 5 req/min per IP
- `/api/auth/callback/credentials`: 10 req/min per IP, 5 attempts per
  account in 15 min
- `/api/v1/auth/forgot`, `/api/v1/auth/reset`: 3 req/15 min per IP
- Anonymous public endpoints (health, csrf, providers): 60 req/min
  per IP (generous; the cost is real DB hits)
- Authenticated endpoints: 600 req/min per user (matches dashboard
  polling shape comfortably)

Plus: emit standard `RateLimit-*` headers per IETF draft so SDKs +
ops tooling see the gate.

## Acceptance

- `curl … signup … & wait` of 200 requests returns ≥ 195 × HTTP 429
- `Retry-After` header present on every 429
- Successful login after a 429 backoff returns 200 (no permanent
  block — sliding window)
- `/api/v1/dev/rate-limit-stats` (or similar) shows per-IP token
  counts for ops visibility

## Cleanup of 200 throwaway accounts on dev1

```bash
ssh crewship-dev "sqlite3 /opt/crewship_1/crewship.db \"DELETE FROM users WHERE email LIKE 'flood-%@unify.cz';\""
```
(I'll do this myself after this issue lands.)

## Related

- This is what makes OWASP API Top 10 #4 (Unrestricted Resource
  Consumption) apply to Crewship.
- Without this, the SOC 2 CC6.1 control on logical access can't be
  argued.


---

## Issue #572

**Title:** security: Caddy access log captures X-Internal-Token / Authorization / session cookie headers in plaintext

**Labels:** security

**Created:** 2026-05-23T15:35:24Z  **Author:** Srbino

---

**Detected by:** release-readiness loop iter #15 (token-leak check)
**Severity:** MEDIUM-HIGH — secret-in-logs across the request path
**Repo SHA:** b640d3f6
**Infra:** Caddy reverse proxy in front of every Crewship dev/prod slot

## Finding

Caddy's default JSON access log captures **every request header**, including
auth headers. The current `infrastructure/crewship/caddy/crewship-dev.Caddyfile`
has no header filtering, so the access log files
(`/var/log/caddy/crewship-dev*.access.log`) contain in plaintext:

- `X-Internal-Token: <64-byte secret>` — server↔sidecar/CLI internal auth
- `Authorization: Bearer crewship_cli_<64-hex>` — CLI bearer tokens
- `Cookie: __Secure-authjs.session-token=<JWE blob>` — web session tokens

Verified today on dev1 by triggering one internal-API call with a token and
grepping the log — got 1 full hit + the `X-Internal-Token` header name itself.
Sample log line (token redacted by the reporter before paste):

```json
{"request":{"headers":{"X-Internal-Token":["<REDACTED-FULL-TOKEN>"], ...}}, ...}
```

Existing log volume: Caddy logs every request, so over time these logs accumulate
**every CLI session token + every internal-token use + every authenticated user
session-token** that the proxy sees. Logs rotate to disk, are often shipped to
log aggregators (Loki/ELK), and are accessible to anyone with file-read on the
VM or aggregator access.

## Why it matters

1. **Lateral movement**: an attacker who reads `/var/log/caddy/` (or compromised
   log shipper) extracts current internal tokens → unauthenticated access to
   `/api/v1/internal/*` surfaces (the 46 endpoints we audited in iter #4).
2. **Session hijacking**: NextAuth session JWE values are logged → replay
   against any slot until token expiry.
3. **CLI token leak**: pairs with #571 (`--server` flag SSRF) — token logged
   on every legitimate `crewship` invocation; even without #571 attack,
   anyone with log access gets the tokens "for free".
4. **Compliance**: ISO 27001 A.8.32, SOC 2 CC6.6, PCI-DSS 3.4 — all require
   secrets not to be logged in plaintext.

## Reproduce

```bash
ssh crewship-dev
# pick whatever token your env uses
TOKEN=$(sudo tr '\0' '\n' < /proc/$(pidof crewship)/environ | grep INTERNAL_TOKEN | cut -d= -f2)
curl -s -H "X-Internal-Token: $TOKEN" https://crewship-dev1.unifylab.cz/api/v1/internal/crews
sudo grep -c "$TOKEN" /var/log/caddy/crewship-dev1.access.log
# expect: > 0
```

## Suggested fix

Add a `log` block to each Caddy site that **deletes the sensitive headers**
before serialization. Caddy syntax:

```caddyfile
log {
    output file /var/log/caddy/crewship-dev1.access.log
    format json
    # Redact auth headers from logged requests.
    # Caddy applies these at serialize time; the actual upstream request
    # still carries the header — only the log copy is scrubbed.
    format filter {
        request>headers>X-Internal-Token delete
        request>headers>Authorization delete
        request>headers>Cookie delete
        request>headers>__Secure-authjs.session-token delete
    }
}
```

(Verify exact filter syntax against Caddy 2.x docs — the `filter` module
on the `format` directive is what does selective deletion.)

Apply to every site block: `crewship-dev*.unifylab.cz`, `langfuse.unifylab.cz`,
`grafana.unifylab.cz`, `tempo.unifylab.cz`, `dtrack.unifylab.cz`. Easiest:
extend the existing `(crewship_common)` snippet in
`infrastructure/crewship/caddy/crewship-dev.Caddyfile` so adding a new site
picks up the filter automatically.

**Plus:** add a one-shot cleanup pass to scrub historical logs:

```bash
ssh crewship-dev "sudo find /var/log/caddy -name '*.log' -exec sed -i \
  -E 's/(Authorization|X-Internal-Token|session-token)\":\\[\"[^\"]+\"]/\\1\":[\"<REDACTED>\"]/g' {} \;"
```

## Acceptance

- After Caddy reload with the filter, repeat the repro: grep on the log
  returns 0.
- Historical logs scrubbed (or rotated out — whichever is faster).
- Crewship's own `slog` output already redacts (confirmed in iter #15:
  go.log had 0 token hits) so only Caddy-side needs fixing.

## Related

- #571 (CLI `--server` SSRF) — same token would leak both ways.
- #569 (no rate limiting) — attacker who extracts a token from logs has
  unlimited replay budget.
- #559 (closed FP) was a sibling info-disclosure category.


---

## Issue #573

**Title:** auth_recovery.Reset: dead `subtle.ConstantTimeCompare` (compares value to itself)

**Labels:** security

**Created:** 2026-05-23T15:51:48Z  **Author:** Srbino

---

**Severity:** LOW (correctness / misleading defensive code; not directly exploitable)

**Location:** `internal/api/auth_recovery.go:294-298`

**Summary:** The "defense in depth" constant-time compare in `RecoveryHandler.Reset` compares `tokenHash` to a `storedHash` that is just a fresh assignment of the same `tokenHash` — so the compare always returns 1 regardless of any DB content. The comment claims defense against a timing oracle that this code does not actually provide.

```go
// Defense in depth: even though SQL filtered by tokenHash, do a
// final constant-time compare so two equal-length hash strings
// don't ride a millisecond timing oracle.
storedHash := tokenHash                                                   // <-- assigned from input, not from DB
if subtle.ConstantTimeCompare([]byte(tokenHash), []byte(storedHash)) != 1 {
    replyError(w, http.StatusBadRequest, "Invalid or expired token")
    return
}
```

The preceding SELECT (`auth_recovery.go:278-280`) deliberately scans only `identifier` and `expires` — the token column is never returned, so there is no DB-side value to compare against.

**Why this matters:**
1. **False sense of security in code review.** A future auditor (or an LLM-based security scan) sees `subtle.ConstantTimeCompare` and considers the timing-oracle question answered. It is not — the actual constant-time property comes from the SQL `WHERE token = ?` filter at the DB layer, which is a different (and adequate, but unverified-by-this-test) mechanism.
2. **Dead code that pretends to be live.** Hard to lint, easy to leave in place for years.

**Not directly exploitable today** because:
- The DB-side `WHERE token = ?` filter on the hashed token already does the comparison.
- Reset tokens are 256-bit (`generateResetToken` = 32 random bytes hex-encoded, `auth_recovery.go:392-398`) — brute-force is computationally infeasible regardless of timing.

**Suggested fix — pick one:**

**Option A (delete the dead branch):**
```go
// The SQL WHERE token = ? above already provided the lookup;
// no separate constant-time compare is needed because SQLite's
// b-tree index lookup runs in non-secret-dependent time.
```

**Option B (make it real — actually retrieve and compare stored hash):**
```go
var email, expiresStr, dbTokenHash string
err = tx.QueryRowContext(r.Context(), `
    SELECT identifier, expires, token FROM verification_tokens
    WHERE token = ? AND purpose = 'password_reset'`, tokenHash).Scan(&email, &expiresStr, &dbTokenHash)
// ... existing error handling ...

if subtle.ConstantTimeCompare([]byte(tokenHash), []byte(dbTokenHash)) != 1 {
    replyError(w, http.StatusBadRequest, "Invalid or expired token")
    return
}
```

Option A is honest. Option B is genuine defense in depth (protects against e.g. a SQLite collation quirk or LIKE-like behavior in `WHERE token = ?`). Either beats leaving the misleading comment + no-op compare in place.

**Discovery context:** Found during iter #18 of an audit-stack pass on `crewship-dev1.unifylab.cz` (HEAD `b640d3f6`), 2026-05-23. Initial finding came from probing `/api/v1/auth/reset` brute-force resistance — source review of the handler turned this up.

**Adjacent findings (already covered or non-issues):**
- `/api/v1/auth/reset` lacks rate limiting → already part of #569 (token entropy makes brute-force infeasible, but logged for completeness)
- Reset token entropy = 256 bits via `crypto/rand` ✓
- Token stored as hash, never plaintext ✓
- Per-purpose check (`purpose = 'password_reset'`) prevents cross-purpose token confusion ✓
- Generic "Invalid or expired token" response for all failure paths (no enumeration) ✓
- Sessions revoked on successful reset, lockout state cleared ✓
- 30-min TTL enforced ✓

This is the only odd thing in an otherwise solid handler — worth fixing for code quality + audit clarity.


---

## Issue #574

**Title:** WebSocket upgrade ticket leaks into Caddy access log via ?token= query (15-min replay window)

**Labels:** security

**Created:** 2026-05-23T15:56:07Z  **Author:** Srbino

---

**Severity:** MED-HIGH (sibling to #572 — bearer in plaintext at log layer)

**Summary:** `Hub.HandleUpgrade` and `terminal.Handler` accept the WS auth ticket via the `?token=` query parameter. Because Caddy's default JSON access log captures the full request URI, every successful WS handshake writes the ticket verbatim into `/var/log/caddy/crewship-*.access.log`. The ticket is a valid bearer for `WSTicketTTL = 15 * time.Minute` (`internal/auth/jwt.go:46`), so any reader of the access log (operator, log shipper, SIEM, backup tarball) can replay it during that window.

**Reproduction (2026-05-23 on `crewship-dev1.unifylab.cz`, HEAD `b640d3f6`):**

```bash
MARKER="WS_TICKET_LEAK_PROBE_$(openssl rand -hex 8)"
curl -s -o /dev/null \
  -H "Upgrade: websocket" -H "Connection: Upgrade" \
  -H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" \
  "https://crewship-dev1.unifylab.cz/ws?token=$MARKER"

sudo grep "$MARKER" /var/log/caddy/crewship-dev1.access.log
# {"...","uri":"/ws?token=WS_TICKET_LEAK_PROBE_402e5e73a1f0574f",...}
```

Confirmed on both `/ws` (chat hub, `internal/ws/hub.go:381`) and `/ws/terminal` (`internal/terminal/handler.go`). Crewship's own slog does NOT log the URI ✓ — only Caddy does.

**Why URL-query auth was the original choice (and why it's now wrong):**
- Browsers' native `WebSocket` constructor can't set custom headers, so historically people punted to query strings or `Sec-WebSocket-Protocol` smuggling.
- This is still the dominant browser path, but the **right** modern way is the `Sec-WebSocket-Protocol` subprotocol header — browsers can set it via the `WebSocket(url, protocols)` constructor, and it lands in a header (not the URI), so it is not captured by default request-line / URI access logging.

**Compounding factors:**
1. Ticket TTL is 15 min — long enough for replay even from a daily-rotated log.
2. Caddy's default JSON log forwards `request.uri` end-to-end to whatever log sink the operator configures (Loki, S3, CloudWatch). Each hop is a new copy of the bearer.
3. `WSTicket` claims include `userID`, `sessionID`, `name`, `email` (see `IssueWSTicket` in `internal/auth/jwt.go:144`). Replay = full session takeover until the underlying user_session is revoked.
4. The chat hub's revoke-on-WS path (`hub.go:404-418`) only fires *if* the underlying browser session is revoked. Ticket replay against an active session is indistinguishable from a legitimate reconnect.

**Adjacent same-class concerns (separate issues if accepted as a pattern):**
- `/exposed/{token}/...` (port-expose route param)
- `/api/v1/webhooks/{token}` (pipeline webhook bearer)
- `/api/v1/oauth/callback?code=...` (OAuth code — but those are spec-required in URL and have short single-use semantics)

These all share the property that the bearer-class secret rides in the URI and lands in any URI-logging proxy. The fix pattern (below) is reusable.

**Suggested fixes:**

**Short-term (operator-side, ships today):** Add a Caddy log filter that scrubs the `token` query param. Same shape as the X-Internal-Token fix proposed in #572:

```caddyfile
(crewship_common) {
  log {
    format filter {
      wrap json
      fields {
        request>uri query {
          replace token REDACTED
        }
      }
    }
  }
}
```

Caddy supports `query` filter on the URI field (`caddyserver/caddy#5085`), so the scrub can happen at the access-log layer without code changes.

**Long-term (code-side, ships with next breaking auth change):** Switch the WS upgrade to read the ticket from `Sec-WebSocket-Protocol`:

- Client (browser): `new WebSocket(url, ["crewship.v1", "auth.bearer." + ticket])`
- Server: `wsticket := pickFromSubprotocols(r.Header["Sec-WebSocket-Protocol"], "auth.bearer.")` ; echo back `crewship.v1` only (per WS spec)
- Server then keeps the existing `ValidateWS` + session revoke check unchanged.

This is the same trick `cmux`/`gRPC-Web` use to get bearers off the URL. The protocol token in `Sec-WebSocket-Protocol` is NOT in the standard access log format (it's a request header), so it never lands in `request.uri`.

**Adjacent clean checks (no findings on the WS handler itself):**
- Origin gate on hub: ✓ (Origin host matched to req.Host, with `CREWSHIP_ENV=production` gating the localhost bypass — `hub.go:435-454`)
- Origin gate on terminal: ✓ (same-origin equality for browsers, `X-Crewship-Client` header required for Origin-less clients — `terminal/handler.go:72-95`)
- 64 KiB inbound frame cap on hub (`hub.go:469`) — protects against fan-out N-amplifier
- 1 MB read limit on terminal handler (`handler.go:118`)
- Per-purpose JWT keys (`v.wsKey` separate from `v.accessKey`, `v.refreshKey`) — refresh smuggled into `?token=` is rejected as `ErrWrongKind` (`jwt.go:151-167`)
- Post-upgrade auth (not pre-upgrade) on terminal handler — separates handshake from auth, allows clean error frames
- Session-revoke check on hub WS upgrade for browser tickets (`hub.go:404-418`)
- Channel authorize interface (`ChannelAuthorizer`) is wired separately — TODO follow-up: verify all `subscribe` ops actually go through it

This is the only meaningful exposure on the WS surface I found in iter #19 of the audit-stack pass. Filing focused so the Caddy filter ships before the larger subprotocol refactor.


---

## Issue #575

**Title:** WS hub: handleSendMessage fail-open when channelAuth nil + bridge ignores userID (defense-in-depth gap)

**Labels:** security

**Created:** 2026-05-23T16:00:54Z  **Author:** Srbino

---

**Severity:** MED-HIGH (regression risk — not exploitable today, single config error away from full cross-user chat injection)

## Two related findings, one fix

### Finding 1: `handleSendMessage` fail-open inconsistency

`internal/ws/client.go:181-200` (subscribe) and `client.go:321-333` (handleSendMessage) take **opposite** approaches to a nil `channelAuth`:

```go
// subscribe() — line 181:  FAIL CLOSED ✓
if c.hub.channelAuth == nil {
    c.hub.logger.Warn("channel subscription denied: no authorizer configured", ...)
    /* return "access denied" error to client */
    return
}
if !c.hub.channelAuth.CanSubscribe(c.ctx, c.userID, channel) { /* deny */ }

// handleSendMessage() — line 321:  FAIL OPEN ✗
if c.hub.channelAuth != nil {                                // <-- inverted
    sessionCh := "session:" + payload.ChatID
    if !c.hub.channelAuth.CanSubscribe(c.ctx, c.userID, sessionCh) { /* deny */ }
}
// if channelAuth was nil, control falls through to the goroutine
// that calls c.hub.chatHandler.HandleChatMessage with the user-supplied
// payload.ChatID and no further authorization check
```

If a deployment ever boots without calling `Server.SetChannelAuthorizer` (regression, test harness, partial init, etc.), the chat handler accepts `send_message` for any chatID from any authenticated user — cross-tenant chat injection.

### Finding 2: `bridge.HandleChatMessage` accepts `userID` but never authorizes it

`internal/chatbridge/bridge.go:245`:

```go
func (b *Bridge) HandleChatMessage(ctx context.Context, userID, chatID, content string, streamFn func(ws.ChatEvent)) error {
    // ... immediately calls b.resolver.ResolveChat(ctx, chatID) ...
    // userID is never compared against the chat's workspace membership
}
```

`grep "userID" internal/chatbridge/bridge.go` returns exactly one match — the function signature. The parameter is decorative. This means **bridge.HandleChatMessage is the inner authorization layer that doesn't exist** — the WS hub's `channelAuth` is the ONLY check standing between an authenticated socket and any chat's message stream.

Two layers, one of them missing, the other inconsistent → a config error in either layer alone collapses the whole gate.

## Why this doesn't pop today

Production wires `Server.SetChannelAuthorizer` in `internal/server/server.go:884-886`, and the authorizer (`NewDBChannelAuthorizer`) hard-panics if `db == nil` (`channel_auth.go:22-25`), so a misconfigured prod boot would crash loud instead of silently fail-open.

So this is **regression-protection-only** today. But:
- The fail-open code path exists and is reachable via a one-line change (anyone refactoring server.go and skipping the SetChannelAuthorizer call would not get a test failure for handleSendMessage)
- The bridge having `userID` as an unused parameter is a Chekhov's gun: future code may assume it's been authorized when it hasn't

## Suggested fixes

**Fix 1 (1-line, ships in same PR):** Mirror `subscribe()`'s pattern in `handleSendMessage`:

```go
// in handleSendMessage, after the payload validity checks (~line 320):
if c.hub.channelAuth == nil {
    c.hub.logger.Warn("send_message denied: no authorizer configured", "user_id", c.userID, "chat_id", payload.ChatID)
    resp, _ := json.Marshal(ServerMessage{
        Type:    "error",
        Channel: msg.Channel,
        Payload: map[string]string{"error": "access denied"},
    })
    c.safeSend(resp)
    return
}
sessionCh := "session:" + payload.ChatID
if !c.hub.channelAuth.CanSubscribe(c.ctx, c.userID, sessionCh) {
    /* existing deny */
}
```

**Fix 2 (defense-in-depth, in bridge):** Verify `userID` against `chatID` workspace membership inside `HandleChatMessage`, before any side-effecting work. The existing `b.resolver.ResolveChat(ctx, chatID)` already returns workspace_id (`info.WorkspaceID`) — adding a single membership check is cheap:

```go
// near the top of HandleChatMessage, right after ResolveChat:
if b.workspaceAuth != nil {
    ok, authErr := b.workspaceAuth.IsMember(ctx, userID, info.WorkspaceID)
    if authErr != nil || !ok {
        streamFn(ws.ChatEvent{Type: "error", Content: "access denied"})
        return fmt.Errorf("user %s not authorized for chat %s", userID, chatID)
    }
}
```

Both layers check → either alone failing is contained.

## Minor adjacent finding (LOW): stale comment

`internal/ws/hub.go:52`:
```go
cancelFns    map[string]context.CancelFunc // session_id -> cancel function for active runs
```

The actual key is `userID + ":" + chatID` (composite — see `client.go:266` and `client.go:343`). The composite scheme is what gives `handleCancelMessage` its implicit per-user isolation (user A's cancel for chat1 looks up `A:chat1`, user B's cancel for the same chat looks up `B:chat1` — different keys, harmless).

The comment misleads anyone refactoring this. Fix:
```go
cancelFns    map[string]context.CancelFunc // (userID + ":" + chatID) -> cancel; composite key isolates cross-user cancels without an explicit ACL call
```

`handleCancelMessage` could also gain an explicit `CanSubscribe("session:"+chatID)` check for symmetry with the fixed `handleSendMessage`, but the composite-key invariant + documentation refresh is sufficient.

## Adjacent clean checks (the rest of the WS surface)

- `subscribe()` fail-closed on nil authorizer ✓ (`client.go:181`)
- `DBChannelAuthorizer.CanSubscribe` parses `type:id`, dispatches to per-type membership queries, fails closed on nil receiver/db/userID ✓ (`channel_auth.go:35-71`)
- Workspace membership backs every channel type (`workspace`, `crew`, `agent`, `session`, `keeper`, `files`, `mission`) ✓
- `providers` is the only intentional global channel — any-authenticated allow ✓
- `isMemberOfCrewWorkspace` filters `deleted_at IS NULL` (no soft-deleted crew hijack) ✓
- Hub upgrade path: per-kind JWT validation + session-revoke recheck for browser tickets ✓

**Discovery context:** Iter #20 of audit-stack pass on `crewship-dev1.unifylab.cz` (HEAD `b640d3f6`), 2026-05-23. Traced from `ChannelAuthorizer` interface → callsites → sibling `send_message`/`cancel_message` handlers → downstream bridge.


---

## Issue #576

**Title:** /exposed/{token}/ capability tokens leak into Caddy access log (up to 24h replay)

**Labels:** security

**Created:** 2026-05-23T16:05:38Z  **Author:** Srbino

---

**Severity:** MED-HIGH (sibling to #572, #574 — bearer-class secret in plaintext at log layer)

**Summary:** Port-expose capability tokens (`/exposed/{token}/...`) are 256-bit, properly random (`crypto/rand` in `port_expose_handler.go:184-190`), and the application's own slog already uses `safeTokenPrefix` (`port_expose_handler.go:194-199`) to log only the first 8 chars. But the full token rides in the URL **path segment**, so Caddy's default JSON access log captures it verbatim in `request.uri`. The capability TTL defaults to **1 hour** and can be requested up to **24 hours** (`port_expose_handler.go:70-71`), so a captured log line is a valid bearer for the entire remaining TTL.

This is the same leak class as #572 (X-Internal-Token header) and #574 (WS upgrade `?token=` query), but with a longer replay window and a different scrub mechanism — the token is a **path segment**, not a query param or header, so the Caddy filter from #574 (`request>uri query replace token REDACTED`) does not apply.

**Reproduction (2026-05-23 on `crewship-dev1.unifylab.cz`, HEAD `b640d3f6`):**

```bash
MARKER="EXPOSED_TOKEN_LEAK_$(openssl rand -hex 16)"
curl -s -o /dev/null "https://crewship-dev1.unifylab.cz/exposed/$MARKER/some/path?q=v"

sudo grep "$MARKER" /var/log/caddy/crewship-dev1.access.log
# {"...","uri":"/exposed/EXPOSED_TOKEN_LEAK_<32 hex chars>/some/path?q=v","status":404,...}
```

The unknown-token 404 still lands in the log with the full URI; legitimate proxied requests (200/3xx/4xx from upstream) leak it just the same.

**What's already done right (do not change these):**

The `ServeExposed` handler (`port_expose_list_revoke_serve.go:187`) is otherwise carefully written:
- Token is the capability (no separate auth needed) — documented and intentional
- Unknown → 404, expired → 410, WS upgrade → 426 (explicit method-level decisions)
- `r.Header.Del("Referer")` before reverse-proxying — **prevents the same token-in-URL leak via outbound third-party requests from the proxied container** (already saw this risk)
- `r.Header.Del("Cookie")` and `r.Header.Del("Authorization")` — proxied container never sees user's auth artifacts
- Container IP re-resolved per request via Docker inspect — defends against IP reuse after container restart
- Application slog uses `safeTokenPrefix(token)` everywhere (verified — no full-token writes to slog)

The only gap is the **Caddy access log**, which sees `request.uri` before any of the application's careful in-handler stripping runs.

**Suggested fixes:**

**Short-term (operator-side):** Caddy log filter that path-rewrites the `/exposed/<token>/` segment. Path filters in Caddy are not as ergonomic as query filters; the cleanest approach is a structured-log replace on the `uri` field using a regex:

```caddyfile
(crewship_common) {
  log {
    format filter {
      wrap json
      fields {
        request>uri replace_re ^/exposed/[^/]+ /exposed/REDACTED
      }
    }
  }
}
```

(Caddy's `replace_re` directive in the filter encoder — supported via the `caddyhttp.LogFilter` family of modules. If `replace_re` is not available on the installed Caddy build, fall back to a `request>uri delete` and rely on `request.host` + status code for log forensics.)

The same snippet would also need to scrub the `?token=` query from #574; the two filters compose:

```caddyfile
fields {
  request>uri replace_re ^/exposed/[^/]+ /exposed/REDACTED
  request>uri query {
    replace token REDACTED
  }
}
```

**Long-term (code-side):** Bind the capability to a short-lived cookie on first hit, redirect to a cookie-gated URL. Sketch:
1. `GET /exposed/{token}/...` → server validates token, mints `__Host-expose.<token-prefix>=...` cookie scoped to `Path=/exposed/proxy/<routing-id>`, issues 302 to `/exposed/proxy/<routing-id>/<rest>`.
2. Subsequent requests use the cookie; the path no longer carries the bearer.
3. Cookie lifetime = min(remaining capability TTL, 15min — refreshed on hit).
4. Original `/exposed/{token}/` URL still works (single-use redirect bootstrap), so the "agent hands user a URL" UX is preserved.

This matches how ngrok / Cloudflare Tunnels handle this — the capability URL is for delivery, but the long-lived session lives in a cookie.

**Adjacent same-class issues (already known):**
- #572 — X-Internal-Token in Caddy log
- #574 — WS upgrade `?token=` in Caddy log (15-min replay)
- This (#576) — `/exposed/{token}/` in Caddy log (up to 24h replay) ← biggest window of the three

If all three are accepted, the operator-side fix is one consolidated `crewship_common` snippet that scrubs all three patterns in a single log filter block.

**Discovery context:** Iter #21 of audit-stack pass on `crewship-dev1.unifylab.cz`, 2026-05-23.


---

## Issue #577

**Title:** Webhook flattenHeaders forwards Authorization/Cookie/X-Internal-Token into pipeline inputs unfiltered

**Labels:** security

**Created:** 2026-05-23T16:11:15Z  **Author:** Srbino

---

**Severity:** MED (footgun for any pipeline that templates from `inputs.headers.*`; depending on pipeline step rendering, can escalate to RCE)

**Location:** `internal/api/pipeline_webhooks.go:510-522` (`flattenHeaders`) used at `pipeline_webhooks.go:364` (`"headers": flattenHeaders(r.Header)`)

**Summary:** `FireWebhook` passes the full request header set into the pipeline executor as `inputs.headers.<lower_underscore>`, with no allowlist and no blocklist. The sender controls every value. Pipeline DSLs can template `{{ inputs.headers.x_event_type }}` etc., so any header an attacker chooses to set is substitutable into pipeline step arguments. Sensitive-by-convention headers like `Authorization`, `Cookie`, `Proxy-Authorization`, `X-Internal-Token`, and `X-Crewship-Signature` are passed through identically to user-meaningful headers.

```go
// flattenHeaders: no filter, just lowercase + dash->underscore
func flattenHeaders(h http.Header) map[string]string {
    out := make(map[string]string, len(h))
    for k, vs := range h {
        out[strings.ToLower(strings.ReplaceAll(k, "-", "_"))] = strings.Join(vs, ",")
    }
    return out
}
```

`reservedWebhookInputKeys` (the `event`/`raw`/`headers` confused-deputy fix from audit A17.2 M2) only locks the **top-level** keys — it does not constrain header sub-keys.

**Exploitable surfaces:**

1. **Concurrency-key bypass.** A pipeline author writes `concurrency_key: "{{ inputs.headers.x_tenant }}"` expecting tenant isolation. Attacker fires the webhook with `X-Tenant: alice`, `X-Tenant: bob` etc. — each value yields a distinct concurrency key, fanning out parallelism the operator never authorized.

2. **Header smuggling via reserved names.** `X-Internal-Token` is the bearer header for Crewship's internal API. A pipeline template `{{ inputs.headers.x_internal_token }}` would let an attacker plant arbitrary values that *look* like valid internal tokens to any downstream step that reads from `inputs.headers`. Same risk for `Authorization` and `Cookie`.

3. **Command injection via header value (depends on Render escaping).** A step like `args: ["--user-agent", "{{ inputs.headers.user_agent }}"]` interpolated into a shell command without per-arg quoting allows `User-Agent: ;rm -rf /` to escalate to RCE. Whether the template engine quotes the substituted value depends on the step kind — review of `executor_render.go:Render` would tell.

4. **Auth-header reflection back to operator.** Same-origin webhook receivers that log their inputs may write `inputs.headers.authorization` to a log aggregator, persisting any bearer the attacker chose to send. Cheap log-poisoning surface.

**Why this slipped past the chain of webhook hardening:**

The handler has been audited multiple times (A13.2, A17.2 M1, A17.2 M2, #490, H-iter5-A17.2). All of those focused on the **dispatch** layer (HMAC, idempotency, rate-limit, reserved top-level keys). The **input shape** delivered to the pipeline executor was treated as opaque sender-controlled payload — true for `event` / `raw`, but `headers` deserves the same scrutiny as a body field because the sender controls it identically.

**Suggested fix (small, focused):**

Allowlist — pass only headers the platform documents as available, and prefix them so operators learn the convention:

```go
// allowed: lowercase canonical names the platform contracts to forward
var webhookForwardableHeaders = map[string]struct{}{
    "content_type":      {},
    "user_agent":        {},
    "x_github_event":    {},  // GitHub webhook routing
    "x_gitlab_event":    {},  // GitLab
    "x_event_id":        {},  // operator-defined event id
    "x_event_type":      {},  // operator-defined event type
    "x_request_id":      {},  // tracing
    // any header an operator-controlled webhook source ships
}

func flattenHeaders(h http.Header) map[string]string {
    out := make(map[string]string, len(webhookForwardableHeaders))
    for k, vs := range h {
        key := strings.ToLower(strings.ReplaceAll(k, "-", "_"))
        if _, ok := webhookForwardableHeaders[key]; ok {
            out[key] = strings.Join(vs, ",")
        }
    }
    return out
}
```

Or blocklist — keep the wide-open default but strip the obvious sensitive names:

```go
var webhookBlockedHeaders = map[string]struct{}{
    "authorization":           {},
    "proxy_authorization":     {},
    "cookie":                  {},
    "set_cookie":              {},
    "x_internal_token":        {},
    "x_crewship_signature":    {},  // the HMAC the sender just provided — no use case in template
    "x_crewship_client":       {},  // CSRF-style header for WS
}
```

Allowlist is safer (deny-by-default); blocklist is more permissive but breaks no operator who's already using non-listed headers. Pick whichever matches the wider platform philosophy.

**Adjacent clean checks (the rest of the webhook surface is solid):**

- HMAC-SHA256 signature verification via `hmac.Equal` (constant-time) — `pipeline/webhooks.go:225-243` ✓
- Empty SigningSecret → ValidateSignature returns false (no bypass on legacy rows) — same file ✓
- Same 401 "signature mismatch" for both wrong-sig and no-secret-on-row → no enumeration ✓
- 1 MiB body cap ✓
- HMAC checked BEFORE rate limit (invalid sigs don't consume slots) ✓
- Per-token rate limit floored at `defaultWebhookRatePerMin` (600/min) when DB row has 0 — fixes audit A17.2 M1 ✓
- Idempotency: explicit header `Idempotency-Key`/`X-Crewship-Event-ID` → fallback to `sha256(token + body + signature)`, salted by token so cross-webhook same-body doesn't collide — closes H-iter5-A17.2 replay ✓
- `reservedWebhookInputKeys` blocks operator's `inputs_template` from overriding `event`/`raw`/`headers` — A17.2 M2 confused-deputy fix ✓
- 404 on unknown / disabled / soft-deleted token (no enumeration) ✓
- Token entropy: 32-byte hex via `crypto/rand` — `pipeline/webhooks.go:360` ✓
- `ErrConcurrencyLimitReached` → 429 with `Retry-After: 5` ✓
- Webhook fire recorded even on failure (`RecordFire`) — audit trail ✓

**Lesser concern (catalog only, not the primary finding):**

The webhook token rides in the URL path (`/api/v1/webhooks/{token}`) and is captured by Caddy access logs — same pattern as #574 (WS ticket) and #576 (port-expose). Severity here is lower: the token alone is insufficient to fire (HMAC signing secret is the real auth), and the idempotency cache defeats body-replay. But if both token and signing secret are captured (e.g. shared in a setup script that ends up in a log), an attacker can craft new signed bodies. The fix is the same operator-side Caddy filter recommended in #574/#576.

**Discovery context:** Iter #22 of audit-stack pass on `crewship-dev1.unifylab.cz` (HEAD `b640d3f6`), 2026-05-23.


---

## PR #552 (NOT deleted, body redacted in-place)

**Title:** feat(observability): opt-in pprof + pyroscope push + OTLP setup docs

**Branch:** feat/perf-pprof-langfuse

**Created:** 2026-05-23T10:35:11Z  **Author:** Srbino

---

## Summary

Three related observability deliverables from the 2026-05-23 perf review:

1. **Opt-in pprof endpoint** (`internal/telemetry/pprof.go`) behind
   `CREWSHIP_PPROF_ADDR`. The public HTTP surface still 404s
   `/debug/pprof/*` because exposing CPU profile generation to the
   internet is both an info leak and a DoS vector (a 30-second
   `/debug/pprof/profile` blocks all other GC). The dedicated server
   binds to operator-chosen address (recommended `127.0.0.1:6060`),
   emits a WARN on non-loopback binds (now including empty-host
   binds like `:6060`), and drains in-flight downloads with a
   3-second timeout on shutdown.

2. **Opt-in pyroscope-go push profiler** (`internal/telemetry/pyroscope.go`)
   behind `CREWSHIP_PYROSCOPE_URL`. Ships CPU + heap + goroutines +
   alloc + mutex + block profiles every 10 s, tagged with `slot` and
   `hostname` so a flame-graph view filters per dev slot. Block-profile
   rate set to 10 ms (was 5 ns, which sampled almost every event).
   Push URL credentials redacted before any log emission. Accepts
   `context.Context` for lifecycle.

3. **OTLP tracing setup guide** (`docs/observability/otlp-setup.md`)
   documenting how to point the existing `internal/telemetry` OTel
   GenAI pipeline at any OTLP-compatible backend. Zero Crewship code
   change required — just two env vars (endpoint + auth header).

## Real-world validation

pprof was used to capture a baseline profile on dev1 under 50-concurrent
load. Findings:

- **CPU**: 3.8% utilization over 20 s → idle-bound, not CPU-bound; no
  hotspot dominates.
- **Heap**: 12 MiB resident, no leaks.
- **Goroutines**: 49 total, 94% in `gopark` (healthy idle).
- **Allocations**: `io.ReadAll` is the #1 source (39.6%, 49 MiB in
  20 s). Worth auditing in a future pass to replace with
  `json.Decoder` streaming where the body is JSON-parsed downstream.

**No code fix recommended right now** — the data tells us not to
optimize prematurely. The infrastructure is the deliverable.

OTLP smoke verified on dev1: HTTP 200 from the backend, Crewship logs
`OTel GenAI telemetry enabled` on restart with the two env vars set.
Real LLM-call spans flow naturally on next chat.

## Test plan

- [x] `go build ./...` clean
- [x] `go vet ./...` clean
- [x] `go test -short ./internal/telemetry/ ./internal/server/` — all pass
- [x] pprof endpoint live on dev1 (port 6060, loopback)
- [x] Profile captured under 50-conc curl load — see findings above
- [x] OTLP smoke trace ingested + visible in backend UI
