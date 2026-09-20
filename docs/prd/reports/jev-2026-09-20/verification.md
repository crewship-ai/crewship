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
| Full CLI rerun with `-timeout 25m` | PASS, 500.495s |
| Local API/database rerun with `-timeout 25m` | Stopped after ~18m once equivalent full CI passed; not reported as a local pass. |
| CI full Go suite on `c4e340dcc176c08e1c92ad101277d428404da345` | PASS: actual `go test ./... -count=1 -timeout 15m` step, 08:28:49–08:35:27 UTC. |

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

Pre-commit gitleaks and golangci-lint passed. Draft PR: #2630. CodeRabbit
reported a rate limit rather than a completed review; its green status is not
review evidence. No merge was attempted.

CI evidence: [Go job 106048550848](https://github.com/crewship-ai/crewship/actions/runs/35499448165/job/106048550848).
Go vet, cross-platform builds and Go test steps all executed and concluded
success. Subsequent changes only update these research/verification documents;
the tested implementation is unchanged. Other PR jobs may still be running.
