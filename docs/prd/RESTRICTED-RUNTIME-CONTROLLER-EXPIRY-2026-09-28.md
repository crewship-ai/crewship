# Restricted runtime: independent expiry after controller death

Status: public technical reference from 2026-09-28. Instance deployments,
measured run logs and work coordination are retained in private context.
Application authority is defined by [restricted context](../specs/restricted-context.md).

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

`TestLiveControllerCrashExpiry` launches an owned child Manager and synthetic
UID-1001 run, verifies init/lease attack denial, kills only that Manager and
observes independent expiry. The scenario must not call Stop/Reconcile while
waiting for termination. Unit coverage includes absent admission, unrenewed
leases, malformed/removed records and excessive deadlines. Report measured
latency separately from the 17-second healthy-host test target; a local result
is not a universal termination guarantee.

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
