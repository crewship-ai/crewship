# Release 1.0 test-readiness status

Status verified on 2026-08-06 against the `test/pr-gates` branch and its
ancestors. This is the evidence index for the remaining audit findings; it is
not a coverage target.

## Audit defects D1–D8

| Defect | Status | Gate / evidence |
|---|---|---|
| D1 — in-band agent failure recorded as completed | Closed | `internal/orchestrator/orchestrator_run_inband_error_test.go`, `TestRunAgent_InBandResultError_MarksRunFailed` drives exit 0 plus `is_error`. |
| D2 — foreign workspace references on writes/reads | Closed for known paths | `internal/api/cross_workspace_fence_matrix_test.go`, `internal/api/cross_workspace_reference_test.go`, and `assignee_write_invariant_test.go` exercise the real router and scan every assignee writer. |
| D3 — `crew_id` injection on agent creation | Closed | Foreign-reference matrix includes `POST /api/v1/agents` with a victim crew id. |
| D4 — leader/ownership gates | Closed for landed scheduler/mission gates | Existing leader and scheduler tests include the non-leader no-op case. Any new gate must still be added to the relevant route/handler invariant. |
| D5 — delegation limits and execution settings (`temperature`, `max_tokens`) | Open product/implementation decision | Existing tests identify persisted-but-unconsumed fields; enforcement/removal is intentionally not chosen in this PR. |
| D6 — internal `/spawn` LEAD policy | Open product decision | Current internal-hire path and autonomy policy are covered, but the desired strict LEAD-only contract is not chosen here. |
| D7 — backup/memory blob isolation | Closed for the audited path | Existing backup and memory portability cross-workspace tests cover the tenant boundary. |
| D8 — journal integrity model | Partly closed, decision open | Hash-chain and signed-compaction tests provide tamper evidence. Strict append-only semantics versus tamper-evident semantics remains a product decision. |

## Explicitly unresolved flagship question

`learned-*.md` is written by consolidation but is not read into the agent boot
prompt or exposed through the memory tool. The sentinel in
`internal/orchestrator/learned_rules_not_delivered_test.go` keeps this gap
visible. It must become a product issue: either deliver the file or document
that the feature is not part of the 1.0 contract. This PR does not silently
choose either behaviour.

## Test-first evidence

The new shell gates have negative tests in the repository. The existing Go
security tests above are adversarial: they seed two tenants, mint credentials,
drive `Router.ServeHTTP`, and assert the foreign marker cannot be read or
persisted. The in-band test uses an adapter stream with `is_error: true` and a
zero process exit code. Breakage transcripts belong in the PR description when
the branch is opened.

