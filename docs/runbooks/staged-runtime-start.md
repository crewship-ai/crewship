# Activate and recover the staged runtime pilot

Use the existing crew config/start/stop/restart CLI paths; this feature adds no
public endpoint. Follow the [staged-start contract](../specs/staged-runtime-start.md)
and [container isolation pilot](../security/container-isolation.mdx).

1. Install the matching release's static Linux sidecar and entrypoint artifacts,
   and select a restricted, nonprivileged runc crew with no unsupported mounts,
   features or image VOLUMEs. The runtime data directory must permit host-owned
   immutable artifact copies and private host-owned workload material. A stable
   installation identity and host server UID distinct from runtime UIDs1001/1002
   are required; an ad-hoc Docker provider without ownership identity is
   denied. No private repository is required.
2. Drain active work and confirm ownership before stopping an existing legacy
   runtime. Run `crewship crew restart-agents <slug-or-id>` to remove its runtime
   container while preserving binds and named volumes; the next agent exec
   recreates it. This command force-removes the container, so drain first. Adding the
   selector to a live or stopped legacy container alone returns a configuration
   error; it never silently migrates or kills running work.
3. Add its ID/slug to `container.egress_fence_crews` or
   `CREWSHIP_EGRESS_FENCE_CREWS` and restart the server. A newly staged start
   performs keeper-only prepare, current fence readback and one bootstrap.
   Readiness comes from live generation evidence, not `/workspace/.ready`.
4. After a sidecar or entrypoint upgrade, drain and use `crewship crew
   restart-agents <slug-or-id>` to recreate selected runtimes against the new
   immutable artifact generation. Existing old egress-pilot runtimes also need
   this explicit upgrade step.
5. On failure, inspect the concrete artifact/config/fence/bootstrap error.
   Restore the required artifact or configuration before retrying. A failed or
   ambiguous consumed bootstrap is not retried in the same boot; explicitly
   restart the owned runtime to create a new staging cycle, then start the crew.
6. To roll back, drain and stop the pilot before explicitly recreating a legacy
   runtime and removing its selector. Keep persistent data and current access
   decisions. Removing the selector alone does not downgrade a staged runtime
   or bypass its gate; never reactivate an unfenced legacy runtime automatically.

Operator Docker access is trusted; raw Docker exec bypasses product admission
and is not a supported workload launch path. Test evidence must use the real
provider, calibrate allowed traffic, prove forbidden arrivals are absent, and
record exact server/image/artifact identities and boot generations. The mandatory
CI fixture fails on missing execution, failures or skipped required children.

Keeper-only preparation expires after 30 seconds without contact. The first
trusted status contact gives one 30-second reconciliation window; repeated status
requests do not extend it. An unfinished bootstrap exits with the keeper at that
bound. The host also bounds each staging attempt to 30 seconds. The next owned
restart creates a new nonce. Workload environment is captured at creation in
private, unmounted runtime material; configuration edits require recreation.
Per-exec overrides are applied last. Loader injection and shell-startup variables
are suppressed; `LD_LIBRARY_PATH` remains available. Bootstrap error metadata
reports stage/exit status and bounded stderr size without exporting its contents.

Successful provider-owned runtime removal also deletes matching private workload
material after Docker confirms removal. Unsafe, corrupt or orphaned metadata is
retained for explicit operator reconciliation. The standalone resource-cleanup
client has no configured host output scope and can leave orphaned material;
there is no automatic global sweep. Keep this material under the same protected
host storage controls until an operator verifies its scope before removal.

Each pilot exec intentionally performs an uncached, full-content sidecar audit:
several complete reads and hash passes, an artifact temporary-file write, and
live Docker checks. Transient buffers grow with concurrent execs. Start with a
small pilot and limit concurrent execs conservatively (for example, one or two
on a small backend) while observing memory and latency; this is operator
guidance, not an enforced limit or a verified performance benchmark. No inode,
mtime or path cache replaces content verification. If a selected crew's desired
local image tag is missing, Ensure denies reuse until that tag is restored.
