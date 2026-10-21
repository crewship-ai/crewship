# Offline GridCraft worker image

This experimental worker is disabled unless a host explicitly installs a
qualified image by digest. The image alone is not runtime qualification.

Build `cmd/crewship-gridcraft-worker` with `CGO_ENABLED=0` for linux/amd64 and
place it here as `crewship-gridcraft-worker`. Download the linux-x86_64 tarball
from the official `storytold/gridcraft` v0.10.0 release and verify archive SHA-256
`b78a28605cd34594fc35dfc1ad36a924054571a1382625fb7506fd70cdfb9197`.
Extract only `bin/gridcraft-cli`, whose SHA-256 must be
`000100860eb14b641da48e5e0d2dcd6dcc5ddcd65a4b267a0ef2fcdfadcdd984`.
Extract `share/doc/gridcraft/LICENSE-APACHE` and `LICENSE-MIT` from the same
verified archive into this directory. The two binaries and downloaded license
inputs are ignored; the recipe copies both upstream texts and the host license
into the image. This recipe is not a publication workflow and has not been
qualified for distribution.

The host runner requires: no network, a read-only root, UID/GID 1001, no
capabilities, no-new-privileges, 256 MiB RAM without swap, one CPU, 32 PIDs and
32 MiB private tmpfs at `/scratch`. No host mounts, database, tokens or Docker
socket are passed into the worker. It accepts one bounded JSON task over stdin,
returns a candidate over stdout, then exits. Timeout/cancellation must remove
exactly that worker, including an ambiguous Docker client exit.

Only native JSON workbook imports and `cell.set` on the active sheet are
implemented. Formula recalculation that changes another cell is conservatively
rejected. Other upstream commands, file paths, UI control and arbitrary MCP
calls are not accepted. Runtime image build, pinned installation, resource
placement and crash/cancellation cleanup require operator qualification before
activation. There is no unsandboxed server fallback.
