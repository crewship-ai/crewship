# Releasing Crewship

Internal runbook for cutting a release. The pipeline is fully automated —
this document explains *what* the steps do, *what* to check, and *how*
to recover when something goes wrong.

## Versioning

Crewship uses [Semantic Versioning](https://semver.org/). Pre-1.0
releases (`0.x.y`) treat **minor bumps as the breaking-change boundary**;
patch releases are backwards-compatible fixes only.

Pre-release suffixes follow semver: `-beta.1`, `-beta.2`, `-rc.1`. The
goreleaser config has `prerelease: auto`, so a tag with a suffix lands
as a GitHub pre-release; a clean `vX.Y.Z` tag becomes a stable release.

## Pre-flight checklist

Before tagging, verify:

- [ ] `main` is green: `go test ./... -count=1 && go vet ./...` and
      `pnpm lint && pnpm build && pnpm test --run`.
- [ ] `gofmt -l internal/ cmd/` is empty for files touched in the
      release window.
- [ ] `CHANGELOG.md` has a populated `## [X.Y.Z]` section under
      `## [Unreleased]`. Move the entries down, leave `## [Unreleased]`
      empty.
- [ ] `package.json` `version` matches the target tag (without the `v`
      prefix; `0.1.0-beta.1` for tag `v0.1.0-beta.1`).
- [ ] No unresolved CodeRabbit major comments on the release PR.
- [ ] Goreleaser dry-run passes:
      `goreleaser release --snapshot --clean --skip=publish,sign`.
- [ ] The `release` branch is at most a few commits behind `main`
      (`git log --oneline origin/release..origin/main`). The dogfood
      prod VM tracks `release` and a stale branch means you ship from
      old code.

## Cutting the tag

```bash
# 1. Confirm you're on main and synced
git checkout main
git pull --ff-only origin main

# 2. Tag (signed if you have GPG configured, plain otherwise)
git tag -a v0.1.0-beta.1 -m "v0.1.0-beta.1: first public beta"
git push origin v0.1.0-beta.1
```

The push triggers `.github/workflows/release.yml`. Watch the run at
`https://github.com/crewship-ai/crewship/actions`. Expected duration
~10 minutes:

1. Checkout, install pnpm + Node + Go.
2. `pnpm install` and `pnpm build` to produce `out/`.
3. `cp -r out web/out` so the Go embed FS picks it up.
4. `goreleaser release --clean` cross-compiles for darwin/linux/windows
   (amd64 + arm64 where supported), signs each binary with cosign
   keyless via GitHub OIDC, generates SPDX + CycloneDX SBOMs, builds
   archives, and uploads everything to a GitHub Release.
5. Homebrew formula is auto-pushed to `crewship-ai/homebrew-tap`.

## After the release lands

- [ ] Install the binary from the release artifact and run
      `crewship version` — confirm the tag, commit SHA, and build date
      look right.
- [ ] `brew tap crewship-ai/tap && brew install crewship` on a clean
      Mac and run `crewship version` — same check, plus the formula
      published cleanly.
- [ ] Promote the GitHub Release notes (auto-generated from the
      changelog block + commit history) — re-edit if anything reads
      poorly.
- [ ] Bump `## [Unreleased]` in `CHANGELOG.md` to the next planned
      version's heading, push the change.
- [ ] Sync `release` branch from `main` so the dogfood prod VM rolls
      forward: `git push origin main:release`.
- [ ] Announce: short post in the discussion forum + Discord/community
      channel; mention any breaking changes prominently.

## Recovery

**Tag pushed too early / wrong commit.** Delete the remote tag, fix the
issue, retag the right commit:

```bash
git push --delete origin v0.1.0-beta.1
git tag -d v0.1.0-beta.1
# Fix things, then retag from the corrected SHA
```

If goreleaser already produced a GitHub Release for the bad tag, delete
it via the UI before retagging — otherwise the next run errors on the
existing draft.

**Goreleaser fails mid-flight.** Re-run the workflow from the Actions
tab. The build is reproducible (`-trimpath`, fixed `GOFLAGS`), so a
re-run from the same commit produces identical bytes; cosign keyless
signing is the only step that's not perfectly idempotent (it generates
a fresh certificate against the same identity), but that's fine.

**Homebrew formula push fails (403 / permissions).** Check that the
`HOMEBREW_TAP_TOKEN` repo secret has not expired. Goreleaser writes
the formula to a separate repo, so the main release is unaffected —
re-run only the Homebrew step manually if needed.

**Binary doesn't run on a user's machine.** Common causes:

- *macOS Gatekeeper* — binaries are signed but not notarized in v0.1;
  users must `xattr -d com.apple.quarantine ./crewship` after download.
  Documented in the Quick Start.
- *Linux missing glibc* — Crewship is built `CGO_ENABLED=0` so a static
  binary works on any modern distro. If the user is on
  Alpine + musl, the static build still runs.

## Cadence

Target cadence in pre-1.0:

- **Beta minor (`v0.x.0-beta.1`)** — every 4–6 weeks while the API
  surface is still moving.
- **Beta patch (`v0.x.y-beta.N`)** — within 1 week of finding a P0/P1.
- **RC (`v0.x.0-rc.1`)** — once no open P0/P1 for 7 days.
- **Stable (`v0.x.0`)** — promote RC after 7-day soak.

## Stability tiers (per-feature, in addition to the version)

Inside a release, individual features carry one of three labels in
their docs:

- **stable** — frozen surface; only backwards-compatible changes in
  patches.
- **beta** — usable, but the contract may shift in minor bumps.
- **experimental** — research preview; do not build production
  workflows on it.

The Crew Journal, Paymaster, Lookout, Harbormaster, and Backup ship
as **stable** in v0.1.0-beta.1. Episodic memory and Consolidate ship as
**experimental** (packages exist, auto-wiring lands in v0.2). The full
matrix is in [docs/production-checklist.mdx](docs/production-checklist.mdx).
