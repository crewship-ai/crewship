# Agent stop follow-up to the dev2 live check

The live smoke on main `bfa28fd8` started Claude in the dev2 `test` crew.
`crewship agent stop test` reported success and the API changed the agent to
STOPPED, but the identified Claude process survived and the run stayed RUNNING.
Only targeted cleanup of that test process ended the run. See the previous
live report at `/srv/crewship/backups/crewship_2/live-validation-2026-09-14/`.

## Root causes and change

- The IPC handler only edited legacy state; it never signalled a runtime.
- The API discarded the IPC response (including errors) and manufactured
  STOPPED. It now requires a matching, confirmed daemon response; failure is
  HTTP 502 with no agent status update.
- Active RunAgent invocations have independent process-local stop ownership.
  Stop closes the creation gate during preparation. Once creation was
  requested, stop signals the runtime and waits for absence AND invocation
  completion. It does not just cancel a running output stream.
- Direct execution (including oversized prompts) now uses `setsid --wait`,
  retaining stdin and literal argv. A per-run container-local PID/start-time
  record allows a process-group TERM without guessing a host PID or killing
  every process with an agent name. Existing tmux runs use their run session.
- State for an unowned running invocation after restart is not a stop proof;
  the daemon refuses confirmation. Agent stop targets invocations present at
  the request, and does not disable future submissions or implement I7.

The direct path requires Linux /proc, util-linux setsid with --wait, /bin/kill,
and the existing stdbuf dependency in the agent container. Unknown identity,
an unreadable probe, or a process that ignores TERM leads to an unconfirmed
stop, not forced success. This change does not add automatic KILL escalation.
PID identity files remain in container /tmp; lifecycle cleanup is separate.
Process-local ownership is not durable restart reconciliation.

## Evidence boundaries

- Red API regression: a daemon error originally produced HTTP 200 and changed
  IDLE to STOPPED. Final tests also reject an empty acknowledgement, a response
  for another agent and a mere requested response.
- Real local process tests exercise direct stop, sibling process-group
  isolation and literal argv/stdin preservation.
- `TestStopAgent_RealDirectProcess` also passed against the existing dev2 test
  container with a disposable sleep process. The container transport in this
  test uses docker exec; the stop implementation and probes are production
  orchestrator code. It is not a new deployed HTTP-to-Claude acceptance test.
- Linux-specific real-process tests use Linux build constraints; the generic
  stop-gate tests run on every platform. No skip-budget increase.
- Removing the direct stop probe via a Go overlay makes the real-process stop
  test fail with an unconfirmed stop deadline. Production files were untouched.
- An initial Docker test exposed setsid detaching from docker exec; --wait
  fixed it. An initial shell test exposed dash's built-in kill rejecting --;
  the probe uses /bin/kill. Earlier red runs are not counted as green.

The optional test-only environment variable `CREWSHIP_STOP_TEST_CONTAINER`
selects the container ID for the real-process test above. Unset by default,
the test uses local Linux processes. Set it only to a designated test
container: it creates and stops its own sleep process and removes its own PID
record. It does not change production configuration.

Complete-suite results, final commit and remote CI are recorded in the PR
and archived logs after completion. Do not infer a full-tree pass from the
focused tests above. No live service reload or deployment occurred in this
follow-up. The failed Claude response smoke remains unexplained; this fix
addresses stopping, not model/provider readiness. R6/R7/I7, mailbox and real
T06/T07/T14 remain separate release work.

## Observed local completion

- Full `go test -p4 ./... -count=1 -timeout=30m`: exit 1, solely
  `TestPX2_AgentStop_UpdateFails500` in API. That fixture previously assumed
  an unavailable IPC daemon could still reach the DB update. It now confirms
  stop first and retains the original DB-failure assertion. All other packages
  passed (or reported no test files); this run is not labelled green.
- After that test-only change, the entire `internal/api` passed in 194.561 s,
  exit 0. The unchanged production code was covered by both runs.
- Full-tree go vet: exit 0. Scoped lint for API/server/orchestrator: 0 issues;
  API lint after the final fixture correction: 0 issues.
- Go binary build and pnpm build: exit 0. Strict docs inventory: exit 0.
  Skip budget remains 144/144. Focused stop/direct-process race tests passed.
- Final disposable dev2-container stop test: exit 0. The stop-probe mutation
  failed as expected, exit 1. No Claude success claim follows from this test.

Raw logs, including earlier failures, are archived in
`/srv/crewship/backups/crewship_2/agent-stop-fix-2026-09-14/`.

## September 15 live follow-up

Merged current main a1bfdd1e into this branch (0fb64494) without conflicts.
The complete local Go suite on that merged revision passed, exit 0, and
full-tree vet passed. A fresh CI was requested by pushing the merge.

Reloaded dev2 only, confirmed version 0fb64494, then started real Claude run
msg_1789464853997726387_eeca8dffc3a8063e. Stop returned exit 0 and the observed
Claude PID 301741 and setsid PID 301735 disappeared. The run terminated with
143. This is a real deployed CLI/API/runtime test, not the earlier sleep test.

That test exposed a downstream outcome defect: chatbridge only recognized
cancellation of its own context, so the daemon's signal was recorded as FAILED
and the agent became ERROR. The follow-up preserves an explicit agent-stop
cause through SIGTERM settlement (or cancelled preparation), records CANCELLED
in the bridge, and identifies agent-stop origin in terminal metadata so the
agent projection agrees on STOPPED. Other active runs still project RUNNING;
ordinary chat cancellation remains IDLE. Exit 0 remains completion, unrelated
failure and in-band failure remain failure. Stop confirmation still requires
process absence and is not inferred from this error classification.

A bridge/orchestrator regression reproduced FAILED before the correction.
Projection tests cover agent stop, chat cancellation, another active run and
independent failure. Final revised-tree results and the repeated live test are
recorded in the PR and in /srv/crewship/backups/crewship_2/prd-closure-2026-09-15/.
The T01–T14 matrix in that archive explicitly leaves I7, MCP guaranteed memory,
mailbox, real parallel Claude isolation and OS/storage crash evidence open.
