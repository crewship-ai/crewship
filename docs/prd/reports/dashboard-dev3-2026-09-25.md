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
  Each card opens Inbox with its own URL-backed category selected. Categories
  include every active inbox kind counted by the card, including both failed
  runs and tripped schedule circuit breakers in Run alerts. The selected chip
  can be cleared in Inbox.
  Up next links to the routine calendar, and Your crews scrolls through all
  crews. Run-volume bars use each configured crew colour.
- The four agent run summary cards share one height and text layout.
- Dashboard colours now follow a quieter hierarchy: red for failed runs,
  amber for pending decisions, blue for active work and controls, and neutral
  for completed work, schedule icons and summary metrics. Run-volume keeps
  crew colours because they identify chart series.
- On desktop, Results & review and the Up next / Your crews column share one
  height. Results and the crew list scroll within their panels when needed;
  arrows in their headers make additional items discoverable. Narrower screens
  stack the panels at natural height.
- The sticky top bar shows the Dashboard title and 24h / 7d / 30d selector.
  Crew and agent counts, the Live badge, New issue and Chat with agent buttons,
  and the entire System details panel were removed after review.

## Deployment

Dev3: `https://crewship-dev3.unifylab.cz/`, systemd service `crewship-ws@3`.
The service uses the prebuilt
`/srv/crewship/dev3-pages-release/crewship.unsigned` binary. Its source is
local deploy branch `deploy/dev3-dashboard-20260925`, commit `f38f290ff`;
the matching PR colour commit is `645de06b1`. This deploy branch is an artifact,
not the PR branch. The server build uses the installed sidecar hash
`33ad18f2afb3`; the sidecar binary was not replaced.

An earlier dashboard binary is backed up at
`/srv/crewship/dev3-pages-release/backups/dashboard-20260925T1416Z/`.
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

- PR branch: `pnpm lint`, `pnpm test:types`, `pnpm build`, seven focused
  dashboard component tests, earlier focused Go tests, `go vet ./...`, docs
  inventory/surface checks and migration lint passed. The shared host ran out
  of disk space during the full Go suite, causing unrelated package link and
  SQLite test failures. No Go code changed in this frontend increment.
- Deploy branch: `pnpm build`, static export embedding and `make build:go`
  passed. The public and local URLs returned HTTP 200, systemd was active,
  and the error-priority journal had no entries after startup.
- Authenticated Chromium at widths 390, 768, 1024, 1280, 1440 and 1800 px
  found the Dashboard title and reporting controls, no System details, page
  errors, horizontal overflow or obsolete host-resource API requests. At
  desktop widths, Results and Your crews ended at the same y coordinate. The
  list arrows scrolled both panels and disappeared from Results when a filter
  had no overflow. Mobile hid the arrows and kept touch scrolling. Switching
  to 7d worked; the sticky bar stayed at y=44 px after scrolling.
- Local `/metrics` exposed the host CPU, memory and sample timestamp gauges.
  The old SQLite history stayed at 79 rows more than a minute after restart,
  confirming that the sampler stopped writing.
- Inbox attention update: 31 focused Vitest tests, lint, test typecheck,
  frontend build and Go vet passed. The dev3 frontend and embedded Go binary
  built successfully. Authenticated Chromium clicked all three dashboard cards
  and confirmed the matching URL and selected Inbox chip with no page errors.
  Reload preserved the approval filter; removing the chip cleared the URL.
- Colour update: 46 focused dashboard tests, lint, test typecheck and frontend
  build passed. The embedded dev3 binary built and authenticated Chromium
  found neutral finished pills and no page errors after deployment.
- A full `go test ./... -count=1` run returned a failure in the shared test
  environment after about ten minutes. This dashboard increment changes no Go
  code; `go vet ./...` and the production Go build passed.

Do not merge before an actual CodeRabbit review; a passing but rate-limited
check does not count as one.
