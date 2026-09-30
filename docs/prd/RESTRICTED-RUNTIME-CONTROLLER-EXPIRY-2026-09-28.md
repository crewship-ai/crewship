# Restricted runtime: independent expiry after controller death

Follow-up to #2709 / PR #2710, 2026-09-28. The user requested progress from
an offline experiment toward the release PRD. The application grant model and
HTTP/WS integration remain separately assigned in #2711; taking over that
workstream is a pending scope question, not an assumed authorization to
silently create a second grant model.

## Closed runtime gap

Previously, killing the host Manager could leave a client process alive until
startup reconciliation. The container now starts its trusted init child as
UID 1002; task execution remains explicitly UID 1001. Both original agent
and sidecar UIDs are unchanged. No agent code executes in the init identity.

The host delivers the absolute server-issued deadline over stdin to a trusted
UID-1002 helper. It writes `/broker/runtime-lease.json` atomically with private
permissions. The protected init reads that file and enforces a monotonic local
deadline. Rereading the same record never extends its lifetime, an expired
local deadline cannot be revived by a late update, malformed/removed state
fails closed, and grants more than 15 seconds into the future are rejected.
There is a bounded 15-second provisioning window before the first grant;
agent code is released only after current admission and successful delivery.
Renewal remains five-second polling. A failed renewal delivery stops the run.

When this guard returns, init exits and the kernel terminates the remaining
PID namespace, including descendants with open files. UID 1001 cannot signal
init, read/write its lease, or invoke the renewal helper as UID 1002. Docker
configuration audit and real-process tests check these identities separately.
No second host supervisor or restart loop was introduced; restart policy
remains `no`. The existing managed-service controller is untouched.

## Verification

`TestLiveControllerCrashExpiry` launches its own child Manager, admits a real
synthetic UID-1001 run, verifies the attempted init/lease attacks are denied,
then SIGKILLs **only that child Manager**. It never calls Stop/Reconcile while
waiting for the container to die. The observed independent stop was 14,982.635
ms after controller death, inside the 17-second healthy-host test budget.
All eight live tests passed with the race-enabled host harness (45.716 s).
Lease unit tests cover no admission, one unrenewed lease, malformed/removed
files and an excessive deadline.

The first live run exposed a fixture issue: the fake independent service used
the former `hold` command. That command now requires protected init identity
and a lease, so it correctly exited. The fixture now uses ordinary `sleep`
and explicitly proves that service is running before stopping the Manager;
service independence and all other acceptance cases pass again.

Final clean-source verification at `d47b73d8ac3b43fa6a7219353ce6ae99eedbb4a8`:
all eight live tests passed in 46.495 s with `-race`; independent stop after
controller death was 14,969.422 ms. Warm startup p50 was 456.712 ms and p95
497.195 ms (five samples). `go test ./... -count=1 -p 2 -parallel 4
-timeout 25m` and `go vet ./...` both exited zero. The full suite reported
147 passing packages and 12 packages without tests. Temporary test databases
used a private directory in `/dev/shm`; no host mount configuration changed.

Raw source-pinned evidence: [live tests](https://github.com/crewship-ai/crewship/blob/07bfd2360a97dbe1c9f71c03e1d9b5d5d471c993/docs/prd/reports/restricted-runtime-controller-expiry-2026-09-28.txt)
and [full Go tests](https://github.com/crewship-ai/crewship/blob/07bfd2360a97dbe1c9f71c03e1d9b5d5d471c993/docs/prd/reports/restricted-runtime-controller-expiry-go-2026-09-28.txt).

## Authorized dev2 deployment

The same runtime change was cherry-picked onto the preserved dev2 application
baseline as `e8b3572cc092783c12ae2ac1c81665d797127546` and deployed with
`systemctl reload crewship-ws@2`. The running process reported this clean commit,
built at 2026-09-28T12:08:40Z. CLI `whoami` and `system health` succeeded,
the database remained connected, and the public dev2 URL returned HTTP 200.

The eight live tests were repeated from this clean deployed source and passed
with `-race` in 48.757 s. Independent container stop after killing the test's
own controller took 15,093.872 ms, including Docker observation latency; this
is an observed healthy-host result, not a universal 15-second stop guarantee.
[Deployed-source raw evidence](https://github.com/crewship-ai/crewship/blob/07bfd2360a97dbe1c9f71c03e1d9b5d5d471c993/docs/prd/reports/restricted-runtime-controller-expiry-deployed-2026-09-28.txt).
Original untracked wireframe work was restored and its full status inventory
matched the pre-deployment inventory. Recovery stashes remain available.
No shared host, Docker daemon, dev1 or dev3 service was restarted.

## Limits and next integration boundary

This closes Manager-process death, not host/kernel suspension or daemon
administrator interference. Stop remains `termination_unconfirmed` when the
host cannot verify Docker state, even if the independent guard should expire;
reconciliation is still mandatory before replacement. Already-read secret
bytes and completed external effects cannot be recalled.

The previous historical evidence reports correctly describe their earlier
implementation. Their Manager-death limitation is superseded by this change.
The remaining production gates are unchanged: server A2/B authority and all
entrypoints/read paths, connected broker/egress, persistent-volume quotas and
full application acceptance. Ordinary `crewship run` is not yet claimed to
use isolated execution. No shared host or Docker daemon was restarted by
these tests.
