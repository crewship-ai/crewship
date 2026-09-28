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

Source-pinned final verification is recorded after the complete checks.

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
