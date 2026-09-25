# Dashboard — dev3 handoff (2026-09-25)

PR #2697 (issue #2696) is a draft intentionally kept open for more frontend
clarity work. The dashboard implementation is on `feat/dashboard-live-work`.
The HTML wireframe is in `docs/prd/wireframes/dashboard-live-work-2026-09-25.html`.

## What shipped to dev3

- Results & review combines active agent and routine runs, in-progress and
  review issues, and recent completed issues/routines. Filters and a bounded
  scroll area keep the main panel useful without stretching the page.
- Needs your attention links to review issues instead of credential tool gaps.
  Crew setup alerts remain visible through crew health.
- Your crews shows all ten current crews in a scroll area. Up next uses local
  day/time and links to the existing routine calendar. Run-volume bars use
  crew colours, including the legacy `sky` code observed on Coolify.
- The four agent run summary cards share one height and text layout. The lone
  run-volume sparkline was removed because the other metrics have no comparable
  time series; the actual run-volume chart remains below.
- System details now summarizes new run capacity, crew health, agents and
  scheduled routines. Crew health counts agent and service failures, not
  credential/tool gaps; it does not claim no failures if service checks are
  unavailable. Agent status calls out unavailable agents. Rows link to the
  relevant crew, agent or routine view; capacity is informational when no
  work is held.
- The dashboard subbar and its 24h/7d/30d controls stay visible while the
  content scrolls. On phones, the selector has its own compact row in that
  sticky bar.
- Server load shows measured host CPU and RAM gauges and a history chart using
  the same reporting window. A server-side sampler records one reading per
  minute and keeps 30 days. This is host load, including processes outside
  Crewship; it is not per-crew usage. Earlier host history cannot be recovered
  because the previous release did not store it. Missing periods stay empty.

The public dev3 service is `crewship-ws@3` at
`https://crewship-dev3.unifylab.cz/`. It runs the prebuilt
`/srv/crewship/dev3-pages-release/crewship.unsigned` binary, not the current
`crewship_3` checkout. Its source is local deploy branch
`deploy/dev3-dashboard-20260925`, commit `12f39cbd7`: the dev3
unsigned-webhook profile based on main, plus the dashboard code. The earlier
`264571fb1` was the initial dashboard slice. This deploy branch is an artifact
and is not the PR branch.

The pre-deploy SQLite online backup passed `PRAGMA quick_check=ok`:
`/srv/crewship/dev3-pages-release/backups/dashboard-20260925T104951Z/`.
That directory also holds the original server/sidecar binaries and the two
intermediate dashboard server binaries. No migration ran. The sidecar binary
was not replaced; the server was built with its installed sidecar hash.
The second dashboard rollout has its prior server binaries in
`/srv/crewship/dev3-pages-release/backups/dashboard-20260925T1200Z/`.

## Verification

- The deploy branch passed `pnpm build`, static export embedding and Go
  binary build. The PR branch passed TypeScript, lint (no new errors), targeted
  dashboard tests and `go vet ./...`.
- After the final restart, local and public URLs returned HTTP 200, systemd
  reported active and the error-priority journal had no entries.
- Authenticated Chromium checks at 1440 px and 390 px loaded the dashboard
  without page errors. They found eight finished rows and all ten crews;
  Coolify's chart swatch resolved to `rgb(14, 165, 233)`.
- On the current build, authenticated Chromium showed four equal-height KPI
  cards, four crew failures, one unavailable agent, no horizontal mobile
  overflow and no page errors. The public URL returned HTTP 200.
- Full `go test ./... -count=1 -p 4 -timeout 35m` passed for the initial slice
  (the API and database packages took about 27 and 35 minutes on the shared
  host). A fresh full Go run is in progress for this follow-up. `pnpm
  test:types` passed. CodeRabbit is rate limited on the draft; the PR must
  receive an actual review before merge.
