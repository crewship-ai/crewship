# Retained execution output

Supported local Linux Docker providers can capture admitted CLI execution through
an independent host helper. `storage.run_output_path` defaults to
`<installation-data-root>/run-output` during startup; `CREWSHIP_RUN_OUTPUT_PATH`
overrides it. Explicit YAML `run_output_path: ''` disables it. Remote Docker,
Docker Desktop, controller UIDs 1001/1002, and other providers retain their existing execution transport.
Before a protected launch, Docker qualifies the read-only staged stop helper
against its current trusted source hash and versioned process-capability probe.
Known missing or older helpers keep the legacy transport before any protected
claim or CLI execution; operational qualification errors refuse launch.

The controller persists version-2 capture authority before launching its own
executable in private `--internal-run-capture` mode. The helper receives admitted
argv, environment, cwd, bounded stdin, deadline and redaction values through a
private pipe, and starts the actual Docker exec once. These inputs are not written
to workload storage. The helper has its own session and owns the Docker output
attachment independently of the controller. Losing a controller reader does not
stop the helper, start a replacement CLI, or establish a terminal outcome.

The host spool must belong to the server identity, which must differ from agent
UID 1001 and sidecar UID 1002. Private directories, protected ancestors and
absence of workload mounts are checked before launch. Admission rejects symlink
aliases and bind-backed local volumes exposing a spool ancestor. Docker container
ID, image and start identity remain bound to the admitted authority; a restart
cannot silently rebind the execution or reinstall a fence using partial state.

Streams remain separate. Secrets are redacted before the host log is committed;
stdout alone feeds structured CLI parsing. The bounded append-only log records
sequenced byte chunks and the actual provider exit result. A synced, atomically
replaced checkpoint identifies the committed prefix; incomplete tails are ignored.
An exclusive durable claim refuses every duplicate launch, even after an
interrupted handoff. Unknown start/inspection/storage results stay unresolved.

Defaults reserve 64 MiB per run plus 64 KiB of metadata within a 1 GiB, 4096-run aggregate bound.
`storage.run_output_max_bytes` and `storage.run_output_max_runs` override the
aggregate limits; zero selects the defaults. Admission
reserves capacity under a process-shared root lock, so concurrent launches cannot
overcommit. After a verified terminal checkpoint is synced, its reservation
shrinks to the committed source size plus metadata; incomplete captures retain
the full reservation. Capacity exhaustion refuses launch. Output limits, the original
absolute deadline and read failures request a stop and retain their specific
reason. Observer cancellation does not change the original deadline.

The exact-process stop helper uses a pidfd and process start time, with Docker
exec identity and PID namespace checks. It requires host permission to inspect
that identity. The trusted helper verifies the target PID namespace from within the
container, so the controller does not need host ptrace privileges. Killing the exec leader does not prove that descendants stopped:
forced capture results preserve occupancy and reconciliation until whole-runtime
absence is established. No container-wide kill or privileged UID fallback is used.

Recovery validates the original workspace, agent, configured spool root and
provider identity. It reconstructs framing from sequence one, then projects stable
line identities into the journal. Each source acknowledgement follows synchronous
projection. Journal projection receipts survive normal history pruning, so a lost
ACK cannot append the same event again or re-run commit observers. They do not make
external side effects transactional. Workspace bundles preserve visible journal
history but exclude source-installation replay receipts and execution authority;
whole-instance database copies retain the receipts. Structured CLI analysis independently
reconstructs terminal envelopes, in-band failures, session and usage metadata;
exit zero without a required terminal envelope remains unresolved.

A live invocation keeps its settlement authority. After a controller loss,
protected recovery can settle only the current work generation parked by lease
recovery or the explicit retained-capture handoff, at the matching run locator.
Operator resolutions and newer attempts cannot be overwritten. Successful
execution can finish that work; failed execution retains reconciliation for
uncertain external effects. Contradictory previously staged terminal facts are
refused. Normal API completion and recovered completion use the same stable
journal terminal representation and durable receipt.

The periodic server pass includes terminal capture records, because execution can
finish before history projection. Fully acknowledged natural terminal sources
older than seven days are collected, releasing their aggregate reservation.
Collection is crash-resumable and retains the immutable launch tombstone and
projection/collection receipts. Aggregate limits apply to retained capture data
and reservations; immutable identifier and projection receipts remain for the
installation lifetime and admission currently scans those tombstones.
Incomplete, forced or unacknowledged output is not
silently removed. A missing spool never licenses re-execution.

The older version-1 `crewship-sidecar run-capture`, `run-read`, `run-result` and
experimental `AgentRunRequest.DurableOutputDir` transport remain compatibility
fixtures. They run under the workload identity and cannot authenticate a result;
no production dispatcher selects that path. The private host path is version 2.

The required `scripts/ci/managed-environments.py` lane builds the real command
and sidecar plus tagged test binaries, and runs named capture qualifications as
a distinct unprivileged host UID. Missing or skipped qualifications fail the lane.

Verification distinguishes component tests, the real Docker helper transport,
controller-process loss plus reopened durable state/server replay, and full
RunAgent dispatch. A passing component test is not evidence of the latter.
Host power loss, Docker daemon loss and Docker Desktop support require their own
acceptance evidence. The tagged `TestTmuxRunContinuesAfterOutputReaderLoss`
reproducer preserves the original FIFO/SIGPIPE failure for unsupported legacy
transport; it is not a durable-path acceptance test.
