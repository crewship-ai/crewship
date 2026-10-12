# Host filesystem roots

Run `go run ./scripts/host-paths` from the repository root. This scans production
Go files in `internal/` and `cmd/` for direct `os.UserHomeDir`, `os.TempDir`,
`os.Getwd` calls, including import aliases, and literals rooted at `/tmp`,
`/var/lib/crewship` or `/var/log/crewship`. Tests and testdata are excluded.
The central server path resolver and installation root selector are permitted.

`allowlist.json` records existing sites, including client configuration,
container-side paths, OS integration and server paths awaiting migration. It is
an inventory of exceptions, not proof that all current writes are isolated.
New occurrences fail; removed occurrences require deleting their stale entry.
Keys use the containing function and syntax rather than source line numbers.

CI runs with `-base <PR-target-SHA>` and refuses allowlist additions. When the
guard is first introduced, allowed syntax must already exist in that revision.
CODEOWNERS identifies the maintainer, but the ratchet does not depend on branch
protection requiring their review. Local verification without `-base` checks
the current tree; pass a base explicitly to check the ratchet as well.

This check does not inspect every write or derive the meaning of arbitrary
absolute paths. Runtime tests and Docker mount/ownership checks are separate
verification boundaries. Filesystem lookups for Docker sockets and `/tmp`
inside containers remain separately documented exceptions.
