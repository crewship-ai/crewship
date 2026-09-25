# Dashboard — dev3 handoff (2026-09-25)

Draft PR #2697 tracks issue #2696 and remains open for further frontend
clarity work. Implementation: `feat/dashboard-live-work`. The initial HTML
wireframe is `docs/prd/wireframes/dashboard-live-work-2026-09-25.html`; its
System details sketch is superseded by the live implementation.

## Current dashboard

- Results & review combines active agent and routine runs, in-progress and
  review issues, and recent completed work. Filters and bounded scrolling keep
  the panel compact.
- Needs your attention links to approvals, run alerts and schedule alerts.
  Up next links to the routine calendar, and Your crews scrolls through all
  crews. Run-volume bars use each configured crew colour.
- The four agent run summary cards share one height and text layout.
- The sticky top bar contains only the 24h / 7d / 30d selector. The visible
  Dashboard title, crew and agent counts, Live badge, New issue and Chat with
  agent buttons, and the entire System details panel were removed after review.

## Deployment

Dev3: `https://crewship-dev3.unifylab.cz/`, systemd service `crewship-ws@3`.
The service uses the prebuilt
`/srv/crewship/dev3-pages-release/crewship.unsigned` binary. Its source is
local deploy branch `deploy/dev3-dashboard-20260925`, commit `625dbd006`;
the matching PR commit is `37ca54344`. This deploy branch is an artifact,
not the PR branch. The server build uses the installed sidecar hash
`33ad18f2afb3`; the sidecar binary was not replaced.

The previous binary is backed up at
`/srv/crewship/dev3-pages-release/backups/dashboard-20260925T1350Z/`.
The earlier online SQLite backup before the host-sample migration passed
`PRAGMA quick_check=ok` and remains in
`/srv/crewship/dev3-pages-release/backups/dashboard-20260925T1224Z/`.

## Monitoring direction

`/metrics` continues to expose Crewship process and domain metrics and now
includes whole-host CPU and RAM gauges plus a last-sample timestamp. The host
sampler refreshes in memory about every 15 seconds. Prometheus should own
history, Grafana can visualize it, and host/container exporters should provide
machine and Docker metrics. The dashboard does not request host data. The
sampler no longer writes SQLite history; the already applied
`host_resource_samples` migration remains for compatibility.

## Verification

- PR branch: `pnpm lint`, `pnpm test:types`, `pnpm build`, focused Go tests,
  `go vet ./...`, docs inventory/surface checks and migration lint passed.
  A full `go test ./... -count=1 -p 4 -timeout 35m` is running on the shared
  host as of this handoff.
- Deploy branch: `pnpm build`, static export embedding and `make build:go`
  passed. The public and local URLs returned HTTP 200, systemd was active,
  and the error-priority journal had no entries after startup.
- Authenticated Chromium at 1440 px and 390 px found only the three reporting
  controls in the sticky bar, no System details, no page errors, no horizontal
  overflow, and no obsolete host-resource API requests. Switching to 7d worked;
  the bar stayed at y=44 px after scrolling at both widths.
- Local `/metrics` exposed the host CPU, memory and sample timestamp gauges.
  The old SQLite history stayed at 79 rows more than a minute after restart,
  confirming that the sampler stopped writing.

Do not merge before an actual CodeRabbit review; a passing but rate-limited
check does not count as one.
