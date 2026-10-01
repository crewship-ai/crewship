<!-- Moved from CONTRIBUTING.md in the 2026-09-28 repository-clarity
     reorganisation; the entrypoint keeps the rule, this file keeps the
     full explanation. Index: docs/development/README.md -->

## The `web/out` embed

`web/embed.go` does `//go:embed all:out`, so **`web/out/` must exist at
compile time or nothing in the repo builds** — not the package you
changed, everything. `web/out/` is a gitignored build artifact, so a
fresh clone or `git worktree add` has nothing to embed and every
`go build` / `go test` dies with:

```
web/embed.go:19:12: pattern all:out: no matching files found
```

That error names `web/embed.go`, a file you did not touch, and says
nothing about a missing directory — it has been misread as "my change
doesn't compile" more than once.

**It is fixed, and you should not need to do anything.** One file,
`web/out/.placeholder.html`, is tracked in git precisely to keep the
directory non-empty (see the `.gitignore` block around it). `go build`
and `go test` work in any checkout, worktree included, with no setup.

What you get from that build is a binary with **no UI**: every UI route
answers `503` with a page saying the web UI was not built and naming the
command to run, and `crewship start` logs a warning at boot. Build the
real thing with `make build` (or just run `./dev.sh start`).

Two things to not do:

- **Don't hand-roll a stub** (`echo '<!doctype html>' > web/out/index.html`).
  It satisfies the embed and then serves a blank `200`, which looks like
  a broken frontend instead of a skipped build step.
- **Don't `git add -A` after a build that wiped `web/out/`.** Use
  `scripts/embed-web-out.sh sync` (what `make build` and `dev.sh` call) —
  it preserves the tracked placeholder, so the tree stays clean. Deleting
  the placeholder re-breaks the build for every fresh clone; CI fails with
  a named error if it goes missing.

A release cannot ship a UI-less binary: `scripts/embed-web-out.sh verify`
runs on the release, nightly, image and Binary Build paths and fails
unless `web/out/` holds a real Next.js export (`index.html` **and**
`_next/`).
