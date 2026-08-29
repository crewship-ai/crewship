# Release 1.0 reports

Two kinds of file live here, and they are not maintained the same way.

**Authored.** `release-1-0-test-readiness.md` and `wp21-go-todo-triage.md` are
written by hand and reviewed like any other document.

**Generated, and deliberately not checked in.**
`release-1-0-api-cli-inventory.json` and `release-1-0-api-cli-inventory.md` are
rewritten from the router table and the cobra command tree every time
`docs-inventory` runs. Write them with:

```
make docs-inventory
```

They used to be committed. Every pull request that touched a command changed
them, so they conflicted with every other such pull request — and resolving
that conflict always meant discarding both sides and re-running the generator,
which is what a build artifact is. CI still regenerates and gates on them
(`go run ./scripts/docs-inventory -strict`, the "API and CLI documentation is
complete" step), so the invariants are checked on every pull request; the
output is simply no longer stored.
