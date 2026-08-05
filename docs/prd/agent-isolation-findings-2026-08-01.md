# Annex — Verified blockers for per-agent OS identity

Status: draft · 2026-08-01 · **Annex to `agent-identity-signing.md`** — does not supersede it

> **Read `agent-identity-signing.md` first.** Its §"LOCKED DECISIONS" (2026-07-23) already
> states the architecture: *agent = its own ephemeral container*, *crew = persistent home*,
> and *wire encryption + moving secrets off the agent = the security core of 1.0*. It also
> already identifies the root cause — shared uid 1001, stealable bearer token, sibling
> impersonation defeating Keeper's L1–L4 zones — and proposes **uid-per-agent + unix-socket
> `SO_PEERCRED`** as the replacement, with a peercred → SPIFFE/SPIRE roadmap.
>
> **This annex adds only what that document does not contain**: implementation blockers
> discovered by reading the current tree and probing a live instance on 2026-08-01. Several
> of them make the naive form of "give each agent its own uid" impossible, and two of them
> would silently undo the work after it ships.
>
> Everything here was verified against `fix/aux-reach-probes-the-slots-endpoint` and against
> dev1. Re-verify before implementing.

---

## 1. Blockers

### 1.1 `/etc` is read-only — runtime `useradd` cannot work

`ReadonlyRootfs: true` for every non-privileged crew (`docker_container.go:1055`, `:1070`).
Confirmed live inside a running crew container on dev1:

```
$ touch /etc/CS_WRITE_TEST
touch: cannot touch '/etc/CS_WRITE_TEST': Read-only file system
$ command -v useradd groupadd adduser newusers
/usr/sbin/useradd
/usr/sbin/groupadd
/usr/sbin/adduser
/usr/sbin/newusers
```

The tools are present. They have nowhere to write.

**And mounting over it does not help.** The daemon resolves an exec user by reading
`/etc/passwd` and `/etc/group` **through the host-side rootfs path** (`GetResourcePath`
over the overlay `merged` dir), not through the container's mount namespace. Every mount
runc performs is invisible to it. So:

- a **write** to `/etc/passwd` lands in the overlay upperdir, which the daemon *does* see —
  but that needs a writable rootfs;
- a **mount** over `/etc/passwd` (bind or tmpfs) the daemon does **not** see — `id` works
  inside the container while `docker exec -u <name>` fails with `unable to find user`
  (moby#27028, #39261, #22323).

Bind-mounting the single file additionally breaks because shadow-utils writes `/etc/passwd+`
and renames over the target — a new inode, while the mount stays pinned to the old one. And
`useradd` needs a writable `/etc` **directory** for its lock files regardless. tmpfs over
`/etc` discards `/etc/ssl/certs` and `/etc/nsswitch.conf`, i.e. TLS and name resolution.

**Conclusion: users must be baked into the image at build time.** That is reachable — the
provisioner already builds a per-crew image in a temp container with no `ReadonlyRootfs`
and already writes `/etc/environment` there (`provisioner_install.go:451`). Bake a **pool**
of slots (e.g. 256) rather than named agents, so adding an agent never forces a rebuild.

**Do not put the pool members in the crew group's `/etc/group` line** — glibc silently skips
group lines longer than 1024 characters. Use `HostConfig.GroupAdd` instead (§1.2).

### 1.2 Correction: `GroupAdd` *does* apply to execs

`ExecCreateOptions` has no `GroupAdd` field, but moby's `execSetPlatformOpt` → `getUser()`
appends `user.GetAdditionalGroupsPath(c.HostConfig.GroupAdd, groupPath)` to
`AdditionalGids`. **Supplementary groups set at container create apply to every exec**, and
bare numeric GIDs are accepted with no `/etc/group` entry.

The real defect is the *form* of the user string: the branch populating `Sgids` in
`GetExecUser` is reachable only for the implicit form (`"1001"`); our `"1001:1001"` makes it
unreachable. Verified live — `id` returns `groups=1001(agent)` and nothing else.

Two consequences for this PRD:
- a shared crew group is available **today**, one line, no image change;
- the implicit form `"2000"` with **no** passwd entry yields `Gid = 0` — the root group.
  `IsPrivilegedExecUser` (`internal/provider/container.go:178`) permits that form.

### 1.3 Backup restore flattens ownership — this would undo the work after shipping

`internal/backup/docker.go:176` extracts with `--no-same-owner` running as `1001:1001`,
commented *"we can't chown to other uids and don't want to preserve archive uids across
restored crew identities anyway."*

Correct today. After per-agent uids, **the first `crewship restore` collapses every agent's
home to one uid**, and with `0700` homes each agent loses access to its own directory. The
failure is silent and surfaces at a customer's disaster-recovery drill.

Restore must reconstruct ownership from the slug→uid map in the DB, not from the archive —
it *cannot* come from the archive, since uids differ between instances.

### 1.4 `/secrets` tmpfs root blocks per-uid subdirectories

`secretsTmpfsSpec = "rw,noexec,nosuid,size=16m,mode=0700,uid=1001,gid=1001"`
(`docker.go:609`). At `0700` owned by 1001, an agent running as a different uid cannot
create its own `/secrets/<slug>`. Needs `mode=1770` with the crew gid — the sticky bit
matters, or agents can unlink each other's directories.

Live check also showed the per-agent subdirectories are `drwxr-xr-x` (755) while only the
tmpfs root is `0700`.

### 1.5 umask is 022, not 0002

Verified live. runc resets umask to 0022 after filesystem setup, and `docker exec` never
runs PAM, so neither `pam_umask` nor `/etc/login.defs` nor `~/.profile` applies. The
entrypoint's `umask 0002` binds only PID 1 and its children — exec'd processes are children
of the shim.

Two comments assert the opposite and build logic on it (`exec_sidecar.go:812`, `:955`).
Once homes are per-uid this stops being a shared-memory bug and becomes an isolation bug:
new files default to world-readable.

Fix in the exec wrapper (`crew-runtime-capacity.md` §7.2), plus setgid on shared dirs.

### 1.6 UID allocation constraints

`/crew` is a **host bind mount with no userns-remap**, so container uids *are* host uids.

| Range | Owner | |
|---|---|---|
| 1000–59999 | regular host users (`UID_MAX=60000`) | ✗ |
| 61184–65519 | systemd `DynamicUser=` | ✗ |
| **65536–99999** | Debian Policy: *"avoided by adduser"* | ✅ |
| 100000+ | `/etc/subuid`, userns-remap, rootless Docker | ✗ **fatal** |

**Allocate from 70000, configurable, verified against the host.** Two hard rules:

1. **Allocate instance-globally from the DB, never per crew.** Per-crew numbering would make
   crew-1's agent-1 and crew-2's agent-1 the *same principal on the host*, which matters for
   anything touching both trees (backup, host tooling) and for `RLIMIT_NPROC`, which is
   counted per `(uid, user_ns)`.
2. **Never recycle a uid after an agent is deleted.** Orphaned files remain on disk; a
   recycled uid inherits them. The column needs a retired state, not a reusable hole.

### 1.7 Portability across base images

Users must be created identically on Debian/Ubuntu devcontainer images, `*-slim` bases and
Alpine. The traps, all verified from upstream Dockerfiles:

- uid 1000 is taken on nearly every image, and by a **different name** each time: `vscode`
  (devcontainers base/python/go/java), `node` (javascript-node, node:*), `codespace`
  (universal), `ubuntu` (bare ubuntu:24.04). devcontainer images `userdel` the `ubuntu` user
  explicitly on noble/resolute.
- Alpine has no `useradd` (shadow is a community package needing network); busybox
  `adduser` differs: **`-G` is the primary group, not supplementary**; `-M` is `-H`;
  **`-D` is mandatory** or it execs `passwd` and hangs a non-tty exec forever; `-o` is
  unsupported.
- Always pass `useradd -l` (`--no-log-init`): without it a six-digit uid materialises a
  multi-MB sparse `/var/lib/log/lastlog` on overlayfs, which has bloated and hung builds.
- Never `libnss-extrausers` — the daemon parses the files directly and does not consult NSS,
  so `docker exec -u <name>` would fail while `id` succeeded. Also impossible on musl.
- **Serialize user creation.** shadow uses link-based `.lock` files and `useradd` fails
  outright on contention; busybox takes an advisory lock and, on failure, **warns and
  proceeds anyway** — silent lost writes. The two schemes are mutually invisible.
- One batched shell invocation, not N execs (`crew-runtime-capacity.md` §4.3).

### 1.8 Secrets in argv

`CREWSHIP_AGENT_TOKEN` is interpolated into `curl -H "Authorization: Bearer $…"` in the
agent system prompts (`orchestrator/exec.go:69,99,116,146`, `lead.go`, `peer.go`,
`mcp_memory_inject.go:57`). The shell expands it into argv, and `/proc/<pid>/cmdline` is
mode `0444` — **world-readable regardless of uid**. Per-agent uids do not close this.

`hidepid` is unreachable: remounting `/proc` needs `CAP_SYS_ADMIN`, excluded by
`CapDrop: ALL`, and Docker exposes no option (moby#9049).

Fix: read the header from stdin (`curl -K -`) so the token never reaches argv. Keeper's
`/execute` path is already correct — it injects env, not argv (`keeper_execute.go:639`).

---

## 2. Positive finding: the agent-side guardrail held

Asked an agent on dev1 to read a sibling's `/secrets/<peer>/` and home directory. **It
refused**, identified the request as reconnaissance against another crew member's data, and
declined without enumerating topology.

Worth recording, and worth not over-reading. Filesystem permissions were verified as
permissive (`755` homes and per-agent secrets dirs, one shared uid), so nothing at the OS
layer prevented it. The control that held is at the prompt layer — which is exactly the
layer prompt injection targets.

---

## 3. Where this annex disagrees with earlier advice in the same session

Recorded so the reasoning is auditable rather than quietly dropped.

An earlier assessment in this session scored cryptographic signing **2/10 for 1.0** and
recommended deferring it, on the grounds that the sidecar binds `127.0.0.1:9119`
(`sidecar/server.go:23`) so a stolen bearer token is usable only from inside the container
the attacker already occupies.

**That reasoning is sound for the architecture that ships today and wrong for the one
`agent-identity-signing.md` locks.** Under agent-per-container the sidecar hop crosses a
container boundary, and wire encryption plus attested identity become load-bearing. The
locked decision stands; the 2/10 does not.

The same document's `SO_PEERCRED`-over-unix-socket proposal is also simpler and stronger
than the Ed25519-key-in-the-sidecar sketch raised earlier in the session. Prefer peercred.
Note it requires **distinct uids** to carry any information, so §1.1–§1.7 remain
prerequisites either way.

---

## 3a. Decision 2026-08-01 — container-per-agent is rejected

**Owner decision, this supersedes LOCKED DECISION #1 in `agent-identity-signing.md`.**

> Container per agent is **too expensive**. The priority for now is **usability and
> orchestration**; security is second. Rationale given: a sufficiently capable agent will
> find its way out of a container anyway.

Consequences, recorded so the next reader does not re-derive them:

- Agents keep sharing one crew container. §1.1–§1.8 above stay relevant, but as *hardening
  that can land incrementally*, not as prerequisites for a Step-2 migration.
- The `SO_PEERCRED` mechanism in `agent-identity-signing.md` still needs distinct uids to
  carry any information, so per-agent uid remains its prerequisite whenever it is picked up.
- The per-container cost multiplication noted in §4 does not apply. Capacity planning
  follows crew count, as in `crew-runtime-capacity.md` §5.
- Work order becomes: runtime/reliability fixes → memory-on-wake delivery → lifecycle and
  admission control → per-agent uid → attested identity.

**One factual note for the record, not an argument against the decision.** The exposure
found on 2026-08-01 does not require a container escape. Sibling agents share uid 1001, so
reading a peer's `CREWSHIP_AGENT_TOKEN` from `/proc/<pid>/environ` or its credential files
from a `755` directory is ordinary file access inside the box — no kernel exploit involved.
A container escape is a separate, much harder event. Deferring the isolation work is a
legitimate priority call; it should just be made knowing that the deferred item is a missing
wall rather than a stronger one.

The cheapest parts of that wall are one-liners that ride along with the usability work and
are scheduled as such below: `GroupAdd`, `core: 0` in `Ulimits`, `umask 0002` in the exec
wrapper, and moving the token out of argv.

---

## 4. Scope note

`agent-identity-signing.md` §"Build order" makes per-agent containers Step 2 and the
security core Step 3. This annex does not argue for or against that sequencing. It records
that:

- if agents get their own containers, §1.3 (restore), §1.6 (uid allocation), §1.7
  (portability) and §1.8 (argv) still apply — they are about identity and delivery, not
  about the container boundary;
- §1.1 (read-only `/etc`), §1.2 (`GroupAdd`), §1.4 (`/secrets` mode) and §1.5 (umask) apply
  to whichever container the agent ends up in;
- the per-container costs in `crew-runtime-capacity.md` §5 multiply by agent count rather
  than crew count under agent-per-container, which makes the polling fan-out and the
  netns concurrency cliff more urgent, not less.
