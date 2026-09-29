<!-- Moved from CONTRIBUTING.md in the 2026-09-28 repository-clarity
     reorganisation; the entrypoint keeps the rule, this file keeps the
     full explanation. Index: docs/development/README.md -->

## Build identity in a worktree (`go build` gets it wrong)

A binary built by a bare `go build` inside `.claude/worktrees/<name>/`
reports the **parent clone's** commit and dirty flag — silently, with
nothing in the output to say so. `crewship version` will happily print a
commit that was never built and "(uncommitted changes)" for a tree with
none (#1686).

It is a Go toolchain limitation, not a repo bug: `cmd/go` recognises a
repository by a `.git` **directory**, and a linked worktree's `.git` is a
**file**, so the search walks up to the enclosing clone and stamps
`vcs.revision` / `vcs.time` / `vcs.modified` from there.

`git` itself honours the `.git` file, so the fix is to ask git and stamp
the answer. `scripts/build-stamp.sh` is the one place that does:

```bash
scripts/build-stamp.sh dirty            # true | false | "" (not a repo)
scripts/build-stamp.sh commit           # full SHA of the tree you are in
go build -ldflags "$(scripts/build-stamp.sh ldflags)" ./cmd/crewship
```

**`make build` and `./dev.sh` already route through it**, so anything they
produce is correct — including every dev slot, where the reported commit
is the only way to tell a stale deploy from a stale CLI. Deploying a slot
from a hand-rolled `go build` re-opens the hole. Never emit a confident
`false` for the dirty bit when it is unknown: `buildinfo` models "nobody
stamped this" as a third state precisely so it is not flattened into
"clean".
