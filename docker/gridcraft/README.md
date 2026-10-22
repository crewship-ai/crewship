# Offline GridCraft worker image

This default-off worker is a persistent private service. The Go application
sends only native workbook snapshots and narrow commands to its authenticated
Unix socket. The worker starts fresh pinned CLI processes for each candidate,
then removes their private temporary directory. Go remains the sole storage
authority; no database, agent tokens, Docker socket or network is available.

Run `bash docker/gridcraft/prepare.sh` to download and verify the v0.10.0
linux/amd64 release and build the service. It verifies archive SHA-256
`b78a28605cd34594fc35dfc1ad36a924054571a1382625fb7506fd70cdfb9197`
and CLI SHA-256
`000100860eb14b641da48e5e0d2dcd6dcc5ddcd65a4b267a0ef2fcdfadcdd984`.
The ignored inputs include the two upstream license texts. The recipe copies
these texts and the Crewship license. Build with
`docker build -f docker/gridcraft/Dockerfile .`.

The qualification workflow tests the image, retains an immutable image archive
and binds its digest to the exact source SHA and workflow run/attempt. This is
component evidence; it does not authorize installation or replace review.
An operator installs the pinned image through the host's infrastructure workflow.

Required deployment profile: network none, read-only root, non-root UID/GID 1000,
capdrop ALL, no-new-privileges, 256 MiB memory/swap ceiling, one CPU, 32 PIDs,
32 MiB private tmpfs at `/scratch`. Bind only the private socket directory at
`/run/worker` and the regular 0600 service secret read-only at
`/run/secrets/service-token`. Both socket and secret directories must be private
to the service/application identity. The secret never reaches CLI environment
or arguments. Only the managed host launcher owns container lifecycle and stale
socket cleanup; the service refuses any preexisting socket path.

The listener is `/run/worker/service.sock`; authenticated requests use
`X-Crewship-Worker-Service`. `POST /v1/candidate` accepts a bounded snapshot plus
`import` or `cell.set`; `GET /v1/health` checks transport without computing.
`crewship-gridcraft-worker --healthcheck` is the packaged readiness probe.

Only native JSON imports and `cell.set` on the active sheet are implemented.
Formula recalculation changing another cell is rejected as a known limitation.
Generic commands, file paths, UI control and arbitrary MCP calls are unavailable.
