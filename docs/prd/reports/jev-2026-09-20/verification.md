# Jev pilot verification, 2026-09-20

Base: `8a1ca5fc3`. No server deployment or default model change.

| Check | Result |
|---|---|
| Focused Go adapter/recipe/CLI tests | PASS |
| `go test -race ./internal/decisions -count=1` | PASS |
| Python evaluation metric tests | PASS, 3 tests |
| `go vet ./...` | PASS |
| `bash scripts/verify.sh quick` | PASS |
| `go run ./scripts/agents-invariants` | PASS |
| `go run ./scripts/docs-inventory -strict` | PASS after adding CLI documentation |
| `go run ./scripts/docs-surface-check` | PASS |
| Built CLI: 24 triage dry runs | PASS, no model inference |
| Built CLI: OpenRouter rerank dry run | PASS, no model inference |
| Authenticated live inference | BLOCKED: no TypeSafe/OpenRouter key available |
| `go test ./... -count=1` | Initial run: CLI YAML-tag guard failure (fixed); CLI, API and database hit default 10m timeout. All other packages passed. |
| CLI/API/database rerun with `-timeout 25m` | In progress at initial commit; update before declaring verification complete. |

The CLI guard requires mirrored YAML tags even for local JSON-only output;
the new envelope now complies. The documentation inventory also requires a
public page for every new command; `docs/cli/decisions.mdx` and navigation now
cover all four command paths.

The live probe stops before sending any request when the environment key is
absent. Separate unauthenticated reachability probes returned HTTP 403 from
TypeSafe and HTTP 401 from OpenRouter. Neither is a model-quality test.

The fixture consists of 12 paired EN/CS synthetic scenarios. The 24 successful
dry runs validate request generation only. No accuracy, latency gain, savings,
or Czech-language quality was measured. `live-attempt.json` uses null metrics.

Local full logs (not committed; existing suites can log unrelated fixtures):
`/tmp/crewship-jev-go-test.log`, `/tmp/crewship-jev-cli-retest.log`,
`/tmp/crewship-jev-large-retest.log`, `/tmp/crewship-jev-go-vet.log`.
