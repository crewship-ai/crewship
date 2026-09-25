# Dashboard first slice — dev3 handoff (2026-09-25)

PR #2697 (issue #2696) is a draft intentionally kept open for more frontend
clarity work. The dashboard implementation is on `feat/dashboard-live-work`.
The HTML wireframe is in `docs/prd/wireframes/dashboard-live-work-2026-09-25.html`.

## What shipped to dev3

- Results & review combines active agent and routine runs, in-progress and
  review issues, and recent completed issues/routines. Filters and a bounded
  scroll area keep the main panel useful without stretching the page.
- Needs your attention links to review issues instead of credential tool gaps.
  Credential gaps remain in crew status and System details.
- Your crews shows all ten current crews in a scroll area. Up next uses local
  day/time and links to the existing routine calendar. Run-volume bars use
  crew colours, including the legacy `sky` code observed on Coolify.

The public dev3 service is `crewship-ws@3` at
`https://crewship-dev3.unifylab.cz/`. It runs the prebuilt
`/srv/crewship/dev3-pages-release/crewship.unsigned` binary, not the current
`crewship_3` checkout. Its source is local deploy branch
`deploy/dev3-dashboard-20260925`, commit `264571fb1`: the current dev3
unsigned-webhook profile based on main, plus the dashboard code. This deploy
branch is an artifact and is not the PR branch.

The pre-deploy SQLite online backup passed `PRAGMA quick_check=ok`:
`/srv/crewship/dev3-pages-release/backups/dashboard-20260925T104951Z/`.
That directory also holds the original server/sidecar binaries and the two
intermediate dashboard server binaries. No migration ran. The sidecar binary
was not replaced; the server was built with its installed sidecar hash.

## Verification

- The deploy branch passed `pnpm build`, static export embedding and Go
  binary build. The PR branch passed TypeScript, lint (no new errors), targeted
  dashboard tests and `go vet ./...`.
- After the final restart, local and public URLs returned HTTP 200, systemd
  reported active and the error-priority journal had no entries.
- Authenticated Chromium checks at 1440 px and 390 px loaded the dashboard
  without page errors. They found eight finished rows and all ten crews;
  Coolify's chart swatch resolved to `rgb(14, 165, 233)`.
- Full `go test ./... -count=1 -p 4 -timeout 35m` was still running when this
  handoff was written; check its final result before considering this slice
  verified. PR CI and CodeRabbit review were also pending. Do not merge the
  draft PR while review is absent.
