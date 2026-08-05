# Iteration spec — 4 quick wins (no re-architecture)

> **Status:** Ready to implement. Small, additive, no architecture change.
> **Context:** Distilled from the 2026-07-23 architecture review. The big items
> (per-agent containers, crew-as-environment, egress/zero-trust, wire encryption,
> Keeper-as-gate) are **parked** in `agent-identity-signing.md` — NOT in this
> iteration. This file is only the things worth doing now.
> **All file:line refs verified against the tree on 2026-07-23 — re-verify before
> editing, the code moves.**

## Ground rules (from CLAUDE.md — apply to every task here)
1. **Failing test first**, then the fix (Go: `*_test.go` next to the package).
2. **Docs ship in the same PR** (`docs/guides/*.mdx`, Mintlify).
3. **API ↔ CLI parity**: any new `/api/v1/...` route gets a matching
   `cmd/crewship/cmd_*.go` command in the same PR, with an acceptance test that
   drives the CLI binary.
4. **Feature branch, never `main`.** Wait for CodeRabbit (~2–5 min) before merge.
5. No `git add -A`; stage explicit paths. No Claude co-author trailer.

## Order & effort
1. **Task 1 — concurrency setting** · ~0.5 day · no risk. Do first.
2. **Task 2 — live service inventory** · ~1–2 days · additive. Do second.
3. **Task 3 — close plaintext-credential-file leak** · ~0.5–1 day · security. Do third.
4. **Task 4 — auth-on for crew datastores** · ~1–2 days · security. Do last.

Independent of each other; can be separate PRs.

---

## Task 1 — Expose the concurrency cap as a user setting

**Why.** The instance runs at most **8 concurrent agent turns** — a hard
scalability ceiling users can't see or tune from the product. It should be a
documented setting with a recommended default that the operator scales to their
host, not a hidden const/env only.

**Where (verified).**
- `internal/orchestrator/orchestrator.go:950` — `const defaultRunSemCap = 8`.
- `internal/orchestrator/orchestrator.go:954-961` — `resolveRunSemCap()` reads
  `CREWSHIP_MAX_CONCURRENT_RUNS` (env-only today).
- `internal/orchestrator/orchestrator.go:1001-1002` — `runSem: make(chan struct{}, runSemCap)` (one global semaphore per instance).
- `internal/orchestrator/orchestrator_run.go:230` — `acquireRunSlot` (held for the whole turn, up to the 30-min request timeout).

**What to do.**
- Add a config field (e.g. `orchestrator.max_concurrent_runs`) in
  `internal/config/config.go` with default **8**. Precedence:
  `CREWSHIP_MAX_CONCURRENT_RUNS` env > config field > default (keep env working).
- Feed it into `resolveRunSemCap()` so the config value is honoured.
- Surface it in the settings UI (dashboard) and document the trade-off (higher =
  more parallelism, bounded by host RAM + LLM provider rate limits; idle agents
  cost nothing).
- If a settings API route is added → add the CLI command (rule 3).

**Acceptance.**
- Unit test: config value flows into the semaphore capacity; env override still
  wins; default is 8 when unset.
- Docs: a short "Scaling / concurrency" section in `docs/guides/*.mdx`.

**Out of scope (park):** per-provider pools, yield-slot-during-wait. (See
`agent-identity-signing.md` §15.1.)

---

## Task 2 — Live service inventory read from Docker (with icons)

**Why.** A crew's datastores/apps run as real containers, but the human has no
single "what is running in this crew" view. Read it live from Docker and show it
with an icon per service type. Additive, high clarity, no architecture change.

**Where (verified).**
- Services run as **separate per-crew containers** (not in-container, not compose):
  `internal/provider/docker/sidecar.go:3-7` (model), `:166-216` (`EnsureCrewServices` lifecycle), `:402-413` (create/start).
- Naming: `<prefix>-svc-<crewSlug>-<name>` — `sidecar.go:141-143`.
- Network DNS alias = service name — `sidecar.go:394-399`.
- Declared in `crews.services_json` (migration v95) —
  `internal/database/migrate_consts_v95_crew_services.go:33-36`.
- Existing derived read-model (from config, not live): `ResolveCrewResources` —
  `internal/api/crew_resources.go:63`; engine inference `inferDatastoreType` `:124`.
- Existing aggregation endpoint to extend: `GET /api/v1/crews/{crewId}/capabilities`
  — `internal/api/crew_capabilities.go:64`.
- Docker client to query: `internal/provider/docker/docker.go` (container list/inspect).

**What to do.**
- Add a provider method that lists a crew's **running** service containers by the
  `-svc-<crewSlug>-` name prefix, returning `{name, image, type, status, ports}`
  (live from Docker, not just from `services_json`).
- Map service type → an icon key (postgres 🐘 / redis 🔴 / mysql / mongo / kuma /
  generic). Icon selection lives in the frontend; API returns the type key.
- Surface via the capabilities endpoint (or a new `.../services` endpoint). If a
  new route → add the CLI command (rule 3) + a CLI-driven acceptance test.
- Dashboard: a crew "Services / Inventory" panel rendering the live list + icons +
  status.

**Acceptance.**
- Acceptance test via the CLI binary: create a crew with a Redis service → the
  inventory lists it as running with type `redis` and its port.
- A stopped/missing service shows as such (live status, not stale config).
- Docs: an "Inventory" section in `docs/guides/*.mdx`.

**Out of scope (park):** external/connected-service inventory, agent-attributed
ownership of a resource. (Future — see `agent-identity-signing.md` §12.3/§15.2.)

---

## Task 3 — Close the plaintext-credential-file leak (prompt vs reality)

**Why (this is a real, concrete inconsistency found in the audit).** The
agent-facing system prompt tells the agent it does **not** hold certain
credentials in its environment — but the orchestrator writes those same
credentials as **cleartext files** into the agent's own container
**unconditionally**, even when Keeper is enabled. The prompt's claim is false, and
a `SECRET` sits as a readable file (mode 0400, owned by the agent's own UID 1001)
inside the box.

**Where (verified).**
- `internal/orchestrator/exec_sidecar.go:173-319` — `buildCredFileScript` writes
  `CLI_TOKEN, SECRET, GENERIC_SECRET, USERPASS, SSH_KEY, CERTIFICATE` as cleartext
  files under `/secrets/<agent>/…`.
- `internal/orchestrator/orchestrator_run.go:1197-1204` — `writeCredentialFiles`
  is called **unconditionally** (no `keeperEnabled` check).
- `internal/api/agent_config.go:1317` — system-prompt line claiming the agent does
  **NOT** have these credentials in its environment.
- `internal/orchestrator/secrets_cleanup.go:64-75` — `hasFileMountedCreds` treats
  `SECRET` as a file-mounted type.
- The **correct pattern already exists for env** (mirror it for files):
  `internal/orchestrator/exec_env.go:346-352` gates `SECRET`→env on
  `keeperEnabled == false`.
- Keeper enable state: `internal/config/config.go:462-475`,
  `internal/server/server.go:616` (default OFF).
- Related runtime warning / exposure map: `orchestrator_run.go:871-878`,
  `exec_env.go:517-610` (`AgentEnvCredentialExposures`).

**What to do (recommended fix).**
- Make file delivery **consistent with the env gate**: when Keeper is enabled for
  the credential types the prompt claims are withheld (at minimum `SECRET`), do
  **not** write the cleartext file — gate `writeCredentialFiles` (or the per-type
  case in `buildCredFileScript`) on the same `keeperEnabled` condition used at
  `exec_env.go:346-352`.
- If Keeper is OFF (the default), current behaviour stays (files written) — that's
  expected; just make sure the prompt text at `agent_config.go:1317` is only
  emitted when the claim is actually true.
- **Implementer must first confirm the intended contract**: is the "you don't have
  these" prompt emitted always, or only when Keeper is on? Align delivery to
  whatever the prompt claims, in **both** Keeper states. Do not ship a state where
  the prompt says "withheld" and the file exists.

**Acceptance.**
- Test (red first): with Keeper enabled and a `SECRET` assigned, assert the
  cleartext file is **absent** from the agent container's `/secrets/<agent>/` while
  the agent can still obtain it via `/keeper/execute`.
- Test: with Keeper disabled, the file is present (unchanged behaviour) AND the
  prompt does not claim it's withheld.
- Runtime harness: extend the credential suite (`scripts/test-harness/`) to cover
  the Keeper-on no-file case.
- Docs: note the delivery contract in the credentials guide.

---

## Task 4 — Auth-on by default for crew-created datastores

**Why.** A crew-created Redis/Postgres can run **without a password**, and every
container on the shared crew bridge can reach it by DNS. So for a passwordless
datastore, network reachability == full access — the credential grant (the real
permission primitive) protects nothing. Fix: CrewShip-created datastores must
always be provisioned **with** an auth secret, delivered through the existing
credential system. Closes "any agent reaches Redis" via auth, with **no network
work**.

**Where (verified).**
- Service container creation + env injection:
  `internal/provider/docker/sidecar.go:282` (image), `:289-300` (volumes),
  `:303-306` (env `KEY=VALUE`), `:379-389` (hardening).
- Service env resolved from `env_refs` → credentials:
  `internal/chatbridge/services.go:28` (`env_refs`), `:50-59` (resolution),
  `:139-147` (`buildServiceEnvLookup` → `byName[c.EnvVarName] = c.PlainValue`).
- Declared services: `crews.services_json` (v95),
  `internal/database/migrate_consts_v95_crew_services.go`.
- Credential storage (reuse): `internal/encryption/encryption.go:121` (AES-256-GCM,
  `ENCRYPTION_KEY`); credential types incl. `SECRET`/`GENERIC_SECRET`
  (`internal/database/migrate_consts_v94_credential_vault_types.go`).
- Per-agent grant table: `internal/api/agent_credentials.go` (assignment,
  admin-only `canRole "manage"`).

**What to do.**
- When CrewShip provisions a datastore and no auth secret is set, **generate one**
  (strong random) and inject it per engine:
  - Redis → `--requirepass` / `REDIS_PASSWORD` (as the image expects),
  - Postgres → `POSTGRES_PASSWORD`,
  - MySQL → `MYSQL_ROOT_PASSWORD`/user password,
  - Mongo → root user/password.
- Store the generated secret as a **credential** (encrypted vault, existing
  system) and wire it as the service's `env_ref`, so the connection string an
  agent receives already carries auth.
- Default owner attribution: assign the credential to the crew/owning agent via
  the existing `agent_credentials` path (admin-managed).
- Backward compat: existing services that already declare an auth `env_ref` are
  left as-is; only fill the gap when auth is absent. Provide a migration/gate so
  existing passwordless crews get a generated secret on next reconcile (log it).

**Acceptance.**
- Test (red first): create a crew Redis with no auth configured → the running
  container requires a password, and the password exists as an encrypted
  credential (not plaintext in `services_json`).
- Test: an agent **without** the datastore credential cannot authenticate to it
  (even though it can resolve the DNS name — proving auth, not network, is the gate
  in default/open mode).
- Runtime harness: extend the credential/datastore suite.
- Docs: "Datastores are always password-protected" note in the services guide.

**Out of scope (park):** network segmentation / per-service networks / egress
policy — that's the future "locked crew" mode, not this iteration. Auth-on via the
credential system is the small, correct win now.

---

## Explicitly NOT in this iteration (parked in agent-identity-signing.md)
Per-agent containers · crew-as-environment (network + volume + service lifecycle) ·
egress / zero-trust enforcement · wire encryption (mTLS) · making Keeper the gate ·
per-agent tool scoping. These are multi-week reworks of load-bearing paths and are
**not** required for a value/adoption-driven 1.0. Keep them in the PRD as future
direction.
