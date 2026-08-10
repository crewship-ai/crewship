# WP-21 Go TODO/FIXME triage

Measured on `origin/main` at `0ff175669c445c9e13671983ffa7cb5bace9a4a2`.
The work-order inventory is line based:

```bash
grep -rn "TODO\|FIXME" --include='*.go' internal cmd | wc -l
# 132
```

The broad inventory is intentionally kept as the accounting denominator. It
matches issue status values, examples, fixtures, generated schemas, calls such
as `context.TODO()`, and explanatory prose in addition to engineering-debt
comments. A word-boundary search returns 129 because the original grep also
matches substrings; neither number alone identifies debt.

The actionable syntax for this pass is a Go comment containing `TODO:`,
`FIXME:`, `TODO(label):`, or `FIXME(label):`. The AST-based gate in
`internal/todogate/todo_gate_test.go` requires each such comment to use an issue
reference (`TODO(#123):` or `FIXME(#123):`) and reports its file and line. It
does not classify string literals, generated data, status values, or prose that
merely discusses the word TODO as debt.

## Complete accounting

| Raw lines | Classification | Verdict | Result |
|---:|---|---|---|
| 1 | `internal/api/credential_rotation.go`: sidecar grace fallback | real, worth doing | Reference new #1882. |
| 1 | `internal/api/recurring_issue_dispatcher.go`: duplicate scheduled-fire key | real, worth doing | Closed #820 no longer describes the residual; reference new #1883. |
| 1 | `internal/api/agent_chats.go`: non-local attachment cleanup | real, conditional on storage-provider work | Reference open #1768, which owns the non-local provider decision and implementation. |
| 1 | `cmd/crewship/cmd_integration_tools.go`: empty refresh payload | real, worth doing | Reference new #1884. |
| 128 | Does not match actionable debt-comment syntax: domain/status values, generated schemas, fixtures/templates, `context.TODO()`, and explanatory comments | not engineering-debt markers | Leave unchanged. One explanatory line that pointed readers to a bare TODO was rewritten to point directly to #1882. |
| **132** | **Total before triage** |  | **All raw inventory lines accounted for.** |

No actionable marker was classified as already completed or not worth doing.
No runtime behavior is changed by this triage.

The two exhaustive buckets are reproducible with:

```bash
grep -rn "TODO\|FIXME" --include='*.go' internal cmd > /tmp/wp21-all.txt
grep -nE '^[^:]+:[0-9]+:[[:space:]]*//[[:space:]]*(TODO|FIXME)(\([^)]*\))?:' \
  /tmp/wp21-all.txt > /tmp/wp21-debt.txt
wc -l /tmp/wp21-all.txt /tmp/wp21-debt.txt
# before: 132 total, 4 actionable debt comments, therefore 128 non-debt lines
```

The gate is deliberately stronger than these counts: it parses Go comments and
fails with the exact location and text of an unreferenced marker.
