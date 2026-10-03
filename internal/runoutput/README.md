# Run output transport

This package captures a command's output and terminal result independently of
any reader. It is a foundation for controller reattachment, not a released
replacement for the orchestrator's tmux/FIFO execution path.

The existing distributed `crewship-sidecar` binary provides separate process
modes for this transport. These modes do not start the credential proxy:

```sh
crewship-sidecar run-capture --dir /persistent/run-identity --timeout 30m -- command arg
crewship-sidecar run-read --dir /persistent/run-identity --after 12
crewship-sidecar run-result --dir /persistent/run-identity
```

`run-capture` also accepts `--args-file` with bounded NUL-separated argv instead
of command arguments. It does not delete that input file. No argv or environment
is copied into the output journal. The command inherits its caller's environment
and has no stdin. Interactive input and controller authentication are not
provided by this transport.

The directory's parent must already exist on persistent storage. Creation claims
the leaf directory exclusively and syncs its parent before command creation.
An existing or interrupted claim refuses a duplicate launch. Records in
`events.jsonl` have monotonic sequences; a synced, atomically replaced
`checkpoint.json` identifies the committed prefix. Readers ignore an incomplete
or uncommitted tail. A terminal record includes the actual process exit code
and an explicit reason for start failure, cancellation or output limits.

Readers own their acknowledgement cursor. Delivery can repeat after a reader
crash; downstream journal/chat/ledger projection must be idempotent. A reader's
cancellation never signals the capture process. Following advances a local byte
cursor instead of rescanning the whole log on every poll. A fresh reconnect
scans the bounded prefix once to locate its sequence. Output records are byte
chunks, not already-decoded CLI tool events: integration must retain decoder
state or acknowledge only a safely persisted framing boundary. Advancing a
cursor past half a JSON line would lose information on restart.

`run-read` normally emits JSON records and returns transport success regardless
of the captured command's exit code. `--raw` emits command output bytes and
returns the captured exit code; it is intended for the initial CLI stream.
The default serialized-log limit is 64 MiB, with a maximum of 1 GiB. Space is
reserved for a terminal record. Reaching the output limit cancels the command's
process group and records `output_limit`; storage/commit failures are errors and
do not fabricate completion. There is no acknowledgement-driven truncation yet.

`run-result` validates the committed log and returns only its sequence and
terminal result, without exposing command output. Its own exit code indicates
probe success; the returned result contains the captured command's exit code.
A terminal record is not delivered until checkpoint validation completes.

Capture requires a Unix runtime. Linux containers on macOS/Windows hosts are the
intended execution location; this is not evidence of tested Docker Desktop
support. Unit/race tests and the isolated subprocess experiment do not establish
whole-controller crash, Docker restart, host power-loss or graceful-upgrade
acceptance.

The deployment must protect the journal from workload writes if trusted outcome
provenance is required. Running capture and the workload under the same UID does
not establish that boundary. Retained raw output can contain whatever the CLI
prints; it must not become a new unauthenticated read API or bypass the existing
scrubbing and workspace authorization when integrated.

An experimental orchestrator path (`AgentRunRequest.DurableOutputDir`) now
prepares `run-launch`, which exclusively claims the run directory before writing
inputs and starts `run-execute` in a detached tmux session. The admitted client
environment, cwd, stdin and original deadline travel with this launch. The
secret-bearing input file is consumed before the workload starts. No production
dispatcher enables this field and it is not exposed in YAML or the public API.
The tmux conformance test requires `-tags integration` and an installed tmux;
the managed-environment CI lane requires it to actually execute and pass.

RunState retains output location, protocol version, adapter and deadline.
Persistence failure refuses launch; duplicate local ownership and an existing
durable run identity refuse admission. Startup and periodic recovery probe the
retained result and preserve it in RunState. They do not infer cancellation
solely from process absence, or publish completion before output replay. Probe
failure leaves the outcome unresolved, including when a stopped container's
volume cannot yet be read through the exec provider. Retained result evidence
is not acknowledgement of history projection or successful recovery.

Remaining integration: capability qualification for the bound helper binary;
controller detach versus explicit stop; replay into normal event handling with
durable acknowledgement; work-ledger authority; protected writer identity; and
retention. The default tmux/FIFO path remains unchanged until those gates pass.

## Known default-transport failure

The existing tmux/FIFO transport still loses its workload when its output
reader disappears (#2845). Preserve the expected-red reproducer explicitly:

```sh
go test ./internal/orchestrator -tags runoutputrepro -run '^TestTmuxRunContinuesAfterOutputReaderLoss$' -count=1 -v -timeout 1m
```

Run on Linux with tmux installed. The test uses a private tmux socket and
synthetic output; it creates no live agent run. Missing tmux is an error.
The observed failure is an unfinished workload and retained exit 141 (SIGPIPE).
The build tag keeps this known-failing reproducer out of the default passing
suite without reporting it as skipped coverage or raising the skip budget.
Do not count it as durable-transport acceptance or remove its failure evidence.
The separate `TestDetachedLaunchSurvivesReaderAndRejectsDuplicate` integration
test remains required and must execute successfully in managed-environment CI.
Production still selects the existing default transport; this foundation does
not fix #2845 or enable durable output for production dispatch.
