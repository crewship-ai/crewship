# Operations Pages pilots

These examples use the fixed React/TypeScript profile, existing `status.v1`
panels and declared routines. MySQL/Ansible configuration and execution remain
inside the operations crew; no credentials or inventories enter the Page.

The collectors and the routine DSL are tested locally. Live MySQL connectivity,
a real inventory/playbook and installation-specific routine policy still need
to be verified before enabling a production schedule.

## Prepare the producer

Use an existing `ops` crew. Deliver `scripts/collect_status.py` to
`/crew/shared/scripts/collect_status.py` through the normal Crew `files:` manifest
or `crewship crew files save`. The crew needs Python 3 plus `mysql` or
`ansible-playbook`. Provision credentials with the existing Crewship credential
mechanism, outside the Page source project:

- MySQL: `MYSQL_DEFAULTS_FILE` points to a private client options file in the
  crew. The check uses an authenticated `SELECT 1`, with a 10-second deadline.
- Ansible: `ANSIBLE_INVENTORY_FILE` and `ANSIBLE_PLAYBOOK_FILE` point to trusted
  crew files. The action runs check mode with a 120-second deadline. Check mode
  is not a sandbox for an untrusted playbook; some tasks may be skipped, and
  successful completion is not proof of a successful deployment.

The collector discards tool diagnostics and emits only bounded scalar status.
`--routine-output` returns those fields even on failure, allowing the following
`page.write` step to publish `state: failed`. A successful collector step means
it produced a verdict, not that the monitored service is healthy. Publication
failure fails the routine. The executor supplies workspace, crew and run
identity; no human API token needs to be placed in the crew.

```sh
crewship routine validate examples/pages-apps/mysql-health.routine.yaml
crewship routine save --definition examples/pages-apps/mysql-health.routine.yaml --author-crew ops
# Follow the existing routine review/activation flow, then create the Page.
crewship page create --file examples/pages-apps/mysql-health.page.yaml
```

For Ansible substitute `ansible-status`. Set the schedule independently through
the normal routine trigger workflow after a successful manual run. Reading a
Page never starts another collector. Avoid overlapping Ansible checks with the
routine's normal concurrency controls.

## Create the custom application

Use an authorized editor CLI session. These commands only prepare the draft;
publishing executable code is a separate reviewed operation.

```sh
crewship page project init --dir mysql-app
cp examples/pages-apps/main.tsx.tmpl mysql-app/src/main.tsx
crewship page project pack mysql-app > mysql-source.yaml
crewship page project set mysql-health --file mysql-source.yaml --revision 0
crewship page project build mysql-health --revision 1
crewship page project preview mysql-health
```

Open **App preview** in Studio, inspect the source and behavior, run **Check
application**, then publish the reviewed build. Published `Run check` asks for
confirmation in Studio and receives a pending receipt. `Check my run` resolves
only that viewer's request. It does not poll continuously. Explicit retries
retain the same idempotency key; preparing a new request is a separate action.

The same application source works for the Ansible Page because both declare
`health/refresh`. The Page definition decides which routine executes. The
browser cannot substitute a routine, shell command, playbook or inventory.

```sh
crewship page export mysql-health > mysql-health.bundle.yaml
crewship page import mysql-health.bundle.yaml --slug mysql-health-copy
```

The one-file Page export contains the authored definition and React source.
The target must supply the referenced crew/routine. It does not export database
contents, inventories, credentials, existing runs or a trusted publication.
Import remains an inert draft. Deliver collector scripts using the normal Crew
file mechanism; bundling a script as Page source does not install or execute it.

## Local verification

```sh
python3 -B -m unittest discover -s examples/pages-apps/scripts -p 'test_*.py'
crewship routine validate examples/pages-apps/mysql-health.routine.yaml
crewship routine validate examples/pages-apps/ansible-status.routine.yaml
```

The optional `--publish PAGE/PANEL` collector flag is for an external process
with its own already-authorized CLI session and producer grant. It is not the
routine path above and must not be used to inject human credentials into crews.

## Isolated real-service pilot

`pilot/run.py` starts a disposable MySQL server and a collector container on an
internal Docker network, with no published ports. It verifies authenticated
`SELECT 1`, stopped-database failure, successful Ansible check mode and a failing
playbook. Temporary credentials never enter Page data; the containers, network
and credential files are removed afterward. The MySQL server is actual MySQL;
the collector image uses a compatible MariaDB command-line client.

Build `pilot/Dockerfile`, install the MySQL image, inspect their digests, then run:

```sh
python3 examples/pages-apps/pilot/run.py \
  --mysql-image 'mysql@sha256:<installed-digest>' \
  --collector-image 'crewship-pages-pilot@sha256:<built-digest>' \
  --output /tmp/pages-pilot-results.json
```

The optional CLI acceptance test consumes those results using
`PAGES_TEST_PILOT_RESULTS=/tmp/pages-pilot-results.json` together with
`PAGES_TEST_BUILD_IMAGE=<pinned-tools-image>`. It sends the actual verdicts through
CLI → authenticated API → Page snapshots, including producer failure. Human test
tokens remain outside the collector containers. This is not proof of a customer's
inventory, credentials or activated routine schedule; those remain installation
acceptance checks.

## Visible dev3 custom application

`custom-operations.page.yaml` is a single-file v2 export containing the reviewed
React/TypeScript/CSS project and Page definition. Published version 3 is deployed
at `https://crewship-dev3.unifylab.cz/pages/custom-operations` as **Operations Lab**.
It includes a custom layout, memory chart, service filter, actual SDK history,
`runAction` with trusted-host confirmation, and `getActionStatus` for its own run.
The demo inherits the workspace Pages palette through SDK CSS variables and adds
bounded entrance/button animations respecting reduced motion. No external assets
or arbitrary shell/API endpoint are exposed to the browser.

### Real producer and schedule

`scripts/collect_container.mjs` samples cgroup-v2 memory and CPU inside Ops, using
only built-in Node modules. Eight samples take about 1.2 seconds; no subprocess,
network request or LLM call. `custom-operations.routine.yaml` runs that script then
publishes two JSON payloads through governed `page.write`. The routine uses a
concurrency key, 10-second step deadlines and no author-supplied inputs.

On dev3 the script was installed through `crew files save` at
`shared/scripts/pages-operations-sample.mjs`. The routine is
`pages-operations-sample`. Its schedule `psched_cmtty38r20001a61a4e70` runs every
minute UTC, skips missed backlogs, and disables after 3 consecutive failures.
Panel SLA is 3 minutes. Opening more browser tabs does not create more collectors.
The graph shows eight readings from the last run, not a claimed long-term series.
A failed collector leaves the last snapshot to age; see the failed routine run
for immediate diagnostics. It never invents a green replacement measurement.

The previous `collect-custom-operations.py` is the historical one-shot **host**
collector from publication 1. Do not run it to feed the current routine-owned
panels. Host data and continuous container data are different evidence.

### Portability

The Page export contains its definition and complete source; publication approval,
live panel snapshots, the installed script and schedule are not transferred.
Bind `crew/ops` and `routine/pages-operations-sample` on import. Install/review the
collector and routine independently in the target environment. A Page import
must not silently start a recurring job.

A whole-object `page.write.args.data: "{{ steps.collect.output.services }}"`
resolves to a JSON object preserving numbers and arrays. Invalid/null/scalar
results fail before dispatch. Other crewship verbs retain string interpolation.

Dev3 uses the opt-in reviewed-code same-origin development mode described in the
[handoff](../../docs/prd/pages-apps-handoff.md); it does not establish process isolation.


## Built-in demo seed

`crewship seed` now includes Operations Lab at `/pages/custom-operations`.
`embed.go` embeds the **same** `custom-operations.page.yaml`,
`custom-operations.routine.yaml` and `scripts/collect_container.mjs` files in the
CLI binary. There is no runtime dependency on this checkout, `/tmp`, or dev3.
The Docker build copies `examples/` so the distributed binary carries them too.

The normal seed phases install the collector into Ops shared files, save the
routine, create the Page and persist the source as a Git revision. With a
configured Pages project store, build worker and runtime, the seed builds,
checks and publishes this reviewed built-in application through the normal API.
It does not install Docker/images, change runtime origins, enable development
same-origin mode, or relax RBAC. Missing configuration is reported; the panel
Page remains available and source is retained when project storage is enabled.
A build wait is bounded to two minutes and is cancellable.

Reseeding preserves an existing custom Page definition, modified source and any
publication history (including withdrawal). Matching unpublished source reuses
its running/ready build. Existing published apps are not automatically upgraded.
The collector and routine follow the ordinary demo seed's reapply convention.
This seeding exception auto-publishes only the exact built-in source and draft
definition; ordinary YAML imports remain inert drafts requiring review.

The seed requests one producer run, like other routine-backed Pages. The Ops
container must be provisioned and running with Node and cgroup v2 available.
If it is still provisioning, refresh the measurement after it is ready; failed
samples are not replaced with fabricated data. The seed creates **no schedule**.
For recurring collection, first verify a successful run and check existing
schedules, then create one only if absent:

```sh
crewship routine schedules list
crewship routine schedules create --slug pages-operations-sample \
  --name 'Operations Lab' --cron '* * * * *' --timezone UTC \
  --catchup skip --max-failures 3
```

Verification of the seeded app (disposable SQLite/Git, optional real offline
Docker compilation, no live workspace mutation):

```sh
PAGES_TEST_BUILD_IMAGE='<installed pinned build image>' \
  go test ./cmd/crewship -run '^TestSeedPageAppLifecycle$' -count=1 -v
go test ./examples/pages-apps -count=1
```
