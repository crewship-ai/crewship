# Release 1.0 report artifacts

Dated instance audits and acceptance reports were preserved in the versioned
internal archive before removal from the public working tree. The public tree
retains maintained specifications and the regression source fixture needed by
contributors. Historical public versions remain available through Git history.

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
