# Dev2 deployment and CLI verification

On 2026-09-28 the user explicitly authorized deployment to development 2
and verification through the CLI and tests. This supersedes the earlier
prototype-only prohibition on restarting that instance; dev1, dev3, stage,
the shared host and Docker daemon remain outside this deployment.

## Deployed identity and preservation

- Target: `crewship-ws@2`, API `http://localhost:8082`, public
  <https://crewship-dev2.unifylab.cz>.
- Deployed branch: `dev2/restricted-runtime-live-20260928`, commit
  `dfb29a965e9fefbe0ba35242a98386acc621bcad` (clean at build).
- The previous running binary reported `0d6180244` plus a dirty flag; the
  original checkout was a different documentation branch, `7bf15fdcf`.
  Deployment therefore uses the running binary's recorded commit as its
  source base, plus the seven prototype commits through `574a03d9a`.
  No claim is made that unknown uncommitted content of the old binary is
  reconstructible from its recorded commit alone.
- Relative to that recorded base, only the 15 runtime/probe/documentation
  files changed: no application UI, API, migration or service-controller
  changes. The runtime and probe sources are byte-identical to PR #2710.
- The original wireframe WIP was stashed before switching branches, then
  restored and compared with the original `git status` inventory. All original
  files are present; recovery stash `598ce3df444d` remains available.
  The original branch and pre-deployment binary are also retained locally.
- Deployment used `sudo systemctl reload crewship-ws@2`. The unit completed
  successfully and both Go and Next.js processes are running. Executing
  `/proc/<MainPID>/exe version` confirms **the running inode**, not just a
  replacement file on disk, reports `dfb29a965`, built 11:34:06 UTC.

## CLI and HTTP checks

All CLI calls explicitly targeted `--server http://localhost:8082`.

| Check | Result |
|---|---|
| `version` and running-inode version | Expected deployed commit, Go 1.27.1, linux/amd64 |
| `whoami --format json` | Existing authenticated session and workspace work |
| `system info --format json` | Successful response; Docker 29.3.0 |
| `system health --format json` | DB connected; server healthy |
| `crew list --format json` | 3 crews; full listing matches before deployment |
| `agent list --format json` | 8 agents returned successfully |
| `commands --format json`, `run --help` | CLI tree loads; **no restricted-runtime command or run option** |
| Instance-scoped `doctor --format json` | 0 failures; migration v20260925193600 is latest; 2 existing telemetry/DSN warnings |
| Public dev2 root | HTTP 200 |

Doctor's local database checks require
`DATABASE_URL=file:/srv/crewship/crewship_2/crewship.db`; `--server` alone does
not select its local database file. The first unscoped diagnostic warned
about the absent default database; the scoped check passed. Telemetry warnings
were recorded, not silently repaired as part of an unrelated runtime change.
A CLI read attempted during the reload received connection refused; all
post-reload checks above succeeded.

**Deployment does not wire production dispatch to the prototype.** Ordinary
`crewship run` still uses the existing application runtime. No real-provider
agent run is presented as evidence of restricted execution. The following
explicit terminal harness exercises the isolated Manager, real UID-1001
processes, actual UID-1002 sidecars and synthetic credentials from this exact
deployed checkout:

```sh
scripts/restricted-runtime-probe/run.sh -race
```

## Test evidence

All **seven** live acceptance tests passed in **35.028 s**; see
[raw output and binary hashes](reports/restricted-runtime-dev2-deployed-2026-09-28.txt).
They cover two humans using the same agent, own/foreign data and direct
credentials, read-only aliases and path attacks, sidecar tokens and offline
networking, revocation/expiry/open files, restoration, service independence,
authority/Docker transport failures, and the shared-UID positive control.

Five cached-image starts: 344.428, 388.812, 414.542, 416.074, 477.535 ms;
p50 **414.542 ms**, nearest-rank p95 **477.535 ms**. Bare fixture memory.current
was 2,625,536 bytes; with actual sidecar and synthetic upstream, 16,334,848
bytes. Explicit revocation including post-stop denial assertions took
371.555 ms; authority-outage detection and stop took 5056.366 ms. These are
small synthetic observations on this host, not production guarantees.
The race flag instruments the host harness/Manager, not the separately built
bootstrap and sidecar binaries.

Targeted race tests and `go vet ./...` passed on the deployment branch.
The full-repository run also passed, exit 0: **147 tested packages**, 11
additional packages without tests. See [complete package results](reports/restricted-runtime-dev2-deployed-go-2026-09-28.txt).
API took 199.264 s and database 58.946 s. Reproduction:

```sh
# Both variables select the same owned temporary directory on existing tmpfs.
TMPDIR="$owned_test_dir" GOTMPDIR="$owned_test_dir" GOMAXPROCS=4 \
  go test ./... -count=1 -p 2 -parallel 4 -timeout 25m
go test -race ./internal/restrictedruntime -count=1
go vet ./...
```

The owned scratch directory was removed after the test process exited.
The only remaining dirty entries in the deployed checkout are the same
19 pre-existing wireframe files; no product source was modified after build.
Owned acceptance containers, volumes and build image/context were cleaned up.

## Remaining integration work

The limitations in the [prototype evidence](RESTRICTED-RUNTIME-DEV2-EVIDENCE-2026-09-28.md)
still apply: connected egress/broker, server grant/origin/recall/output/stream
adapters, disk quotas, expiry independent of Manager process lifetime, and
application A2/B acceptance. The runtime is installed as a development
prototype, **not enabled as production protection for ordinary CLI runs**.
PR #2710 remains a draft; deployment authorization is not a merge or review
approval.
