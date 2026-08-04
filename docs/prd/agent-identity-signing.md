# PRD — Cryptographic per-agent identity & signing for CrewShip

> **Status:** Draft for external audit. Not yet an issue, not yet scheduled.
> **Author context:** Written by Claude (Opus 4.8) from a direct read of the
> CrewShip codebase on 2026-07-23. All `file:line` references were verified
> against the tree at that time — re-verify before implementing, the code moves.
> **Purpose of this file:** hand to a more capable model for an architecture
> audit / final shape decision, then convert to a phased GitHub issue (repo is
> PUBLIC — see the Disclosure note at the very bottom before publishing).

---

## ✅ LOCKED DECISIONS & PLAN (2026-07-23 — READ THIS FIRST)

Everything below (§0–§15) is supporting analysis. These are the decisions. Simple.

**The model**
1. **Agent = its own container.** Each agent (lead + subagents) is an ephemeral
   "brain": started on an event, killed on sleep. ONE code path (no shared-vs-isolated
   dual logic). Gives real isolation (untrusted / multi-provider safe) and dissolves
   the token-theft hole.
2. **Crew = persistent home.** The crew owns the filesystem (one workspace volume)
   and long-lived services (Postgres, Redis, whole apps). It survives agent sleep
   and deletion; it dies only when the crew is deleted.
3. **Filesystem = one crew volume.** One place the human browses in the dashboard.
   **No git for now.**
4. **Services — two cases only:** (a) **created in the crew** → owned, lives in the
   crew, backed up with the volume; (b) **external** → connected via credential/API,
   NOT owned, its lifecycle stays with the operator ("connect, don't adopt").
5. **Wake / sleep.** Wake on Routine · Issue · Chat · Webhook · Event. Sleep = kill
   the brain container (memory persists centrally). Crew + services keep running.
   You pay for services you keep running + thinking you use; idle agents are free.

**The security work (this is the real content of 1.0)**
6. **Encryption on the wire.** Secure agent ↔ sidecar ↔ backend (signed / mTLS) so
   identity and tokens can't be stolen in transit.
7. **Get secrets OFF the agent.** Today secrets are pushed as plaintext into the
   agent's env + `/secrets/` files — readable by any code in the agent AND by
   same-uid siblings. Fix: secrets never sit in the agent; deliver at use-time;
   per-agent container removes sibling theft.
8. **Keeper — be honest.** Keeper is an OPTIONAL LLM risk-eval overlay, **DEFAULT
   OFF**, on two endpoints, for a subset of credentials. **It is NOT the gate.** The
   real gate is **credential assignment (admin-only membership in `agent_credentials`).**
   Do not depend on Keeper as the security boundary — the boundary is *assignment +
   per-agent isolation + wire encryption*.

**The operability**
9. **Concurrency = a setting** with a recommended default; the user scales it.
10. **Live service inventory read from Docker** (with icons) — the single
    human-readable "what this crew has" view.
11. **Docker now → Kubernetes later, same shape:** crew → namespace + PVC · owned
    service → Deployment · agent → Job/Pod (scale-to-zero) · inventory → from the API.

**Build order**
- **Step 1 — Crew as an environment:** crew network + volume + service containers,
  live inventory, whole-crew backup. (High value, no isolation needed yet.)
- **Step 2 — Agent = ephemeral container:** lead + subagents, sleep=kill,
  wake-on-event, isolated from each other.
- **Step 3 — Security core (1.0):** wire encryption + move secrets off the agent
  (deliver at use-time). This is what makes 1.0 "secure".
- **Step 4 — Later:** capability profiles (free vs restricted crew), egress
  default-deny, gVisor when untrusted third-party code runs.

> Note: the original title ("identity & signing") is now too narrow — this doc
> became the isolation + crew-runtime + credential-delivery architecture. Split into
> three issues when converting: (1) crew-as-environment + backup, (2) per-agent
> container + wire encryption, (3) secret-delivery redesign (deprecate Keeper-as-gate).

---

## 0. One-paragraph summary

Agents in one crew share a single container **and a single OS uid (`1001:1001`)**.
Per-agent identity is enforced today only by a **bearer token**
(`internaltoken.DeriveAgentToken`) that the sidecar constant-time-compares in
`actingIdentity()`. Because siblings share uid 1001, **agent A can read agent
B's token** from the shared process space and impersonate B to obtain a
credential B is assigned but A is not — defeating Keeper's L1–L4 trust zones.
This PRD replaces the *stealable bearer token* with an **unforgeable,
kernel-attested identity** (uid-per-agent + unix-socket `SO_PEERCRED`) and adds
**sidecar-side signing** of Keeper actions for a non-repudiable audit trail.
Roadmap for scale: `peercred` (single host) → `SPIFFE/SPIRE` (multi-node k8s);
isolation ladder tops out at `gVisor`, **not** microVM.

This is **hardening + strategic foundation**, not an emergency: the gap is
intra-crew, intra-workspace, same-tenant. Its value is that it makes Keeper
trust zones *actually enforceable*, gives a signed/tamper-evident audit, and is
the base layer for future cross-server ("servers around the world") federation.

---

## 1. Human value analysis (why do this at all)

**It buys security, not performance.** Signing/verifying costs a few microseconds
per request. Anyone framing this as "faster" is wrong. The value is entirely
security + audit + strategic foundation.

1. **What it brings** — an unforgeable agent identity. Today the backend/Keeper
   trusts `requesting_agent_id`, which the sidecar derives from a bearer token.
   Whoever holds the token *is* the agent. After the change, identity is
   attested by the kernel (peercred) or a signature, not a stealable token.

2. **Where we get more robust** — it closes one concrete hole: all agents in a
   crew run as uid 1001, so agent A (trust L1) can read agent B's (trust L3)
   token from shared process space and pose as B to use an L3 credential A was
   never granted. That undermines the entire point of Keeper's L1–L4 zones.

3. **What it adds for free** — non-repudiable audit (every Keeper action
   provably tied to one agent), key-scoped revocation (kill a leaked agent
   identity without touching others or the human owner), a clean Kubernetes
   path (peercred → SPIFFE/SPIRE), and it keeps *every* coding-agent CLI working
   with zero per-CLI work (signing stays sidecar-side; the CLI never signs).

4. **How much value** — medium-high **strategic**, not acute. Not "patch a CVE."
   For a product selling a "secure self-hosted agent runtime," provable
   per-agent identity + signed audit is a **differentiating capability**, and
   the precondition for federation.

5. **What it costs / prework** — medium effort in well-bounded seams (single
   identity function, single listener, ~10 uid sites, one Keeper body field),
   **not** a rewrite. Main hidden traps: uid allocation, `/etc/passwd` for
   arbitrary uids, and shared-workspace file permissions under per-agent uid.

**Where it does NOT help (don't oversell):** it does not stop an attacker who
gains code execution and escalates to **root** in the shared container — same
kernel. That needs gVisor/microVM (future tier), and is premature until
untrusted third-party code runs in a crew.

---

## 2. Current-state facts (verified in code — give these to the auditor)

### 2.1 Container & identity model
- Container is **per-crew, shared**: `orchestrator_run.go:1004` "agents sharing
  one crew container"; `orchestrator_run.go:860` "shared per-crew sidecar".
- **All agent processes run as uid `1001:1001`** — 10 spawn sites in
  `internal/orchestrator/` hardcode `User: "1001:1001"`:
  `cli_adapter.go:305` (the agent CLI), `orchestrator_run.go` (several),
  `exec_sidecar.go`, `exec_mcp.go`, `exec_mcp_npx.go`, `memory.go`,
  `skills_writer.go`, `secrets_cleanup.go`, `agent_brief.go`,
  `orchestrator_exec_env.go`.
  **Refinement (audit 2026-07-23):** it's **~10 files but 17 literal
  `User: "1001:1001"` occurrences** (several files spawn multiple helpers).
  The *agent CLI itself* is spawned at `orchestrator_run.go:1324`. Same dir also
  has root execs (`User: "0:0"`) and sidecar `1002:1002` — so the tree already
  runs three distinct uids; adding per-agent uids extends an existing pattern.
- **HOW THE TOKEN IS DELIVERED (the crux — PRD never stated it):** the per-agent
  token is injected as an **environment variable** `CREWSHIP_AGENT_TOKEN`
  (`orchestrator_run.go:864-865`), carried into the agent CLI's `ExecConfig.Env`
  (`:1319-1324`). MCP/curl configs reference only the `${CREWSHIP_AGENT_TOKEN}`
  *placeholder*, so the literal token exists only in the process environment.
  **This is why the attack works and why the fix must be uid separation:** on
  Linux `/proc/<pid>/environ` is readable by any process with the *same real
  uid*. All siblings are uid 1001 → agent A reads `/proc/<pidB>/environ`, lifts
  B's token, replays it as `Authorization: Bearer`. env-var/file/`/proc/mem`/
  `ptrace` are ALL keyed on uid — so no delivery trick fixes this without a
  distinct uid. The team already relies on exactly this fact: the sidecar runs as
  1002 *specifically* to stop agents reading `/proc/<sidecar_pid>/mem`
  (`exec_sidecar.go:715-717`). Sibling-vs-sibling just never got the same
  treatment.
- **Sidecar runs as uid `1002:1002`** (`exec_sidecar.go:721`) — a real
  privilege boundary between agents (1001) and the credential-brokering sidecar
  (1002). This is why plaintext secrets held in the sidecar are not readable by
  agents.
- Container image users: `Dockerfile:71` `adduser -u 1001 -S crewship`;
  `docker/crewship-sandbox/Dockerfile:29` `useradd -u 1001 -g agent agent`
  ("sidecar uses UID 1002"). `internal/devcontainer/dockerfile.go` builds
  devcontainer variants (`_REMOTE_USER=agent`).

### 2.2 How an agent authenticates today
- Stateless **HMAC-derived tokens** from one master secret
  (`internal/auth/internaltoken/internaltoken.go`):
  `DeriveWorkspaceToken:73`, `DeriveCrewToken:190`, `DeriveAgentToken:145`
  (`agtv1.<ws>.<agent>.<mac>`). Master **never enters the container**; derived
  tokens are injected at spawn (`exec_sidecar.go:63`).
- **Agent → sidecar IPC**: `Authorization: Bearer <agtv1>` over **TCP**
  `127.0.0.1:9119`, resolved in `internal/sidecar/identity.go:40`
  `actingIdentity()` by constant-time compare against the boot agent token
  (`s.ipc.AgentToken`) and each crew member's `AuthToken`.
- **Sidecar → backend**: `X-Internal-Token` (workspace/crew-bound,
  `coordinator.go:237`), plus `X-Caller-User-Id` / `X-Caller-Source` for the
  human-on-behalf case (`coordinator.go:296-300`). Human id is vouched by an
  HMAC signature `SignCaller`/`VerifyCaller` (`internaltoken.go:268,285`),
  verified at `internal_credentials_mutate.go:135`; the sidecar deliberately
  **refuses to sign agent-supplied ids** to avoid being a signing oracle
  (`coordinator.go:282-300`).
- Existing hardening the team already built (#796, #1274, CRE-153): `identity.go`
  `tokensProvisioned()` (:68), `tokenlessDowngrade()` (:119),
  `refuseUnauthorizedMemory` (`memory_guard.go`), and
  `TestSidecarRoutes_IdentityCoverage` (`memory_routes_coverage_test.go`) which
  fails CI if a sidecar route is left unclassified.

### 2.3 Keeper credential-access path
- `internal/api/keeper_execute.go`: request carries **`RequestingAgentID` in the
  body** (`:113`), validated non-empty (`:142`), used for the credential JOIN
  (`:175-181`) and a re-validation JOIN (`:476-481`). The identity itself rests on
  the sidecar having set it — i.e. on the bearer token.
  **This is the exact spoofing surface: bind `RequestingAgentID` to an attested
  identity, and body-spoofing dies.**
  **Correction (audit 2026-07-23):** container pinning at `:513-516` is *not*
  part of the spoofing surface — the exec target is server-derived
  (`h.container.CrewContainerName(...)`) and the handler explicitly ignores a
  body-supplied `container_id`. `internalAuth` also already pins workspace + crew
  (handler concedes at `:160-161` that it does *not* pin the agent id). So the
  attacker-controlled input is exactly one field, `requesting_agent_id` — the fix
  scope is genuinely narrow.
- Keeper decisions: `ALLOW / DENY / ESCALATE / PENDING`
  (`internal/keeper/types.go:11`), trust zones `SecurityLevel` L1–L4
  (`types.go:50`). Plaintext credential lives only in `crewshipd`/sidecar memory
  (`internal/keeper/secrets/store.go`); released at `keeper_execute.go:488`
  after the ACTIVE + `agent_credentials` re-check.

### 2.4 Secret storage (where an agent private key would live)
- Table `credentials` (`internal/database/migrate_consts_v01_init.go:275`),
  column `encrypted_value` = AES-256-GCM envelope (`internal/encryption/
  encryption.go:121`, master key `ENCRYPTION_KEY`, envelope-versioned, fail-
  closed). SSH/PEM private keys already stored this way (migration v94). Per-
  agent assignment via `agent_credentials` (`migrate_consts_v01_init.go:303`).
- `agents` table (`migrate_consts_v01_init.go:127`) has **no public/private key
  column** today; only `webhook_secret` (:146) as a per-agent secret. Natural
  place to add a `public_key` column.

### 2.5 Existing crypto in the repo
- **Ed25519** used **verify-only** for license signing (`internal/license/
  license.go`); the only place a private Ed25519 key is handled is the offline
  `tools/license-keygen/`. **HMAC-SHA256** for webhooks (`internal/webhook/
  hmac.go`) and internal tokens (`internaltoken`). **cosign/ecdsa** verify-only
  for self-update (`internal/update/sigverify.go`). No nacl, no KMS, no
  secp256k1/Schnorr. **Correction (audit 2026-07-23):** `filippo.io/age` **is**
  in the tree and active — but only for *backup* encryption (`internal/backup/
  encrypt.go`, `runner_create.go`, `internal/api/backup.go`), a subsystem
  unrelated to credential storage. So the "no age" claim was wrong; it does not
  change the recommendation.
  → Ed25519 is the path of least resistance; secp256k1/Schnorr (Nostr) is not in
  the tree.

### 2.6 MCP / protocol reality (context for the auditor)
- CrewShip has **no in-process LLM runtime**; it execs external coding-agent CLIs
  (Claude Code, Codex, Cursor, Droid, Gemini, OpenCode) via a `CLIAdapter`
  strategy (`internal/orchestrator/cli_adapter.go:21`) and parses their stdout.
- MCP is real (native CLI config files + a sidecar MCP gateway over JSON-RPC 2.0,
  `internal/sidecar/mcp_gateway.go`; built-in memory/routines MCP servers with
  `alwaysLoad`). **Composio is one optional MCP provider**, not "the" mechanism.
- There is **no ACP** and **no custom app↔agent wire protocol** — the boundary is
  exec + stdout parsing. Adopting ACP later = writing a new adapter, not
  replacing anything.

---

## 3. Reference designs

- **Buzz (block/buzz, Apache-2.0, Rust/Nostr, launched 2026-07-21):** every
  participant (human or agent) holds a **secp256k1/Schnorr keypair that belongs
  to them, not the platform**; every action is a signed Nostr event in one
  hash-chained log; auth via NIP-42 (WS) / NIP-98 (HTTP); git via NIP-34.
  Headline design: **"authorization ≠ authorship"** — the human signs a
  narrowly-scoped authorization, the agent signs its own work with its own key;
  leak the agent key → revoke the agent without touching the human identity.
  Nuance: **no relay-to-relay federation** — the relay is the single source of
  truth; portability is at the *identity* layer only. Note Buzz **does**
  orchestration (`buzz-workflow`, `buzz-acp`) — it is a closer competitor than a
  pure comms layer.
- **Orca (stablyai/orca, MIT, Electron/React, YC-backed, ~26.7k★, daily
  releases):** also execs external CLIs (no own runtime). Enviable patterns:
  CLI-drivable orchestration-over-terminals (`@group` addressing, task-DAG,
  gates, coordinator loop), version-matched skills (`orca skills get`), Design
  Mode → prompt, SSH-relay remote worktrees. **Fork is impractical** (can't keep
  pace with a funded daily-shipping team) — borrow patterns, don't fork the tree.
- **SPIFFE/SPIRE (CNCF):** the multi-node/k8s standard for attested workload
  identity (X.509/JWT SVIDs). peercred is the single-host version of the same
  idea; SPIFFE is the distributed upgrade and can carry the agent signing key.

---

## 4. Goals / Non-goals

**Goals**
1. Per-agent identity a co-located sibling **cannot forge or steal**.
2. Bind `keeper_execute.RequestingAgentID` to that attested identity.
3. Non-repudiable, tamper-evident audit of Keeper credential actions.
4. **Zero per-CLI work** — signing stays sidecar/server-side.
5. Clean upgrade path to k8s multi-node (SPIFFE/SPIRE) with no rewrite.

**Non-goals**
- Hard isolation vs root-escalation (gVisor/microVM) — design only, future.
- Federation transport (relay-to-relay).
- Replacing RBAC/capabilities — signing **complements** the existing
  verify-then-authorize seam at `internal_credentials_mutate.go:135`.

---

## 5. Critical constraints discovered in code (READ before implementing)

1. **Port 9119 is multi-purpose.** The forward proxy is the top-level handler
   (`server.go:362`) serving the **model proxy** (`ANTHROPIC_BASE_URL=
   http://localhost:9119`, `proxy.go:352`) **and** `/credentials`, `/keeper`,
   `/memory/*`, `/mcp/*`, `/query`, `/escalate`. **The listener cannot simply
   move to a unix socket** — coding-agent CLIs point their model base URL at the
   TCP port. → add a **second, unix-socket listener** for identity-bearing
   routes; keep TCP for the model proxy.
2. **Model-proxy asymmetry.** The proxy injects the per-agent **LLM key** by
   identity but must stay TCP. If hardening only covers unix-socket routes,
   **sibling LLM-key theft remains open.** Decide: accept, or mitigate (per-uid
   loopback binding / abstract-namespace socket). Track explicitly.
3. **Shared crew workspace file permissions.** All agents are uid 1001 today, so
   they freely read/write shared `/crew/agents/<slug>/` worktrees. **Per-agent
   uid breaks this** — needs a shared crew group + group-writable perms
   (umask 002) or POSIX ACLs. **Potential showstopper; prototype first.**
4. **`/etc/passwd` for arbitrary uids.** Some CLIs/tools need a valid passwd
   entry + `$HOME`. Unlisted numeric uid breaks them. → pre-seed a uid pool in
   the image, or `nss_wrapper`. **Biggest hidden trap.**
5. **Coverage-test invariant.** New unix-socket routes must be classified in
   `TestSidecarRoutes_IdentityCoverage` or CI fails by design — lean on it.
6. **Backward compat.** Legacy token-less deployments use the #796 membership
   fallback (`tokensProvisioned()==false`). The new scheme must gate cleanly and
   not silently downgrade.
7. **Reuse existing signature infra** (`SignCaller`/`VerifyCaller`, verified at
   `internal_credentials_mutate.go:135`; oracle-avoidance at
   `coordinator.go:282-300`) — extend this seam, don't fork it.

---

## 6. Phased plan

### Phase 0 — Decisions & prework (MUST resolve before Phase 1)
- [ ] **Crypto primitive:** Ed25519 (simple, in-repo) vs secp256k1/Schnorr
      (Nostr interop/federation). *Recommendation:* Ed25519 now; Nostr as a
      second key type later if federation becomes real. **Decide & record.**
- [ ] **Identify the in-container client** that calls the sidecar `/keeper` and
      `/credentials` endpoints (grep found no caller in `internal/cli`,
      `cmd/crewship`, `internal/mcp` — likely an MCP tool / in-container helper).
      That client must switch to the unix socket. **Pin it down first.**
- [ ] **uid allocation scheme:** static (derived from agent DB row) vs dynamic
      per-boot pool. Reproducibility/audit vs collision-freedom across crews.
- [ ] **passwd strategy:** pre-seeded uid pool vs `nss_wrapper`. Prototype a CLI
      (Claude Code + one npx-based) under a non-1001 uid.
- [ ] **Shared-workspace ownership:** shared crew group + `umask 002` vs ACLs.
      Prototype cross-agent read/write.
- [ ] **Model-proxy identity decision** (constraint 5.2): accept or mitigate.
- [ ] **Transport shape:** one unix socket per container (peercred distinguishes
      uid) — confirm sufficient vs per-agent socket.

### Phase 1 — Identity hardening (~80% of the value)
**Container / spawn**
- [ ] Distinct uid per agent at spawn: replace `User: "1001:1001"` at the
      agent-process sites (`cli_adapter.go:305`, `orchestrator_run.go`); audit
      the other 8 sites (helper vs per-agent).
- [ ] Image: pre-seed uid pool + shared crew group (`Dockerfile`,
      `docker/crewship-sandbox/Dockerfile`, `internal/devcontainer/dockerfile.go`).

**Sidecar transport + attestation**
- [ ] Add a **unix-socket listener** alongside TCP (`server.go:678`) on a shared
      volume path; serve **only** identity-bearing routes there.
- [ ] peercred extraction (`unix.GetsockoptUcred`) → caller uid → agent map.
- [ ] Refactor `actingIdentity()` (`identity.go:40`) to prefer **peercred uid**
      over bearer token on the unix socket; keep bearer path for TCP + legacy;
      preserve `tokenlessDowngrade` semantics.
- [ ] Classify new routes in `TestSidecarRoutes_IdentityCoverage`.

**Backend binding**
- [ ] `coordinator.go` forwards the **attested** agent id (+ signature), not a
      body-supplied one.
- [ ] `keeper_execute.go`: stop trusting `body.RequestingAgentID` (:113) as
      identity — bind to the verified value; reject mismatch before the JOIN.

**Tests (red first)**
- [ ] Repro: sibling presents another agent's token/id → succeeds today on TCP,
      must fail on the unix-socket path.
- [ ] Cross-uid file read/write in the shared workspace works.

### Phase 2 — Signing & tamper-evident audit
- [ ] Ed25519 keypair generated at `agent create` (`agents_create.go:197`);
      public key → new `agents.public_key` column (mirror `webhook_secret`);
      private key → `credentials` vault (AES-GCM, like SSH keys v94), scoped via
      `agent_credentials`, high `security_level`. If gated in the in-memory
      store, include the new `type` in `secrets.Store.Reload`'s query.
- [ ] Sidecar signs each Keeper request (canonical bytes:
      `agent_id | credential_id | intent | command | ts`) with the attested
      identity; backend verifies at `internal_credentials_mutate.go:135`.
- [ ] **"Authorization ≠ authorship" (Buzz)** for ESCALATE: human approval
      produces a **signed, narrowly-scoped grant** (user, agent, credential,
      expiry) the agent references — replacing a plain DB flag.
- [ ] **Hash-chained signed audit** for Keeper decisions (SHA-256 chain,
      single-writer) over `journal_entries`/`audit_logs`
      (`internal_handler.go:62`).

### Phase 3 — Future isolation tiers (DESIGN ONLY, do not build now)
- Isolation ladder: `uid+process (now)` → `container-per-agent (shared pod)` →
  `gVisor runtimeClass` → `microVM (Kata/Firecracker)`. **microVM rejected** for
  hundreds-of-agents scale. **gVisor** is the "cheap virtual kernel" tier if
  untrusted third-party code ever runs in a crew (this is how Cloud Run / GKE
  Sandbox run many semi-trusted workloads cheaply).
- k8s mapping: crew → Pod, agent → container, sidecar → native sidecar
  container, unix socket → shared volume, `runAsUser` per agent (matches k8s
  security best practice: runAsNonRoot, seccompProfile RuntimeDefault, drop caps,
  readOnlyRootFilesystem).
- Identity evolution: `SO_PEERCRED` (single host) → **SPIFFE/SPIRE SVID**
  (multi-node) — same signing key, attested by SPIRE instead of the kernel.

---

## 7. Things to configure / set up (so we don't forget)
- [ ] `Dockerfile` + `docker/crewship-sandbox/Dockerfile` +
      `internal/devcontainer/dockerfile.go`: uid pool, shared crew group, umask,
      passwd/nss_wrapper.
- [ ] New migration: `agents.public_key` column (new migrate-const in
      `internal/database/`).
- [ ] New credential `type` for agent signing keys (+ `secrets.Store.Reload`
      query if in-memory-gated).
- [ ] Sidecar boot payload (`cmd/crewship-sidecar/main.go`, `exec_sidecar.go:707`):
      unix socket path, uid→agent map.
- [ ] Config flags: enable-peercred, legacy-fallback toggle.
- [ ] CI: extend `TestSidecarRoutes_IdentityCoverage`; add a cross-uid workspace
      test to the runtime harness (`scripts/test-harness/`, memory/credential
      suites — the universal use case).
- [ ] Docs: `docs/guides/*.mdx` security page (project rule #2 — docs ship with
      the feature).

---

## 8. Open questions / risks (unresolved — for the auditor)
1. **Shared-workspace file perms under per-agent uid** — biggest correctness
   risk. May force a shared group + ACL model or a coarser granularity.
   *Prototype before committing.*
2. **Which in-container client calls the sidecar keeper/credential API?**
   Unknown after grep — must be found; dictates the client-side transport change.
3. **Model-proxy LLM-key theft between siblings** — stays open unless separately
   mitigated (proxy is TCP).
4. **npx/node CLIs under arbitrary uid** — real `$HOME`/passwd breakage risk;
   test early.
5. **uid exhaustion / collision** across many concurrent crews — allocation
   scheme must be robust.
6. **Performance** — negligible (µs). Explicitly NOT a perf feature.

## 9. Out of scope
Root-escalation defense (gVisor/microVM), federation transport, RBAC changes,
Nostr interop (unless Phase 0 selects Nostr keys).

## 11. AUDIT ADDENDUM (critical review, 2026-07-23)

> Added by a second pass that re-verified every `file:line` against the tree and
> stress-tested the *plan*, not just the facts. Verdict: **the core diagnosis is
> correct and the vulnerability is real; the proposed remedy is architecturally
> right but the plan under-weights its cost, over-scopes Phase 2, and leaves a
> by-design hole open. Do not treat this as shovel-ready.**

### 11.1 What verification confirmed
- **The crux is real.** Same container, same uid 1001, token in `CREWSHIP_AGENT_TOKEN`
  env → a sibling reads `/proc/<pid>/environ` and impersonates. Confirmed end-to-end.
- **`requesting_agent_id` is body-controlled**, guarded only by `internalAuth`
  (workspace+crew), not by an authenticated agent identity. Confirmed. Fix scope
  is one field.
- Auth/token/hardening claims (`actingIdentity`, constant-time compares,
  `SignCaller`/`VerifyCaller`, signing-oracle refusal, `tokensProvisioned`,
  `refuseUnauthorizedMemory`, coverage test) **all check out.** The team has
  already fought and fixed one real bypass in this exact area (CRE-153, legacy
  `/memory/*` routes). That is evidence this subsystem is actively maintained and
  the new work will land on solid, test-guarded seams.

### 11.2 Reframe the threat — the PRD sells the wrong villain
The doc frames the win as "make Keeper L1–L4 trust zones enforceable." That
undersells it and picks a weak adversary: **the human tenant owns every
credential in their own crew — they gain nothing by having agent A steal agent
B's cred.** The adversary that actually matters is **prompt-injection lateral
movement**: a lower-trust agent processing untrusted input gets hijacked and
pivots to a *higher-trust sibling's* credentials inside the same container. That
is a live, industry-recognized agent-security threat and it is the real
justification. Rewrite §1 around it.

**Reachability gate (answer before scheduling anything):** how many *real*
deployments run ≥2 agents at *different* `SecurityLevel`s in one crew, at least
one of which ingests untrusted content? If that number is ~0 today, this is
pre-emptive hardening and can wait behind features. If it's growing, it's on the
critical path. **This single number decides the priority — the PRD never asks
for it.**

### 11.3 The hardest architectural objection — the awkward middle
uid-per-agent *inside a shared container* is the **most operationally painful
boundary that still isn't a clean one.** It pays almost the full cost of
separate containers (per-uid passwd/`$HOME`/nss_wrapper, per-CLI compat matrix,
shared-workspace permission model) while giving a *weaker* boundary (shared
mount/pid/net namespaces; a root-escalation still owns everyone — the PRD admits
this). Two alternatives the PRD doesn't weigh and must:

1. **Per-agent user-namespace remap.** Each agent runs in a userns where it still
   *sees* uid 1001 (so `$HOME`, passwd, and shared-workspace perms are
   unchanged) but maps to a **distinct host uid** (so `/proc`, `ptrace`, and
   `SO_PEERCRED` isolation are real). This potentially gets Phase 1's security
   with a fraction of the blast radius — no passwd pool, no umask/ACL redesign.
   Nesting userns inside an already-containerized runtime is finicky and must be
   prototyped, but if it works it dominates flat distinct-uids. **This is the
   option most likely to change the whole plan; evaluate it first.**
2. **Container-per-agent.** Cleaner boundary, but it fights the product's soul:
   the crew *is* a set of agents sharing one `/crew` workspace (see the
   crew-filesystem "agent-beside-not-inside" vision, CRE-105). So this is
   probably rejected — but the rejection should be *written down with that
   reason*, not left implicit.

**Corollary:** constraint §5.3 (shared-workspace file permissions under
per-agent uid) is not a "side risk to prototype." For a product whose entire
concept is a shared crew workspace, **that permission model IS the project.**
Solve it (or prove userns removes it) in Phase 0 or don't start. Elevate it.

### 11.4 The by-design hole the plan leaves open (§5.2 is bigger than a footnote)
The model proxy **must** stay on TCP 9119 (CLIs point `ANTHROPIC_BASE_URL`
there), and that same TCP port today also serves the control plane
(`/keeper/execute`, `/credentials`, `/memory/*`, …) authenticated by the
**stealable bearer token**. So after *all* of Phase 1:

- If identity-bearing routes are ALSO kept on TCP for back-comaptibility → a
  stolen token still reaches `/keeper` and `/credentials` over TCP and **the hole
  is not closed at all.** The unix socket is then just an alternate door.
- To actually close it you must **remove those routes from the TCP listener**,
  which requires knowing every in-container client that calls them — and the PRD's
  own Open Question #2 admits *it could not find that client.* That is not an open
  question, it's a **Phase-0 blocker**: you cannot safely move a door you can't
  locate. Find the caller (grep suggests an MCP tool / in-container helper;
  `exec.go`/`lead.go`/`peer.go` curl the sidecar with `$CREWSHIP_AGENT_TOKEN`) —
  pin it before committing to the socket split.
- Even in the best case, **LLM-API-key theft between siblings stays open** (proxy
  injects the key on TCP, keyed by the stealable identity). The PRD says "decide:
  accept or mitigate." Decide it up front — an identity project that still lets a
  sibling steal your Anthropic key and burn your quota has a credibility gap.

### 11.5 Phase 2 (signing) defends a *different* adversary — unbundle it
- The sidecar is already fully trusted (it brokers every credential and holds
  plaintext in memory). If the *sidecar* signs the Keeper request, the signature
  proves "the sidecar, acting for B, asked" — which an attested identity + an
  audit row already prove. Sidecar-side signing therefore buys little over
  Phase 1 for the intra-crew adversary.
- What signing + a **hash-chained audit log** actually defends is **post-hoc log
  tampering by someone with DB/backend write access** — a real threat, but a
  *different, higher-bar* one than "sibling agent." Bundling them into one PRD
  muddles the value story and inflates scope. Ship Phase 1 alone; make
  tamper-evident audit its own initiative with its own threat model.
- **"Authorization ≠ authorship" (Buzz) is misapplied here.** Buzz's property is
  that *the agent* signs with a key *only it holds*. In this design the sidecar
  holds/uses the key on the agent's behalf, so you don't get that property. And
  if you gave the *agent process* its own private key, a sibling steals it the
  same way it steals the token today (same uid, `/proc`) — so agent-held keys are
  **meaningless until Phase 1's uid separation lands**, and even then live better
  in the 1002 sidecar than in the 1001 agent. Keep the elegant framing, but stop
  claiming a guarantee the architecture doesn't deliver.

### 11.6 Stability: this is a cost, not a benefit — say so
§0 calls this "hardening + stability." Be honest: **short-term it REDUCES
stability.** It rewrites the single most load-bearing path (agent spawn + shared
workspace) and must keep **six external CLIs** (Claude Code, Codex, Cursor,
Droid, Gemini, OpenCode) working under a non-1001 uid with a synthetic passwd
entry and group-writable shared dirs. That is a wide regression surface. The
trade is *security gain for stability risk* — a good trade if §11.2's
reachability number is non-trivial, a bad one if it's zero. Don't sell it as
"more stable."

### 11.7 Cheap wins to bank NOW (independent of the big project)
1. **Blast-radius policy in Keeper (days, not weeks, no uid work).** Refuse to
   release an L3/L4 credential into a container that also hosts a lower-trust
   agent — or restrict L4 creds to single-agent crews. This caps the exact
   damage the crux enables, is pure policy at `keeper_execute.go`, and buys time
   to do Phase 1 right. Costs some product flexibility (mixed-trust crews can't
   hold the highest creds); worth it as defense-in-depth.
2. **Alert on token reuse anomalies.** Weak (same-uid theft is undetectable at
   the token layer) — list it, don't oversell it.
3. **Decide the model-proxy question (§11.4) in writing** before any code.

### 11.8 Recommendation
- **Do it, but re-shape it.** Phase 1 (attested identity) is the right spine and
  lands on well-tested seams. But: (a) gate on the §11.2 reachability number;
  (b) prototype **userns-remap vs flat distinct-uid** in Phase 0 — it may gut the
  hardest costs; (c) treat shared-workspace perms and "find the in-container
  client" as Phase-0 blockers, not open questions; (d) resolve the model-proxy
  TCP hole explicitly; (e) **split Phase 2 out** as a separate tamper-evident-audit
  effort; (f) ship the Keeper blast-radius policy (§11.7.1) immediately regardless.
- **Net:** medium-high strategic value for a "secure self-hosted agent runtime,"
  real vulnerability, correct instinct — but a genuinely hard, multi-week change
  with a showstopper-class prerequisite (workspace perms) and a by-design residual
  hole (model proxy). Not a quick patch; scope it honestly.

---

## 12. ISOLATION FOUNDATION (added 2026-07-23 — folds full agent isolation into this PRD)

> The original PRD scoped *identity*. This section answers the broader ask:
> **build the foundation so agents genuinely don't know about each other and
> cannot leave their box unless allowed.** It is grounded in a fresh audit of how
> CrewShip creates containers TODAY, and in how Anthropic/OpenAI and the
> code-sandbox providers (Modal, e2b, Daytona) actually do it.

### 12.0 What CrewShip ALREADY has (verified — do not rebuild)
Container creation goes through the **moby/moby Docker Engine API**
(`internal/provider/docker/docker_container.go:572`), and the `HostConfig`
baseline is already strong (`:1058-1091`):
`CapDrop:["ALL"]`, `SecurityOpt:["no-new-privileges"]`, `ReadonlyRootfs:true`,
`Privileged:false`, `User:"1001:1001"`, `PidsLimit:200`, memory/CPU caps, tmpfs
`/tmp`+`/secrets` as `noexec,nosuid`, `NET_RAW` deliberately dropped, feature caps
allowlisted to only `NET_BIND_SERVICE`. Exec **fails closed** on an empty/
privileged user (`docker.go:798-826`); the docker socket is **not** mounted into
agent containers by default. **A `Runtime` field is already wired end-to-end**
(`config.go:91,320`; `docker_container.go:516-522,1059`) accepting
`runc|runsc|kata-runtime|sysbox-runc`, with a `CREWSHIP_RUNTIME` override.
→ This is a better-than-average hardened-OCI baseline. The gaps are narrow.

### 12.1 The two isolation axes (name them separately)
- **Axis A — agent ↔ agent:** siblings must not see each other's processes,
  memory, env, tokens, or sockets. **Open today:** all agents are uid 1001 in one
  shared crew container, no userns → `/proc/<pid>/environ`, `/proc/<pid>/mem`,
  `ptrace`, shared sockets all cross between siblings. (This is §0's vuln.)
- **Axis B — agent ↔ world:** the agent must not escape the box, and must not
  reach the network unless a domain is allowed. **Partly open today:** the
  default image has **unrestricted L3 egress**; the domain allowlist is enforced
  only by a *cooperative* in-container proxy (`HTTP_PROXY`/`ANTHROPIC_BASE_URL` →
  `127.0.0.1:9119`, `exec_env.go:228`). A process that ignores the proxy env and
  opens a raw socket egresses freely. A kernel-level allowlist firewall exists
  (`docker/crewship-sandbox/init-firewall.sh`, default-DROP + ipset allowlist +
  IMDS block) but is **opt-in** (separate image + `NET_ADMIN`).

### 12.2 How the reference implementations actually do it
- **Anthropic Claude Code / `sandbox-runtime`:** sandboxes the *agent process* with
  **bubblewrap + seccomp** (Linux) / **Seatbelt sandbox-exec** (macOS), plus a
  **proxy allowlist** for network. Filesystem = deny-writes-by-default; network =
  all traffic via host proxy. **Explicitly does NOT defend against root
  escalation** ("overly broad write perms enable privesc"). Takeaway: even
  Anthropic treats "strong process isolation" as **filesystem + network + seccomp**
  and leaves kernel-escape to a container/VM layer.
- **OpenAI Codex:** **Landlock (FS) + seccomp (no network by default)** on Linux,
  Seatbelt on macOS, restricted tokens on Windows — the only major agent with
  sandboxing **on by default**, and **no outbound network by default**.
- **Scaled untrusted-code providers:** **Modal → gVisor** (software kernel,
  sub-second start, GPU-friendly, ~10-15% syscall overhead); **e2b/Daytona →
  Firecracker microVM** (own kernel, ~150ms start, snapshot/restore) or
  **hardened OCI** (Daytona 27-90ms: seccomp + netpol + namespaces).
- **Convergent lesson:** *default-deny network + seccomp + per-workload boundary*
  is the baseline; **gVisor** is the price/perf sweet spot for a real kernel
  boundary; **microVM** is reserved for hostile multi-tenant.

### 12.3 The recommended foundation — a 4-layer ladder, cheapest first

**Layer 1 — finish kernel-surface hardening.** *Cost: config → tiny code.*
Already have cap-drop/no-new-privs/ro-rootfs/limits. **Add a custom seccomp
profile** to the `SecurityOpt` slice (`docker_container.go:1034`) — today it
relies on the daemon default. Small, standard, low-risk.

**Layer 2 — make egress default-DENY (biggest Axis-B win).** *Cost: medium.*
Promote the opt-in `crewship-sandbox` firewall to the **default**: default-DROP
netfilter + ipset domain allowlist behind the existing identity-aware proxy, so a
raw socket **cannot bypass** the allowlist. This is exactly the Anthropic/OpenAI
model (no direct egress; proxy allowlist). Highest security-per-effort item here.

**Layer 3 — Axis-A isolation: prefer CONTAINER-PER-AGENT over uid-in-shared-container.**
*Cost: medium-high, but it DELETES the PRD's two showstoppers.*
The original Phase 1 (distinct uid inside one shared container) is the "awkward
middle" (§11.3): it needs an nss_wrapper/passwd pool and a shared-workspace
permission redesign, and still shares PID/mount/net namespaces. **Recommendation:
give each agent its own container, sharing ONE bind-mounted `/crew` workspace
volume.** Then:
- Each agent gets its **own PID/mount/net namespace + own seccomp + own uid**, so
  siblings literally cannot see each other's processes, env, memory, or sockets —
  Axis A solved at the kernel, not by hoping uids differ.
- **Each agent can stay uid 1001 *inside its own container*** → **no passwd pool,
  no nss_wrapper, no shared-workspace uid-permission puzzle.** The two
  showstopper risks (§5.3, §5.4) **evaporate** — the workspace is a normal volume,
  not a same-container multi-uid dir.
- **Identity becomes trivial and unforgeable:** give each agent a **per-agent unix
  socket** to the sidecar whose **mount topology IS the attestation** (agent B's
  container never bind-mounts agent A's socket), or `SO_PEERCRED`/mTLS. No
  stealable bearer token, no signing oracle.
- The sidecar (credential broker) becomes its own container (or one per crew);
  agents reach it over the shared volume socket / an internal-only network.
- This is precisely the **k8s pattern the PRD already sketched for Phase 3**
  (crew→Pod, agent→container, workspace→shared volume) — just pulled forward,
  because it is the *clean* answer to "processes don't know about each other,"
  and CrewShip's Docker-API layer already creates per-crew containers, so creating
  N per crew + a shared volume is an **extension of existing code, not a new
  runtime**.
- *Price/performance:* N containers/crew instead of 1. Per-idle-container RAM is
  modest (shared image layers; process overhead tens of MB). Cold start mitigated
  by image pre-pull + a **warm pool** (Daytona ships hardened containers in
  27-90ms). For hundreds of agents this is exactly what e2b/Daytona/Modal do.
  Verdict: the right price/perf point for CrewShip *now*.

**Layer 4 — kernel-escape upgrade, when untrusted 3rd-party code runs.**
*Cost: config flip (already wired).* Set `container.default_runtime: runsc`
(**gVisor**) — software kernel per container, sub-second start, GPU-friendly,
~10-15% overhead. **Already plumbed**; needs the daemon to register `runsc` +
testing. **Kata/Firecracker microVM** (own kernel) only for genuinely hostile
multi-tenant — **rejected now** for hundreds-of-agents scale (matches §3/§Phase-3).

### 12.4 Sequencing vs the identity PRD
- **This SUPERSEDES the original Phase 1 uid work.** Do NOT do "distinct-uid in a
  shared container." Go straight to **container-per-agent + shared volume** — same
  security goal, cleaner boundary, and it removes the passwd/workspace-perm
  showstoppers that made the original Phase 1 risky.
- **Before 0.0:** Layer 1 (seccomp) + Layer 2 (default-deny egress) are high-value,
  low-risk, mostly config/small-code — land them, plus the Keeper blast-radius
  policy (§11.7.1). These make the "secure runtime" claim honest at launch.
- **Post-0.0 milestone:** Layer 3 (container-per-agent) — this is the real "full
  agent isolation," and it also *is* the attested-identity fix, so identity +
  isolation become one coherent milestone instead of two.
- **Ready when needed:** Layer 4 (gVisor flip) — cheap insurance for untrusted code.

---

## 13. COST MODEL & TIERED ISOLATION (added 2026-07-23 — answers "won't container-per-agent blow up RAM/disk?")

### 13.1 The three fears, checked against the real cost drivers
Verified in code. **Two of the three fears are unfounded:**

1. **"100 agents = 100 images / 100× the CLI on disk" — FALSE.** Images are
   content-addressed and shared: same base+features+mise+Dockerfile hash → **one**
   `crewship-cache:<hash>` image reused across all crews/agents
   (`provisioner.go:338-354`, `provisioner_cache.go:30`). The coding-agent CLIs
   (Claude Code, Codex, Gemini, …) are **baked into that image as layers**
   (`crews.yaml:7` feature + `postCreateCommand`, committed at
   `provisioner.go:502,538`). N containers started from the same image **share ONE
   on-disk copy of the CLI via overlayfs copy-on-write.** Container-per-agent does
   **not** multiply CLI disk or build cost.
2. **"100 agents = 100 live processes hammering RAM/CPU" — FALSE by default.**
   Concurrency is a **global run semaphore, default 8** (`orchestrator.go:944`,
   `CREWSHIP_MAX_CONCURRENT_RUNS`). "100 agents" = 100 *defined*, ~8 *running*.
   RAM is dominated by the ≤8 concurrent agent processes — which you pay in ANY
   isolation model — plus near-zero per-container kernel bookkeeping (namespaces/
   cgroups are not VMs). The container boundary is cheap; the agent process is the
   cost, and it's gated at 8.
3. **"Big engineering change" — TRUE, and this is the real cost.** The genuine new
   costs of container-per-agent are NOT disk/RAM but:
   - **Sidecar re-architecture.** Today the sidecar is **one-per-crew on container
     loopback `127.0.0.1:9119`** (`orchestrator_run.go:977`, `exec_sidecar.go:688`).
     Split agents into separate containers and it's unreachable cross-container.
     Either duplicate it per agent, or move it to a **shared endpoint** (unix
     socket on a shared volume, or an internal network address). **This is the
     linchpin refactor.**
   - **Per-agent worktrees are net-new.** Agents get a per-agent HOME
     (`/crew/agents/<slug>`, `exec_env.go:21`) but share ONE `/workspace`
     (`docker.go:625`); there are **no git worktrees today**. Per-agent code
     isolation must be built.
   - **N-container lifecycle** (create/warm-pool/teardown) vs today's cheap
     `docker exec` into one container.

### 13.2 Bonus robustness point (relevant to "makes it more stable?")
Today `Memory`/`NanoCPUs`/`PidsLimit` are set on the **crew container** — i.e.
shared by all agents in the crew. So one runaway agent can OOM/CPU-starve its
**siblings**. Container-per-agent gives **per-agent cgroups → fault isolation**:
one agent crashing or leaking can't take down the crew. That is a genuine
*stability* gain (unlike the identity work, which is a stability cost).

### 13.3 Recommendation — TIER ISOLATION BY TRUST, don't apply it uniformly
The price/performance-optimal design is **not** "every agent always in its own
container." It is: **the isolation boundary = the trust boundary = the hiring
boundary.**
- **Trusted, same-tenant crew members (the common case):** keep the shared
  container + `docker exec` model (cheapest; what most multi-agent frameworks —
  AutoGen, CrewAI, LangGraph — do, because they assume mutual trust). Add Layer 1
  (seccomp) + Layer 2 (default-deny egress) so even the shared box is hardened.
- **A hired external/untrusted agent, or one ingesting untrusted content:** give
  **that agent** its own container (own uid/PID/net ns, own seccomp, gVisor-
  swappable). When the lead `hire`s an agent it doesn't fully control, THAT one
  gets isolated; trusted co-workers don't pay for it. Isolation on demand, priced
  to the threat. This is exactly the on-demand-sandbox model the code-execution
  providers (e2b/Daytona/Modal) use for hiring: spin a sandbox from the shared
  image on hire, tear down on fire.
- Make isolation a **per-agent/per-crew policy knob**, not a global architecture
  switch, so you dial it to threat + budget.

### 13.4 Kubernetes readiness — do the two prep steps that pay off REGARDLESS
The k8s-idiomatic shape depends on crew size and dynamism:
- **Small, fixed, mutually-trusting crew:** crew→Pod, agents→containers in the
  Pod, workspace→`emptyDir`, sidecar→sidecar container. Clean, but you **can't add
  containers to a running Pod** (bad for dynamic hire) and Pods cap out at ~dozens
  of containers.
- **Dynamic hiring / scale (your case):** **agent→Pod** (or a Job/Pod per run) is
  the idiomatic pattern — the same shape as ephemeral CI runners (GitHub Actions,
  Tekton, Argo). Crew shared state → a **PVC (RWX) or object store**, not a shared
  local dir. hire = create Pod, fire = delete Pod.
- **The tension to resolve:** a single shared *local* `/workspace` is what fights
  k8s scale. The k8s-native shape is **per-agent worktree + shared artifact/coord
  store.** So the two investments that make you k8s-ready ALSO improve the Docker
  product today, independent of any isolation decision:
  1. **Sidecar over a shared endpoint** (unix socket on shared volume / internal
     network) instead of container loopback — unlocks BOTH container-per-agent AND
     the k8s Pod-with-sidecar model. **Do this first; it's the linchpin.**
  2. **Per-agent git worktrees** over a shared artifact store — decouples agents'
     code state (better even in one container) and is the prerequisite for
     Pod-per-agent. Matches the existing crew-filesystem "agent-beside" vision.

### 13.5 Net verdict on container-per-agent
- **Do NOT do it uniformly, and do NOT fear the RAM/disk** — those are shared
  (image CoW) or bounded (8-concurrent). The cost is engineering, not resources.
- **Do** the two k8s-prep refactors (shared-endpoint sidecar; per-agent worktrees)
  regardless — they help today and are the on-ramp to k8s.
- **Then** make container-per-agent a **trust-tiered policy**: shared box for
  trusted crew, own box for hired/untrusted. That is best-practice, hiring-native,
  k8s-ready, and price/performance-optimal.

---

## 14. WORKSPACE, RUNTIME COST & THE "INJECT INTO ANY IMAGE" VISION (added 2026-07-23)

### 14.1 The real cost is the RUNNING CLI and TOKENS — not the container
The concern "100 containers = 100× a heavy Claude Code" needs precise framing:
- **You run a CLI per *active agent turn*, not per *defined agent*.** Concurrency
  is gated at 8 (`orchestrator.go:944`). So it's ~8× Claude Code at once **in any
  topology** — shared container or one-per-agent doesn't change that number. Idle
  agents run **no** process and cost ~0.
- **The dominant multiplier of "more agents" is TOKENS, not RAM.** Industry data:
  multi-agent runs consume **4-220× the tokens** of a single agent (n workers = n×
  context). RAM is secondary and concurrency-bounded; tokens are the real scaling
  cost, and they're independent of container topology. Price the product on
  concurrency + tokens, not on "how many containers fit in RAM."

### 14.2 Two layers of parallelism — don't conflate them (answers "Claude Code already parallelizes")
- **CrewShip agent** = a *persistent* entity with identity, memory, credentials, a
  role, delegation, cross-crew reach, and a lifecycle (hire/fire). Expensive on
  purpose — it's a *coworker*.
- **Claude Code sub-agent** (the CLI's own Task fan-out) = *ephemeral, anonymous,
  intra-task* parallelism inside ONE agent's turn. Cheap, no identity/memory.
- **Design rule to put in the docs and the UI:** *use the CLI's own sub-agents to
  parallelize a single task; hire a CrewShip agent only when you need a distinct
  role / identity / memory / credential.* Hiring 10 CrewShip agents just to fan out
  one task wastes both runtimes AND tokens — that's the anti-pattern to prevent.
  This keeps "agent-per-container" from ever meaning "a heavy CLI per unit of
  parallelism."

### 14.3 Shared workspace + simple human view — the competition has converged on GIT WORKTREES
Every serious parallel-agent tool solves "isolated agents + one coherent human
view" the same way:
- **Orca (stablyai):** the unit of work IS a **git worktree per agent** — fan one
  prompt to 5 agents → 5 worktrees, each a clean repo copy, none can clobber
  another; the human **reviews each agent's diff, annotates, or picks the winner**.
  Ships **SSH remote worktrees** for exactly the remote-human case.
- **octomux:** parallel agents each in its own worktree; **unified diff review +
  permission inbox + monitor grid**.
- **gnap:** a shared **git repo as the task board** (todo/doing/done) — git *is* the
  coordination substrate, no orchestrator process needed.
**Takeaway for CrewShip:** the shared workspace should be **one crew repo; each
agent gets its own git worktree** (isolated working copy) instead of today's single
shared `/workspace` (`docker.go:625`). Coordination + the human view are **git**:
branches/diffs per agent, merged like PRs. This is *simpler* for humans than any
filesystem scheme because they already understand files/branches/diffs.
- **Remote human view = the dashboard, not SSH.** The control plane
  (`crewship.unifylab.cz`) exposes ONE file/diff browser over the crew repo/volume;
  the human never touches a container. Local or remote is identical. Containers are
  an implementation detail the UI never surfaces.

### 14.4 UX rule: present AGENTS and CONCURRENCY, never CONTAINERS
- The product must **never** tell a user "you may run 10 containers or RAM dies."
  Surface **concurrent active agents** (idle = free), tuned to host size,
  autoscaled on k8s. A crew can *define* 100 agents; *N work at once*.
- Frontend unit ladder: **Crew → Agents → Runs → Diffs/Files.** Per-agent card =
  what it's doing + its branch/worktree + its diff (review/merge like a PR, the
  Orca model). One crew file browser. A concurrency dial, not a container quota.

### 14.5 The "inject an agent into any image" vision (GitLab, Uptime Kuma, a customer service)
Architecturally sound and **standard** — this is the sidecar/ephemeral-container
pattern the whole industry already uses:
- **k8s:** a **mutating admission webhook injects a sidecar container** into the
  Pod (exactly how Vault Agent, OpenTelemetry, Datadog attach to arbitrary apps);
  for a *running* target, **ephemeral containers** (`kubectl debug --target`,
  `shareProcessNamespace`) inject at runtime without rebuilding the image.
- **Docker:** bind-mount a **static agent binary** and `exec` it into the target's
  namespaces — same idea, no image rebuild.
- **Critical design consequence — split "presence" from "reasoning":** what you
  inject must be a **lightweight static Go agent-runtime** (like the sidecar binary
  already bind-mounted today, `docker.go:635`), NOT the heavy node/Claude-Code
  runtime — the target image (GitLab, Kuma) has no node and must not be bloated.
  The injected sidecar (a) sees *inside* the target (shared PID/mount/net ns), (b)
  talks back to the orchestrator over the **shared-endpoint sidecar channel**
  (§13.4.1 — the same linchpin refactor), and (c) the LLM reasoning runs in a
  **dedicated, pooled agent container** that acts on the target *through* that
  sidecar. Cheap presence everywhere; heavy reasoning centralized and pooled.
- **What it brings (the product wedge):** orchestration + routines + memory +
  credentials for **any containerized app**, with **no framework adoption and no
  image rebuild**. "Point CrewShip at your existing GitLab/Kuma → get a scheduled,
  orchestrated agent that works *inside* it and coordinates with your crew." That
  is differentiating vs framework-based competitors, and it is the same
  "agent-beside-not-inside" model the crew-filesystem vision already commits to.
- **In the UI:** an injected agent appears as "an agent attached to <your
  service>", sitting beside crew agents, driven by the same routines/orchestration
  surface — the human sees a coworker on their app, not a container.

### 14.6 How this resolves the whole thread
- **Don't fear N× RAM:** runtimes are concurrency-gated (~8), tokens are the real
  cost, idle agents are free. "Agent-per-container" does not add CLI instances.
- **Shared workspace = git worktrees + a dashboard diff/file view.** Simple for
  humans, remote-native, k8s-native (worktree → per-agent copy; repo/volume → PVC).
- **Isolation stays trust-tiered (§13.3):** trusted crew shares a box; hired/
  untrusted/injected agents get their own. The injection vision *is* the extreme
  case of the same model — an agent living in someone else's box, talking home over
  the shared-endpoint channel.
- **Two refactors unlock everything:** (1) sidecar over a shared endpoint,
  (2) per-agent git worktrees. Both help the Docker product today and are the k8s
  on-ramp. **Do them first.**

---

## 15. CONCURRENCY CEILING & SINGLE SOURCE OF TRUTH (added 2026-07-23 — the two points that matter most for scale + UX)

### 15.1 The concurrency model is a real scalability limiter — and it's a SCHEDULER problem, not a container one
Verified: **ONE global semaphore, cap 8, for the entire crewshipd instance**
(`orchestrator.go:950,1001`) — not per-crew, per-provider, or per-agent. The slot
is **held for the whole synchronous turn (up to 30 min)** including LLM-API waits
AND any in-turn background work like a build or DB task (`orchestrator_run.go:230`
`defer releaseRunSlot()`, blocking `Exec` at `:379`). All providers (Claude/Codex/
Gemini/OpenCode) share the same 8. The only escape is a **detached tmux** run,
which frees the slot but then runs **unmetered** (`:601-608`) — i.e. it escapes the
limit entirely, the wrong end of the fix.
**So the user is right: today the instance-wide ceiling is ~8 concurrent turns, and
one agent "creating something" pins a slot for up to 30 minutes.** That caps
CrewShip scalability hard.
- **Crucial: this is orthogonal to container topology.** 8 heavy CLIs is 8 heavy
  CLIs whether in one shared container or eight separate ones. Isolation does NOT
  worsen (or improve) concurrency. Decouple the two decisions.
- **The processes are I/O-bound on the LLM** (per the perf playbook: LLM = 87-99%
  of latency), so a slot-holder is mostly *idle-waiting*. RAM + provider rate
  limits bind, not CPU — so the 8 is conservative and can rise substantially on a
  real server. Don't advertise "10 containers max"; the true limits are **RAM per
  concurrent CLI** and **LLM provider rate limits/token budget**.
- **Fixes (own work item, independent of isolation):**
  1. **Yield the slot while waiting / during long work.** Hold it only during
     active reasoning; release (and *re-meter*, unlike detached-tmux) during long
     background jobs and API waits. This multiplies effective concurrency without
     more RAM.
  2. **Per-provider pools.** Claude vs Codex vs Gemini are different rate-limit +
     RAM domains; split the single global 8 into per-provider pools.
  3. **Horizontal scheduling on k8s.** The ceiling should be *cluster-wide*
     (schedule agent Pods across nodes), not one host's `runSem`. That is the real
     "don't cap scalability" answer.
- **UX rule (restated):** present **concurrent active agents**, tuned to host/
  cluster size, autoscaled on k8s — never a container quota.

### 15.2 Single source of truth — CrewShip ALREADY centralizes truth; isolation does NOT fragment it
The fear "isolate agents → state scatters into boxes → the client hunts through
every container" is **already prevented by the architecture**, because truth is
NOT stored in the execution box — it's in central DB registries:
- **Datastores/services:** `crews.services_json` (v95) + the derived read-model
  `ResolveCrewResources` (`crew_resources.go:63`) — Postgres/Redis/MySQL/Mongo a
  crew has.
- **Tools/MCP per agent:** `agent_mcp_bindings` + `mcp_tool_bindings` +
  `crew/workspace_mcp_servers` — central, per-agent-attributable.
- **Memory (facts):** `memory_versions` with `tier IN (agent,crew,workspace,pins,
  learned)` — one central table, agent-scoped AND crew-shared.
- **Credentials:** `credentials` + `agent_credentials` + Keeper; credentials can
  already be attributed to a provisioned service (`provisioned_for_service`, v98).
- **Audit:** `journal_entries`, `audit_logs`, `credential_audit`, `mcp_tool_calls`.
- **A crew inventory endpoint ALREADY EXISTS:** `GET /api/v1/crews/{id}/capabilities`
  (`crew_capabilities.go:64`) returns ONE bundle — datastores + installed tools +
  integrations (with enabled tool names) + agent list. This is literally the
  "what does this crew have" screen.

**Design principle to make explicit and enforce: "declare to the center, execute at
the edge."** An agent's box is disposable scratch. Anything that must be *true* or
*visible* is written to a central registry via a CrewShip operation — the same way
Keeper mediates credentials. Never let an agent create durable truth that lives
only inside its container.

- **The postgres example, done right:** when an agent provisions a datastore, that
  goes through CrewShip → a **resource record** (kind, **owner_agent**, connection,
  status) written centrally → surfaced in the inventory/capabilities view; its
  credential lands in Keeper (`provisioned_for_service` already exists). The client
  sees *"Crew X: Postgres (owned by Accounting agent, active), Redis (owned by Cache
  agent)"* from ONE registry — never SSHing into a box.
- **The ONE net-new gap:** **per-agent resource attribution.** Services are crew-
  level today (`services_json`), and the capabilities endpoint aggregates at crew
  scope. Add (a) an owner-attributed resource record (small: a table or an owner
  field on the service declaration + a provisioning path that registers centrally),
  and (b) a per-agent inventory view over `agent_mcp_bindings` + resources. That's
  the delta — not a rebuild.

### 15.3 Don't universalize git — pick the source-of-truth per work type
Git is the right canonical store for **code artifacts** (branches/diffs/merge, the
Orca model). It is the wrong metaphor for **records/data** (an accounting system):
there the canonical store is a **shared DB/records the crew owns**, mediated by
CrewShip, with agents proposing changes — not each forking a private copy. The
unifying idea is **disposable edge + central canonical store**; the store's *type*
depends on the work (git=code, DB=records, memory=facts, Keeper=secrets, resource
registry=infra). Git is one registry among several, not "the workspace."

### 15.4 Frontend interpretation — UI and the lead read REGISTRIES, never containers
- **Crew Inventory view** (extend the existing capabilities endpoint with per-agent
  attribution): datastores, tools, integrations, credentials, agents — one screen,
  "what this crew has," each item showing its owning agent.
- **Knowledge/Memory view:** searchable durable facts; the lead answers "where is
  X?" by querying this, because the facts are central — the lead is a *router over
  the registries*, not a thing that peeks into boxes.
- **Files/Artifacts view:** the shared canonical store (repo/volume/records), with
  per-agent contributions shown as diffs if code.
- **Chat with the lead** and the **file explorer** both read the SAME registries —
  that's what keeps it coherent and simple, local or remote.
- **Invariant:** neither the UI nor the lead introspects a container. If an answer
  isn't in a registry, the fix is to *record it in a registry*, not to go look in
  the box.

### 15.5 Net for this thread
- **Concurrency is a scheduler problem** (yield-during-wait, per-provider pools,
  horizontal on k8s) — a separate, high-value work item, independent of isolation.
- **Single source of truth already exists** (memory, credentials, tools, services,
  audit, a capabilities endpoint). The only delta is **per-agent resource
  attribution + a per-agent inventory view**. So you can isolate execution
  *without* fragmenting truth — and you don't have to isolate at all to get the
  coherent client view. Build the registry delta; make isolation an optional knob.

---

## 10. References
- Orca — github.com/stablyai/orca (git-worktree-per-agent, per-agent diff review,
  SSH remote worktrees); octomux, gnap (git-as-coordination).
- k8s sidecar injection (mutating webhook) + ephemeral containers — the standard
  "inject an agent into an existing app" mechanism.
- Anthropic `sandbox-runtime` — github.com/anthropic-experimental/sandbox-runtime
  (bubblewrap+seccomp / Seatbelt, proxy allowlist; no root-escape defense).
- OpenAI Codex sandbox — Landlock + seccomp, default-deny network.
- gVisor (Modal), Firecracker (e2b/Daytona) — untrusted-code isolation tiers.
- Buzz — github.com/block/buzz (authorization≠authorship, per-actor keypair,
  signed events, hash-chained audit, NIP-42/98/34).
- Orca — github.com/stablyai/orca (exec-CLI model, orchestration-over-terminals).
- SPIFFE/SPIRE — CNCF workload identity.
- Related issue: #1001 (Keeper Watchdog EPIC).

---

## ⚠️ Disclosure note (before turning this into a GitHub issue)
`crewship-ai/crewship` is a **PUBLIC** repo. This document describes an
**unpatched** intra-crew credential-escalation path in concrete detail.
Publishing it verbatim to a public issue discloses an attack path before it is
fixed (indexed/cached even if later deleted). Prefer a **private GitHub Security
Advisory**, or a public issue with the exploit specifics softened (framed as
hardening). Decide visibility explicitly before posting.
