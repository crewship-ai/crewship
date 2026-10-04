# Keeper control protocol

The static Linux sidecar exposes explicit keeper, control, bootstrap and exec
modes before proxy flags or credential initialization. This standalone protocol
is a prerequisite for staged runtime admission. It does not activate any Docker
provider selector or replace the existing startup path by itself.

The keeper requires PID1 and UID1002, disables dumping, reaps orphan processes
and creates a fresh random boot nonce. Its abstract Unix socket authenticates
kernel peer credentials before reserving bounded connection capacity. UID1002
control peers and UID1001 agent peers have independent pools (32 and 8); other
UIDs are denied before allocation. Initial requests have a two-second deadline,
and the bootstrap completion connection has a 30-second deadline. Filling the
agent pool cannot occupy trusted control capacity or extend the boot watchdog.

Only UID1002 can inspect, seal and reserve a boot. UID1001 consumes the reserved
bootstrap permit once; only that accepted connection can report completion.
The boot is ready only after successful completion. A failed or ambiguous
bootstrap never grants ready admission. Ready exec admits UID1001 and the existing trusted UID1002 sidecar role;
other UIDs are denied by both protocol and socket reservation. The gate
authenticates the socket as
the current UID1002 PID1 before launching a workload. A nonce from a prior
container boot cannot launch a workload in the replacement boot.

The gate strips `LD_PRELOAD`, `LD_AUDIT`, `ENV`, `BASH_ENV`, `SHELLOPTS` and
`BASHOPTS` from bootstrap and workload environments. `LD_LIBRARY_PATH` remains
available. Before enabling a future pilot, move required shell initialization
into explicit bootstrap commands. Applications relying on preload allocators
such as jemalloc need an explicitly linked allocator build or an approved
workload command that configures its allocator after admission; verify the
application behavior. The gate does not preserve image `LD_PRELOAD` behavior.

The protocol authenticates lifecycle evidence, not credentials or network
policy. Its caller is responsible for qualified static bind mounts, immutable
configuration, actual kernel fence readback before sealing, environment storage
and provider exec routing. It does not provide B/sibling isolation or OAuth.
The mandatory Docker fixture exercises real control, bootstrap, peer saturation,
replay denial and restart generation change; absence or skipping fails CI.

Explicit provider qualification is exercised separately from Ensure activation;
legacy startup remains in place until the provider selector is activated.

The legacy opt-in pilot selects `CREWSHIP_EGRESS_FENCE_CREWS`. Ensure initializes
ownership offline, starts only the keeper, then seals before image bootstrap.
Rejected runtime admission is refused and reported for explicit reconciliation;
running artifact upgrades defer to verified idle replacement without interruption.
This pilot does not complete B, sibling-agent isolation or OAuth.

Backup exec uses combined static preparation when the provider offers it.
The pilot admits UID1001/UID1002 commands; root backup self-test commands are
refused with exit126. Drain the pilot and return to a legacy runtime for
workflows requiring root until a separately qualified root mechanism exists.
