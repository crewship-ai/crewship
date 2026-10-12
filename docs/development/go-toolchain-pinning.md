<!-- Moved from CONTRIBUTING.md in the 2026-09-28 repository-clarity
     reorganisation; the entrypoint keeps the rule, this file keeps the
     full explanation. Index: docs/development/README.md -->

## Bumping the Go toolchain (never a one-line change)

Thirteen files name the Go version, and they must all name the same one:

| Where | Line |
|---|---|
| `go.mod` | `toolchain go1.27.2` — the anchor everything else is checked against |
| `Dockerfile` | `FROM golang:1.27.2-alpine` — the compiler for the **shipped** binary |
| ten workflows | `GO_VERSION: "1.27.2"` |
| `.github/workflows/codeql.yml` | a literal `go-version: "1.27.2"` |

`scripts/go-toolchain-pin.sh` parses all of them and fails on disagreement;
it runs in CI's `Shell` job on every PR. Run it before you push:

```bash
bash scripts/go-toolchain-pin.sh
```

Two things it deliberately does **not** do, both worth knowing:

- **It ignores `go.mod`'s `go` directive.** The language floor is a promise
  to consumers and is not the same decision as which compiler builds the
  release (#2060), so it does not ride along with a toolchain bump. It moves
  only when a dependency forces it — which is why it reads 1.27 today:
  shoutrrr v0.19.0 declares `go 1.27`, and the go command refuses a main
  module whose floor is below its dependencies'.
- **It does not build the image.** The root `Dockerfile` is built by
  `release.yml` and `nightly.yml` only, so image-only breakage (a missing
  `COPY`, a `pnpm prisma generate` regression) is still first caught by
  nightly. That gap is #2064 and is open.

The Dockerfile's `ENV GOTOOLCHAIN=local` is what stops the image from
downloading a different compiler than its `FROM` tag names — the default,
`auto`, would fetch whatever `go.mod`'s `toolchain` line asks for. Leave it as
`local`; a version literal there would be a fourteenth copy of the string, and
it fails backwards — bump the `FROM` tag, forget the literal, and Go downloads
the *old* toolchain and undoes the bump with CI still green.

Do not expect the image build to catch drift for you. Under `local` the
`toolchain` directive is ignored outright, so `toolchain go1.27.3` against a
`golang:1.27.2-alpine` base builds silently with 1.27.2 and exits 0. Only the
`go` directive can fail a build, and it tracks the language floor rather than
this pin. The static check is the only thing that sees this.

Bumping the toolchain also means **re-checking the analyser pins**.
`golangci-lint` and `govulncheck` each vendor `golang.org/x/tools`, and an
`x/tools` older than the standard library it is asked to analyse dies on
syntax it does not know — 1.26 → 1.27 needed both to move. Their pins carry
comments saying so; the guard cannot check this one for you.
