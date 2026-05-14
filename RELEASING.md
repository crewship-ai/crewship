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

## Hotfix workflow

When a P0 / data-loss bug lands in a released version, ship a patch
without waiting for the next planned release.

```bash
# 1. Branch from the existing release tag (NOT main — main may have
#    incompatible changes that you don't want in the hotfix).
git checkout -b hotfix/v0.1.1 v0.1.0

# 2. Cherry-pick the fix from main, or write the minimal fix directly
#    on this branch. Keep scope tight — a hotfix is "patch the bug",
#    not "while we're here, also...".
git cherry-pick <sha-of-fix-on-main>

# 3. Tag and push. The release workflow fires off this tag exactly
#    like a normal release.
git tag -a v0.1.1 -m "v0.1.1: hotfix for <one-line summary>"
git push origin hotfix/v0.1.1 v0.1.1
```

Criteria for a hotfix (otherwise wait for the next regular release):

- Data loss, data corruption, or silent data divergence.
- Auth/authorisation bypass.
- Security vulnerability (CVE-grade or equivalent).
- Server fails to start on a version's own DB after a clean install.

After shipping, forward-port the fix to `main` if it wasn't cherry-picked
from there to begin with — otherwise the next regular release ships
the bug again.

**If the hotfix itself breaks something:** retag `v0.1.2` with a
fix-of-the-hotfix. Don't try to "untag" v0.1.1 — once a tag is in
the public release feed, customers have already pulled it. The fix-
forward path is always cheaper than the rollback path.

## Distribution channels

Three concurrent channels serve different audiences:

| Channel | Trigger | Docker tag | Binary | Use case |
|---|---|---|---|---|
| **stable** | clean semver tag (`v0.1.0`) | `:vX.Y.Z`, `:vX.Y`, `:latest` | GitHub Release, Homebrew | Production / default `brew install crewship` |
| **beta** | pre-release tag (`v0.1.0-beta.1`) | `:vX.Y.Z-beta.N`, `:vX.Y` | GitHub Pre-release | Opt-in beta testers (`brew install crewship@0.1.0-beta.1` or `docker pull :vX.Y.Z-beta.N`) |
| **nightly** | every push to `main` | `:nightly`, `:main-<sha>` | Rolling `nightly` GH pre-release | Internal CI, brave testers wanting trunk |

The `:latest` Docker tag only moves on clean semver tags — pre-releases
must never overwrite `:latest`, or `docker pull crewship` would silently
hand beta to users expecting stable. See `.github/workflows/release.yml`
for the gating logic.

## Migration safety

Migrations are the highest-risk part of every release — they touch
production data and a bad one is hard to undo. Guardrails:

1. **`migration-lint` CI workflow** runs on every PR touching
   `internal/database/migrate.go`. Enforces append-only ordering
   (versions strictly increase, no rename of a version already in
   `main`). The Go test counterpart (`migrate_lint_test.go`) catches
   the same class of mistake locally.

2. **Auto-snapshot before apply** — `database.SnapshotBeforeMigrate`
   takes a `VACUUM INTO` copy of the live DB as
   `<dbpath>.pre-migrate-vN-to-vM-<UTC>.bak` whenever any migration is
   pending. Last 10 snapshots are retained per database; opt out with
   `CREWSHIP_SKIP_MIGRATION_BACKUP=1`.

3. **Forward-only schema changes**. Never `DROP COLUMN` in the same
   release that stops reading it: ship "stop reading" in vX, then
   `DROP COLUMN` in vX+1. The previous-release client must remain
   compatible with the next-release schema for at least one minor
   bump.

4. **Restore-from-backup tests** in `internal/backup/` exercise the
   `restoreBackfill` hook chain — when a customer restores an older
   bundle into a newer schema, every migration between source and
   target gets a chance to populate any new columns.

## Telemetry (crash reporting) — Sentry setup

Crewship ships with opt-in Sentry-backed crash reporting wired through
`internal/crashreport`. The binary is a no-op until BOTH a DSN is baked
in via `-X .../crashreport.DSN=...` AND the operator's `telemetry_opt_in`
row in `app_settings` is `"1"`.

### One-time project setup

1. Create a Sentry project (Platform: Go). Note its DSN.
2. Add `SENTRY_DSN` to the repo's GitHub Actions secrets. The
   `release.yml` and `nightly.yml` workflows pass it as a build-arg to
   goreleaser and the Docker image build. Local `go build` and PRs
   from forks leave the DSN empty, so they ship telemetry-disabled.
3. **Configure server-side data-scrubbing rules in the Sentry UI.**
   The client-side BeforeSend hook in `sentry_adapter.go` scrubs
   request headers, query strings, request bodies, the User field,
   and several context maps. It cannot reliably scrub free-form
   strings that *we* generate, e.g.
   `fmt.Errorf("auth failed for %s", userEmail)`. The only sound
   defense for that class of leak is regex-based scrubbing at the
   Sentry server.

   Project Settings → Security & Privacy → Data Scrubbing → add:
   - `@email-pattern`     — emails in messages, breadcrumbs, exception values
   - `@password-pattern`  — Sentry built-in
   - `@creditcard-pattern` — Sentry built-in
   - Custom rule: `[Mask] [Message] [^Bearer\s+\S+]` — bearer tokens
   - Custom rule: `[Mask] [Message] [^sk-[A-Za-z0-9]{20,}]` — OpenAI/Anthropic-style keys

   These rules run inside Sentry before the event is persisted; if
   the regex matches, the matched substring is replaced with
   `[Filtered]`. Verify by raising a test error containing the
   pattern and checking the resulting event in the UI.

### What gets sent

Stack traces, exception messages (subject to server-side scrubbing
above), Crewship version + commit, OS name, an anonymous install ID
(random 32-hex, generated on first opt-in, stable across
opt-out/opt-in cycles).

### What is never sent

Workspace data, credential values, request bodies, Authorization
or Cookie headers, query-string secrets, environment variables, the
user's hostname (`ServerName` is overridden with the anonymous
install ID), the Go module list, or any of the runtime/device/culture
contexts that sentry-go's default integrations would normally attach.
See `internal/crashreport/sentry_adapter.go::scrubEvent` for the
client-side filter and `crashreport_test.go::TestScrubEvent_DropsLeakyContexts`
for the pinning test.

## Branch protection

`main` is protected; configure via `scripts/setup-branch-protection.sh`
(run once with repo-admin gh credentials). Required checks: Frontend,
Backend, Lint migrations, Security, E2E. One approval needed (use
auto-approve for trivial dep bumps via Renovate). Force-push is
disallowed; linear history is required.

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
