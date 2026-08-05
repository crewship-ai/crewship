# Design — Crew runtime: container configuration, capacity, and wake cost

Status: draft · 2026-08-01 · Companion to `agent-identity-signing.md` (isolation) and
`agent-memory-on-wake.md` (context delivery)

> **Scope note.** This document is about the *crew container as an operational object*:
> how it is configured, what it costs while idle, what limits it, and what a wake
> actually pays for. It is deliberately independent of the container-per-agent decision
> locked in `agent-identity-signing.md` — every defect below is present whether an agent
> shares a container with its crew or gets its own, and several get *worse* under
> container-per-agent because the per-container costs multiply.
>
> All `file:line` references verified against `fix/aux-reach-probes-the-slots-endpoint`
> on 2026-08-01. Live measurements taken against dev1 (`crewship-dev1.unifylab.cz`) the
> same day via the CLI. Re-verify before implementing.

---

## 0. The target we are designing for

An operator runs **20–50 crews (~100 agents)** on one host. Only a handful are active at
any moment; the rest are idle and should cost close to nothing. Agents wake on demand —
chat, routine, issue, delegation — and a wake should feel fast.

Today none of the three properties hold:

| Property | Status |
|---|---|
| Idle crews cost ~nothing | **False** — nothing ever stops them (§2) |
| Concurrent wakes are bounded | **False** — no limit on container creates (§3) |
| A wake is fast | **Partly** — 6.5 s measured, dominated by our own exec fan-out (§4) |

---

## 1. What is configurable today, and what that costs us

**Configurable end to end** (manifest → API → DB column → `HostConfig`): `memory_mb` and
`cpus`. That is the whole list.

`kinds/crew.go:212` → `crews_update.go:187` → `crews.container_memory_mb` →
`docker_container.go:1081`.

**Hardcoded, not configurable at any layer** (all in `buildCrewContainerConfig`):
`PidsLimit` = 200 (`docker_container.go:948`), `/tmp` size, `/secrets` size,
`ReadonlyRootfs`, `CapDrop`, `ExtraHosts`, `User 1001:1001`. And these are absent
entirely — zero occurrences repo-wide: `LogConfig`, `Ulimits`, `ShmSize`, `OomScoreAdj`,
`MemorySwap`, `MemorySwappiness`, `StopSignal`, `StopTimeout`, DNS options, `TZ`, `LANG`.
`RestartPolicy` and `Labels` exist **only** on the sidecar (`sidecar.go:382`, `:339`),
never on the crew container.

### 1.1 The two configurable values have no validation

- Manifest rejects only negatives (`kinds/crew.go:391`).
- `crews_create.go:272` accepts any positive value.
- `crews_update.go:186` writes both straight through with **no check at all** — not even
  the negative guard that `container_ttl_hours` gets three lines later at `:192`.
- Provider guards only `<= 0` (`docker_container.go:531`).

**Failure mode.** `container_cpus: 0.005` passes every layer; the daemon then rejects the
create ("Range of CPUs is from 0.01") and **every agent run for that crew wedges** on an
unactionable error. In the other direction there is no ceiling: a MANAGER can set
`container_memory_mb: 999999`.

**Fix.** Clamp at the API tier with a per-workspace ceiling, and return a 400 naming the
valid range. Docker minimums: 0.01 CPU, 6 MB memory.

### 1.2 Defects in what *is* configured

| # | Defect | file:line | Consequence |
|---|---|---|---|
| 1 | `User: "1001:1001"` (colon form) + no `GroupAdd` | `docker.go:858` | **Zero supplementary groups.** See §1.3 — verified live |
| 2 | `Init` default-off; PID 1 is `exec sleep infinity` | `docker_container.go:1072`, `scripts/entrypoint.sh:36` | Nothing reaps orphans. The sidecar is started as `… &` inside `sh -c` (`exec_sidecar.go:839`) so it is **always** an orphan of PID 1. Zombies count against `pids.max` → monotonic leak toward `fork: Resource temporarily unavailable` |
| 3 | `PidsLimit: 200` | `docker_container.go:948` | Counts **threads and zombies**, not processes. One active Node toolchain is 60–150 tasks. Today this does not comfortably fit *one* active agent |
| 4 | `MemorySwap` unset | `docker_container.go:1080` | Docker defaults it to **2 × Memory**. A crew configured for 4 GiB may use 4 GiB RAM **plus 4 GiB swap** — a clean OOM becomes minutes of thrash degrading every co-resident crew |
| 5 | `Ulimits` unset | absent | Inherits containerd's `LimitNOFILE=1048576`, `LimitNPROC=infinity`, **`LimitCORE=infinity`**. A crashing agent dumps core into its CWD `/output/<slug>` — a **host-persistent bind** — and that core contains every credential in the exec environment. This directly defeats the `/secrets`-as-tmpfs design documented at `docker.go:586-593` |
| 6 | umask is 022 on every exec | verified live | `exec_sidecar.go:812` and `:955` both assert umask 0002 and build shared-memory logic on it. With 022, setgid propagates the *group* but files land `0644` — the sidecar (gid 1002) can read them and **cannot write them**. Looks like a live bug in crew-shared memory |
| 7 | `ShmSize` unset → `/dev/shm` = 64 MB | verified live | Chromium/Playwright crash. The repo itself ships Playwright |
| 8 | Healthcheck is a tautology and nobody reads it | `docker_container.go:1000` | Tests `/workspace/.ready`; `/workspace` is a host-persistent bind, so the marker survives forever and a fresh container reports `healthy` at t=0. `ContainerStatus` (`:1282`) ignores `State.Health` entirely. Costs a container exec per crew per 30 s and buys nothing |
| 9 | No `Labels` on the crew container | `docker_container.go:996` | Every crew-container discovery path is name-based and DB-driven (`FindCrewContainer`, `PruneCrewRuntimes`, the orphan reaper). A container whose DB row is gone — crew deleted while the daemon was unreachable, DB restored from an older backup, `container_prefix` changed — is **invisible to every GC path** and keeps its mounts, network and credentials |
| 10 | Provisioner build container has no limits at all | `provisioner_install.go:58` | Runs user-declared devcontainer feature `install.sh` from `ghcr.io` **as root** with no `Memory`, `NanoCPUs`, `PidsLimit`, `CapDrop` or `SecurityOpt`. The sidecar's own comment (`sidecar.go:352`) makes exactly this argument for why sidecars got capped; the strictly more privileged container got nothing |
| 11 | `sudo` is dead code | image | setuid transition is blocked by `no-new-privileges`, and `CapDrop: ALL` would leave the resulting root powerless. Pure attack surface |

### 1.3 The supplementary-group defect, and a correction

An earlier reading of this codebase concluded that `ExecCreateOptions` has no `GroupAdd`
and therefore supplementary groups are unreachable for execs. **That is wrong and the
correction matters.**

moby's `execSetPlatformOpt` calls `getUser()`, which appends
`user.GetAdditionalGroupsPath(c.HostConfig.GroupAdd, groupPath)` to `AdditionalGids`. So
**`HostConfig.GroupAdd`, set at container create, applies to every subsequent exec** — and
it accepts bare numeric GIDs that need no `/etc/group` entry at all.

The actual defect is the *form* of the user string. In moby's `GetExecUser`, the branch
that populates `Sgids` is reachable only when `groupArg == nil` — i.e. only for the
implicit form `"1001"`. Our `"1001:1001"` sets `groupArg != nil`, making that branch
unreachable.

Verified live on dev1:

```
$ id
uid=1001(agent) gid=1001(agent) groups=1001(agent)
```

One group. No supplementary groups have ever been delivered.

**Fix.** Set `GroupAdd: []string{"<crew-gid>", "1002"}` on the crew container. This is a
one-line change that works today, needs no `/etc/group` edits, and is a prerequisite for
any shared-group filesystem design.

**Footgun to close at the same time.** The implicit form `User: "2000"` with **no**
matching passwd entry yields `Gid = 0` — the root group — because `GetExecUser` starts
from a zero-valued struct. `provider.IsPrivilegedExecUser` (`internal/provider/container.go:178`)
permits the bare-uid form today, so this is reachable on a BYOI image.

---

## 2. Idle crews are never stopped

### 2.1 Live evidence

On dev1, 2026-08-01, all three crew containers were running with **zero runs in history**:

| Crew | Running since |
|---|---|
| ops | 2026-07-27 (5 days) |
| quality | 2026-07-30 |
| engineering | 2026-07-30 |

### 2.2 Why

`crews.container_ttl_hours` is **nullable with no DEFAULT** (`migrate_consts_v01_init.go:108`)
— contrast `container_memory_mb INTEGER NOT NULL DEFAULT 4096` on the next line.
`crews_create.go:281` stores it only when `> 0`; `agent_config.go:872` yields 0 for NULL;
`checkTTLs` skips `ttl <= 0` (`orchestrator_lifecycle.go:130`).

**Out of the box, every crew container runs forever once started.**

There is exactly one reaper: `Orchestrator.Start` → `checkTTLs`, 5-minute interval
(`orchestrator_lifecycle.go:92`), measuring from last activity.

### 2.3 Four further defects, even when TTL is set

1. **State is in-process only.** `o.crews` (`orchestrator.go:270`) rebuilds from zero on
   every `crewshipd` restart. Containers rehydrated at boot (`server_lifecycle.go:827`)
   register with the **stats collector only** (`:846`), never with `refreshActivity`. A
   container that survives a restart and is never woken again is never reaped.
2. **Last-writer-wins.** `refreshActivity` sets `cs.ttl = 0` whenever `ttlHours <= 0`
   (`orchestrator_lifecycle.go:115`), so any run carrying TTL 0 silently clears a
   previously registered TTL. `routes_agent.go:59` reads `ttl_hours` straight off the HTTP
   body with default 0.
3. **`CrewConfig.TTLHours` is dead on the provider path.** Zero hits under
   `internal/provider/docker/`. The doc comment at `internal/provider/container.go:32`
   ("auto-stop after idle period") describes behaviour that does not exist there.
4. **Two wake paths never register a TTL or stats.** Script steps
   (`runner_script.go:312`) and prewarm (`prewarm.go:53`) call `EnsureCrewRuntime` without
   reaching `RunAgent`. Such a container is registered **nowhere** — no TTL, no stats, no
   port scanner — and runs until `crewshipd` restarts or an operator stops it by hand.

### 2.4 `agentless: true` is not a container-zero guarantee

`runWakeCheck` (`pipeline/schedules.go:1191`) runs the probe routine through the full DAG
chain to `EnsureCrewRuntime`, and the gate is evaluated at `:1163` **after** the probe has
already run. Fail-closed suppresses only the *main* run. If a wake-gate probe contains any
agent or script step, it wakes the container it was meant to gate.

---

## 3. There is no admission control on containers

### 3.1 The one global limit is on the wrong side of the door

`runSem`, default **8** (`config.go:272`, `orchestrator.go:288`), is acquired **inside**
`RunAgent` at `orchestrator_run.go:230`. But `RunAgent` consumes a pre-resolved
`req.ContainerID` — **every one of the eleven callers ensures the container itself, first.**

So: 20 crews waking together start 20 containers in parallel; backpressure arrives only
afterwards, when 8 of them are allowed to talk to an LLM.

`internal/scheduler` has **no semaphore at all** — robfig/cron runs every due entry in its
own goroutine (`scheduler.go:248`). Twenty crews sharing a cron minute is twenty
simultaneous `EnsureCrewRuntime` calls.

The per-crew assignment budget **multiplies rather than caps**:
`defaultAgentMemoryEstimateMB = 2048` (`assignments_queue.go:47`) gives 20 crews × 8 GiB =
80 admitted runs against a `runSem` of 8.

### 3.2 No host-resource awareness anywhere

No `/proc/meminfo`, no loadavg, no cgroup read, no `gopsutil`. `EnsureCrewRuntime` performs
zero capacity checks before `ContainerCreate`. `internal/diskusage` exists but gates
nothing. `license.max_crews` (default 15) caps crew *rows*, not running containers.

### 3.3 Why concurrent wakes are specifically bad

Network-namespace creation takes a **single global lock**, and its cost grows with the
number of namespaces already on the host. Two independent measurements, seven kernel major
versions apart, agree:

- SOCK (USENIX ATC '18): full 5-namespace container churn ≈ 200 c/s with netns, > 400 c/s
  with IPv6 and the broadcast path removed, **900 c/s with netns eliminated entirely** —
  i.e. netns alone is ~78 % of all namespace work.
- Ant Group (OSDI '25, production fleet): `clone()` with `CLONE_NEWNET|CLONE_NEWIPC` costs
  **1.45 ms serial → ~418 ms at 24× concurrency**.

**Design consequence:** wakes must be staggered, not fanned out. And fewer resident
containers make every subsequent container start cheaper — an additional argument for
stopping idle crews.

---

## 4. Wake cost is an exec fan-out problem, not a container-start problem

### 4.1 Measured

Live on dev1, agent idle 5 days, container warm:

| | Time to answer |
|---|---|
| First wake | **6.5 s** |
| Immediately again | **4.8 s** |

### 4.2 Where it goes

**13–16 sequential `docker exec` round-trips before the agent CLI starts, paid identically
on a warm container.** Each `Exec` is ≥2 daemon calls (`ExecCreate` + `ExecStart`,
`docker.go:857`, `:938`); `ExecInspect` polls at 50 ms. So ~26–35 daemon round-trips per
run.

`preparePreflightDirs` (`orchestrator_run.go:1138`) alone is 8–10 of them: mkdir, manifest
pre-create, memory dirs, crew memory dirs, migration probe, credential files, Claude
config, MCP config, OAuth token injection (1–2 **per OAuth MCP server**), prompt files.
Plus `checkSidecar`, `prepMemoryDirs`, optional `startSidecar`, and `setupTmuxExec`'s
probe + write.

For comparison, published anchors for the container work itself: `docker start` on an
existing stopped container is estimated at **~250–450 ms** on a cloud VM (no isolated
benchmark exists; warm `docker run --rm` measures 554–568 ms on a 2-vCPU Azure VM, n=50,
and `ctr run` 294 ms on 4-core bare metal). `unpause` is 3 ms.

**The container is not the problem. We are.**

### 4.3 Two constraints on the fix

1. **Collapse, do not parallelise.** Concurrent execs contend on the same daemon path;
   fifteen parallel execs would be worse than fifteen sequential. The target is *one* exec
   that does all of it — a script delivered on stdin, which `ExecConfig.Stdin` already
   supports (`internal/provider/container.go`).
2. **Check runc ≥ 1.2 on the hosts.** Below that, the CVE-2019-5736 mitigation memfd-copies
   the entire ~15 MB runc binary on **every container start and every exec** — measured at
   22.6 ms vs 13.9 ms per `runc run`. At ~17 execs per wake that is **~150 ms of pure waste
   per wake, removed for free by an upgrade.** Not verifiable from inside a container;
   run `runc --version` on the host.

---

## 5. What an idle crew actually costs

Correcting two figures that are easy to overstate: **`Memory: 4096` is a ceiling, not a
reservation**, and **`tmpfs size=500m` is a ceiling, not an allocation** — tmpfs pages are
charged only when written.

Marginal cost of one idle container, from measurement:

| Component | Cost |
|---|---|
| containerd shim, **private** RSS | **1–5 MB** (Datadog, `smaps`, six live shims) |
| containerd daemon, marginal | ~0.3–1 MB (single published datapoint) |
| kernel memcg, 2–3 cgroups | ~45–65 KB at 8 cores; scales with host CPU count |
| veth pair | ~25 KB at 8 CPUs; scales with `num_possible_cpus()` |
| our sidecar process + `sleep infinity` | tens of MB (not measured) |

> **Do not cite "10–15 MB per shim."** That is `ps` RSS, which charges the shared Go binary
> text to every process. Marginal is 1–5 MB.

**50 idle crews ≈ 1–2.5 GB.** That is affordable. The real costs are elsewhere:

1. **Polling.** The listening-port scanner runs **one `docker exec` per tracked container
   every 15 s** (`listening_port_scanner.go:22`), plus a healthcheck exec per container per
   30 s. At 50 crews that is ~5 execs/s. One `docker exec` forks ~15 processes, so this is
   **~75 forks/s purely to ask "are you alive."** At ~85 ms of daemon wall-clock per exec
   that is **~42 % of dockerd's serialized exec capacity spent polling idle crews** — and
   moby PR #43480 measured that with ~50 health-checked containers the
   `Tasks/Start` RPC "could take upwards of a full second." Same fleet size as our target.
2. **`/tmp` grows unbounded.** `sidecar.log` is appended with `2>>` (`exec_sidecar.go:839`)
   with no rotation, on a tmpfs whose pages are charged to the crew's memory cgroup. A slow
   leak that eventually OOM-kills the crew and presents as an agent bug.
3. **Zombies** against `PidsLimit: 200` (§1.2 #2).

### 5.1 Host ceiling, in the order it arrives

1. **ARP/neighbour table** — `gc_thresh2 = 512` default. Bites first if containers talk.
2. **1024 ports per Linux bridge** — hard kernel limit `BR_PORT_BITS`; verified in
   moby#44973 where the 1003rd container broke connectivity for all.
3. Shim RSS — ~1–5 GB at 1000 containers.

At 20–50 crews we are far below all three. **The ceiling is not our problem; concurrent
wake latency is.**

---

## 6. Idle strategy: stop, do not pause

`docker pause` is a **single write to `cgroup.freeze`** — runc's `Pause()` touches nothing
else. It therefore **frees no memory**; moby keeps `State.Running == true` and the cgroup
keeps every page. It buys CPU scheduling, which on an idle container whose processes sit in
`epoll_wait` is approximately nothing.

There is a real technique available — a frozen cgroup cannot fault its pages back in, so
`memory.reclaim` or a lowered `memory.high` evicts them from the writer's context ("this is
irrelevant to freezing" — memcg maintainer, i.e. the freezer neither enables nor blocks
reclaim). **Pause + proactive reclaim** is the genuine suspend-to-swap recipe. It is two
steps, resume then pays on-demand page faults, and there is no bulk fault-back-in. **Not
recommended for 1.0.**

CRIU is **not reachable at all**: Engine API v1.51 has zero checkpoint endpoints.

`docker stop` loses: all processes and RAM, tmpfs contents, and **the network namespace and
IP** — Docker's own docs: *"Stopped containers lose their IP addresses."* It keeps the
writable layer, container config, and volumes.

**Recommendation: stop idle crews on a TTL.** Restart is a few hundred ms against an LLM
latency measured in seconds, and it reduces the resident netns count that makes every other
start slower.

**Prerequisite:** resolve the IP loss before enabling a TTL default — either name-based
resolution on a user-defined bridge, or a static IP at create. The port-expose proxy holds
`ContainerIP` today.

---

## 7. Work items

Grouped by whether they can ship independently. None of these depend on the
container-per-agent decision.

### 7.1 Independent fixes — ship first, small PR

| # | Item | Why now |
|---|---|---|
| 1 | Delete the crew healthcheck | Tautological *and* unread. Pure removal, frees ~1.7 exec/s at 50 crews |
| 2 | `Init: true` unconditionally | Stops the zombie leak against `PidsLimit` |
| 3 | `GroupAdd: ["<crew-gid>", "1002"]` | Restores supplementary groups; one line |
| 4 | `MemorySwap == Memory` (+ `MemorySwappiness: 0`) | Removes the silent 2× and the thrash mode |
| 5 | `Ulimits` with `core: 0`, bounded `nofile`/`nproc`/`fsize` | Closes the credential-bearing core-dump path |
| 6 | `ShmSize: 1 GiB` | Unbreaks Chromium/Playwright |
| 7 | Rotate or cap `sidecar.log` | Closes the tmpfs leak into the memory cgroup |
| 8 | `RestartPolicy`, `Labels`, `StopTimeout` on the crew container | Parity with the sidecar; makes the container discoverable by GC |
| 9 | Clamp + validate `memory_mb` / `cpus` at the API tier | Stops a wedged crew from a typo |
| 10 | `LANG=C.UTF-8`, `TZ` | Non-ASCII paths, timestamp sanity |
| 11 | Tighten `IsPrivilegedExecUser` against the bare-uid form | Closes the accidental gid-0 path |

### 7.2 Exec wrapper

A small static binary bind-mounted read-only next to `crewship-sidecar` (that pattern
already exists), which before `execve` does: `setrlimit` → `umask(0002)` →
`oom_score_adj=500`. This fixes the umask bug, gives genuine per-agent limits
(`RLIMIT_NPROC` is per **real uid**, so it only becomes meaningful once uids differ), and
biases the cgroup OOM killer away from PID 1 and the sidecar. Today an OOM kills a random
agent, the container survives, and `docker inspect` reports `OOMKilled: false` — a silent,
unattributable failure.

### 7.3 Preflight collapse

Reduce 13–16 sequential execs to one. Biggest single latency win, applies to **every** run.
Collapse, do not parallelise (§4.3).

### 7.4 Lifecycle and admission control

1. `container_ttl_hours` gets a real default and persistence outside process memory.
2. Register TTL and stats on *every* wake path, including script steps and prewarm.
3. Move admission control **before** `EnsureCrewRuntime`; add a semaphore to the scheduler;
   stagger concurrent wakes.
4. Give admission control host-memory awareness (`MemAvailable`, or PSI
   `/proc/pressure/memory`).
5. Resolve the container-IP loss (§6) before enabling a TTL default.
6. Fix `agentless: true` so a wake-gate probe cannot wake the container it gates.
7. Unify the scanner polling intervals; consider deriving `/tmp` and `/secrets` tmpfs sizes
   from the crew's memory budget.

---

## 8. Open questions

1. **Does the container-per-agent decision change the capacity model?** It multiplies
   per-container fixed costs by agent count rather than crew count — 100 agents at 1–5 MB
   shim RSS is still cheap, but the polling fan-out (§5) and the netns concurrency cliff
   (§3.3) both scale with *containers*, not crews. Both fixes above become more important,
   not less.
2. **runc version on dev/prod hosts** — §4.3. Needs a host-side check.
3. **Should `PidsLimit` and tmpfs sizes derive from crew size,** or become first-class
   manifest fields? Today they are constants and `PidsLimit: 200` does not fit one active
   agent.
