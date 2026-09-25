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
- The sticky dashboard bar contains only the 24h/7d/30d selector. The visible
  Dashboard title, crew/agent counts, Live badge, New issue and Chat with agent
  actions, and the entire System details panel were removed after review.
- `/metrics` still exposes whole-host CPU and RAM gauges, sampled in memory
  about every 15 seconds. Prometheus owns the history. The dashboard no longer
  requests host data, and the sampler no longer writes to SQLite. The already
  applied `host_resource_samples` migration remains for deployed databases.

The public dev3 service is `crewship-ws@3` at
`https://crewship-dev3.unifylab.cz/`. It runs the prebuilt
`/srv/crewship/dev3-pages-release/crewship.unsigned` binary, not the current
`crewship_3` checkout. Its source is local deploy branch
`deploy/dev3-dashboard-20260925`, currently at commit `506a0c5af` before the
final simplified-header deploy: the dev3
unsigned-webhook profile based on main, plus the dashboard code. The earlier
`264571fb1` was the initial dashboard slice. This deploy branch is an artifact
and is not the PR branch.

The original dashboard backup is
`/srv/crewship/dev3-pages-release/backups/dashboard-20260925T104951Z/`.
Before the host-sample migration, an additional SQLite online backup passed
`PRAGMA quick_check=ok` in
`/srv/crewship/dev3-pages-release/backups/dashboard-20260925T1224Z/`.
That directory also holds the previous server binaries for rollback. The
host-sample migration applied on dev3. The sidecar binary was not replaced;
the server was built with its installed sidecar hash.

## Monitoring direction

- Keep `/metrics` as Crewship's Prometheus scrape surface for process,
  orchestration, queue, run and cost metrics. Host gauges carry a last-sample
  timestamp for stale-data alerts.
- Use Prometheus for historical host data and Grafana for operational
  dashboards. The previous SQLite minute sampler has stopped.
- Use a host exporter for machine and disk metrics and a container exporter for
  Docker-level CPU, memory, network and filesystem metrics. Crewship should
  report application semantics and sidecar outcomes through its own `/metrics`
  endpoint. Avoid crew/user identifiers as unbounded Prometheus labels.

## Verification

- The deploy branch passed `pnpm build`, static export embedding and Go
  binary build. The PR branch passed TypeScript, lint (no new errors), targeted
  dashboard tests and `go vet ./...`.
- After the final restart, the public URL returned HTTP 200, systemd reported
  active and the error-priority journal had no entries.
- Authenticated Chromium checks at 1440 px and 390 px loaded the dashboard
  without page errors. They found eight finished rows and all ten crews;
  Coolify's chart swatch resolved to `rgb(14, 165, 233)`.
- On the earlier build, authenticated Chromium showed four equal-height KPI
  cards, no horizontal mobile overflow and no page errors.
- On deployed build `883047cc6`, authenticated Chromium returned HTTP 200 from
  `/api/v1/system/resources?window=7d`, reported live CPU 23.4% and RAM 11.5%,
  showed the server-load panel without page errors, and kept the 7d control at
  y=49 px after scrolling. Build `856ce9133` adds the OpenAPI response-field
  contract fix; the endpoint data shape is unchanged. Its authenticated page
  shows Sep 25 as the current 7d chart endpoint and the same sticky control.
- On deployed build `506a0c5af`, authenticated Chromium found no Workspace
  status panel, showed Server load and its 15-second hint without page errors,
  and both the lightweight live API and history API returned HTTP 200. A local
  Prometheus scrape contained host CPU, RAM and sample timestamp series. The
  public URL returned HTTP 200, systemd was active, and the error-priority
  journal was empty.
- Full `go test ./... -count=1 -p 4 -timeout 35m` passed for the initial slice
  (the API and database packages took about 27 and 35 minutes on the shared
  host). A fresh full Go run found missing OpenAPI `required`, read-route scope,
  backup classification, and published spec-count declarations. They were
  added and their focused tests passed. The later full local run hit its
  35-minute timeout in the database suite on the shared host. The latest
  host-resource API and Prometheus tests, `go vet ./...`, `pnpm build`, lint,
  TypeScript, and `go run ./scripts/docs-inventory -strict` passed. CodeRabbit
  is rate limited on the draft; the PR must receive an actual review before merge.
