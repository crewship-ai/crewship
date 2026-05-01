# CLAUDE.md — Crewship Development Rules

## Verify after every change

```bash
go test ./... -count=1 && go vet ./...   # Go — must pass
pnpm lint && pnpm build                  # Frontend — must pass for UI changes
```

## Anti-patterns (things agents consistently get wrong)

- **Driver name is `"sqlite"`, not `"sqlite3"`** — `modernc.org/sqlite` registers as `"sqlite"`.
- **Never run `prisma migrate`** — Prisma is TS type generation only (`pnpm db:generate`). All DB migrations are Go-only in `internal/database/migrate.go`.
- **Never add API routes to `app/`** — static export silently drops them. They work in dev, break in prod. All API routes go in `internal/api/`.
- **GCM byte layout is `IV||AuthTag||Ciphertext`** — custom order for Go/TS compat. Changing it breaks all stored credentials.
- **Sidecar UID 1002, agent UID 1001** — security boundary. Do not change.
- **`pnpm` only** — never `npm` or `yarn`.
- **No `interface{}` slices** — use typed slices in Go.
- **No `Co-Authored-By`** in commits.
- **No `require()` / CommonJS** in frontend — ES modules only.
- **Never amend commits after pre-commit hook failure** — create a new commit.
- **Never `git checkout .` or `git clean` on WIP** — always stash first.
- **`NEXTAUTH_SECRET` MUST be set on the Crewship server** — otherwise the entire API router AND static UI handler are silently skipped. In `internal/server/server.go`, the guard `if deps != nil && deps.DB != nil && cfg.Auth.JWTSecret != ""` gates both API-router creation (`mux.Handle("/api/", apiRouter)`) and SPA handler setup (`s.spaHandler = goapi.StaticFileHandler(deps.WebFS)`). When missing, the only user-visible signal is `WARN NEXTAUTH_SECRET not set, WebSocket auth disabled`. Symptom: every route returns 404, no ERROR log. Healthy startup logs both `Go API routes mounted` and `serving embedded static UI` — confirm via `journalctl -u prod-server | grep -E "API routes mounted|serving embedded"`.
- **`make build` end-to-end, not piecemeal.** Sequence is `pnpm build` → `rm -rf web/out && cp -r out web/out` → `go build`. Skipping the `cp -r` (e.g. running `pnpm build` followed directly by `go build`) leaves `web/out/` stale; the Go embed FS then drifts ~100+ files out of sync with Next.js output and routes 404 unpredictably.

## Remote environments (dev + prod)

All development and dogfood production happen on remote Proxmox VMs via SSH. Never build or run services locally on the Mac Mini.

### `dev-server` — VMID 300 (development)

- **Connect:** `ssh dev-server` (alias for `ubuntu@10.0.0.1`)
- **DNS:** `crewship.example.com` → 10.0.0.1
- **Repo path:** `/opt/crewship`
- **Backend:** `http://crewship.example.com:8080` (or http://10.0.0.1:8080)
- **Frontend:** `http://crewship.example.com:3001` (or http://10.0.0.1:3001)
- **Resources:** 12 vCPU, **48 GB RAM** (balloon 24 GB, reduced from 64 GB on 2026-04-27 — peak usage was ~29 GB), 200 GB NVMe
- **Tracks:** `main` branch (always-bleeding-edge); started via `./dev.sh start` in tmux
- **VS Code / Cursor:** `code --remote ssh-remote+dev-server /opt/crewship`
- Go PATH on the server requires: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin` (already in `.bashrc`)
- **Multi-instance (`dev 1` / `dev 2` / `dev 3`):** parallel checkouts on the **same VM** — they differ by directory and port, NOT by IP/host. All four reach via `ssh dev-server` and share `10.0.0.1`. Layout:
  - `/opt/crewship` (default, often on `main`) — Go `:8080`, Next `:3001`
  - `/opt/crewship_1` — Go `:8081`, Next `:3011`
  - `/opt/crewship_2` — Go `:8082`, Next `:3012`
  - `/opt/crewship_3` — Go `:8083`, Next `:3013`
  - Port formula in `dev.sh`: instance 0 = 8080/3001 (default); instance N≥1 = `8080+N` / `3010+N` (note the 9-port gap between default Next 3001 and instance-1 Next 3011).
  - Each instance has its own `.env.local`, `crewship.db`, and tmux session — they're independent enough to run different feature branches in parallel for testing/PR review. The branches checked out in `_1` / `_2` / `_3` are typically WIP — see "Dev VM hosts feature branches" rule before any `git checkout`/`pull`/`stash`.
- Tags: `crewship`, `dev`, `environment-development`

### `prod-server` — VMID 301 (dogfood production)

- **Connect:** `ssh prod-server` (alias for `ubuntu@10.0.0.2`)
- **DNS:** `crewship.example.com` → 10.0.0.2
- **Repo path:** `/opt/prod-server`
- **Backend / Frontend (embedded):** `http://crewship.example.com:8080` (or http://10.0.0.2:8080)
- **Resources:** 4 vCPU, 16 GB RAM (balloon 8 GB), 60 GB local-lvm disk
- **OS:** Ubuntu 24.04.4 LTS cloud-init image, Docker 29.4.1 native (overlayfs, cgroup v2)
- **Tracks:** `release` branch (created from main on 2026-04-27). Push there to deploy: `git push origin main:release`. systemd timer polls every 5 min and rebuilds if SHA changed. Rollback: `git push -f origin <good-sha>:release`.
- **systemd units:** `prod-server.service` (server), `crewship-deploy.timer` (5-min poll → `/opt/prod-server/deploy.sh`)
- **Env file:** `/etc/crewship/prod-server.env` (mode 0600, root-owned). Contains `NEXTAUTH_SECRET`, `ENCRYPTION_KEY` (env-unique, NOT shared with dev), `CREWSHIP_ENV=production`, etc.
- **Storage:** DB at `/opt/prod-server/data/crewship.db`, localfs provider at `/var/lib/crewship`
- **Deploy SSH key:** GitHub Deploy Key on `crewship-ai/crewship` (read-only) so the timer can `git pull` without agent forwarding
- **Why VM, not LXC:** unprivileged LXC + Docker fails at first `docker run` (`runc create failed: open sysctl ip_unprivileged_port_start: permission denied`). Privileged LXC would fix it but doesn't match what real customers run (~70 % self-host on cloud VMs with native Docker). Native VM matches Tier 1 customer reality. ~1.5 GB RAM overhead is the price.
- **Network isolation:** Proxmox firewall on net0 (`/etc/pve/firewall/301.fw`). Default ACCEPT in/out, but explicit `OUT DROP` rules block crossover to `.101` (truenas/minio), `.200` (coolify), `.201` (dev-server), `.221` (MBA runner), `.230` (truenas alt-IP), `.251` (proxmox host). Internet (GitHub, LLM APIs) and gateway/DNS (`.1`) remain reachable. SSH from LAN unaffected. Tested: `ping .201` from prod VM = blocked, `curl https://github.com` = OK.
- Tags: `crewship`, `prod`, `environment-production`

### Other Proxmox VMs (non-Crewship)

- VMID 103 `truenas` (storage NAS)
- VMID 200 `coolify` (self-hosted PaaS, also catches `*.example.com` wildcard for undefined subdomains)
- Proxmox host: `ssh proxmox` (alias for `root@10.0.0.251`, DNS `proxmox.example.com`)

## Crew Journal architecture (added 2026-04, shipped on feat/crew-journal)

Single append-only event stream (`journal_entries` table, migration 52) is the canonical source of truth for every observable action. All new platform surfaces are read-models or middleware over this one stream.

Package layout:
- `internal/journal/` — Emit API + batched writer + typed entry catalog. Router exposes `Router.Journal()` getter; handlers call `h.journal.Emit(ctx, Entry{...})` without nil-checking (noopEmitter default).
- `internal/paymaster/` — LLM cost tracking + hierarchical budget enforcement (workspace → crew → mission → agent). Writes `cost_ledger`, emits `llm.call` + `cost.incurred` + `budget.exceeded` entries.
- `internal/lookout/` — guardrails: prompt injection detect, tool arg JSON-schema validation, output parser, secrets redaction. Emits `guardrail.input_blocked` / `guardrail.output_blocked`.
- `internal/harbormaster/` — HITL approval queue. Gate() with Mode none/async/sync; sync polls `approvals_queue` until decided or timed out. Emits `approval.requested/granted/denied/timeout`.
- `internal/cartographer/` — checkpoints + fork + restore over journal cursor. Restore is READ-ONLY (returns divergence warnings); actual state rewind is UX decision in the handler.
- `internal/hooks/` — lifecycle intercept framework (shell/http/subagent handlers, 15 event types). Shell requires `allowedShell=true` at register time.
- `internal/quartermaster/` — trajectory eval + regression detection + LLM-as-judge with rubric-shuffle anti-bias. Provider-neutral `JudgeInterface`.
- `internal/reflection/` — role-based reflection (Logician/Skeptic/DomainExpert critiques → synthesized via quartermaster judge) + Evaluator-Optimizer loop.
- `internal/episodic/` — vector recall over journal (SQLite BLOB brute-force cosine; no pgvector). Selective embedding only: peer.escalation, summary, denied keeper, failed/completed mission, eval regression. NEVER embed exec.output_chunk/metrics/network (prevents memory drift).
- `internal/presence/` — agent Watch Roster (online/busy/blocked/offline). Upsert emits `agent.status_change` only on actual transition.
- `internal/consolidate/` — daily workers: Consolidator extracts semantic rules from journal → `.memory/topics/learned-YYYY-MM-DD.md`; Compactor kompaktuje low-signal entries older than 30 days, emits `system.compaction`.
- `internal/telemetry/` — OTel GenAI spans with W3C trace context propagation. `RegisterJournalResolver()` wires `journal.SetTraceResolver` so every entry carries trace_id/span_id.
- `internal/containerstate/` — captures the *actual* installed-package state of crew containers (apt + pip + npm + os-release) via short Exec probes. Orchestrator calls `recordContainerSnapshot` after every successful agent run; hash-based dedup means a quiet session writes nothing. Emits `container.snapshot` entries — devcontainer.json is declared intent, this is what the container actually has after agents ran apt-get / pip install during a session.
- `internal/llm/middleware.go` — unified stack: `telemetry → paymaster → lookout → raw provider`. Compose via `llm.Middleware(base, j, db)`.

**Write path order is load-bearing.** paymaster outside lookout so a blocked call still records a partial ledger row (attempted-but-blocked audit). lookout outside raw so bad inputs never reach the LLM.

**UI surfaces**: `/journal` (workspace timeline), `/crows-nest/[crewId]` (live terminal + network + filesystem + resources, OWNER/ADMIN only), `/paymaster`, `/approvals`, `/eval`, `/missions/[id]/timeline`. All read from the same journal SSE stream; Crow's Nest filters on exec/network/file/metrics entry types.

**Legacy `standup_handler`** (migration 6 vintage) stays functional for existing sidecar + CLI callers but is deprecated — replaced by Crew Journal + optional LLM summary. Remove after all consumers migrate.

## Project-specific knowledge (not derivable from code)

- Single binary: `make build` → Next.js static export (`out/`) → `web/out/` → Go `//go:embed`. No Node.js at runtime.
- **Dev server: `./dev.sh start`** — starts Go backend + Next.js (+ PostgreSQL if configured). Other commands: `stop`, `restart`, `status`, `seed`, `nuke`, `logs`. Never start services manually.
- **Ollama models** are on the external SSD: `OLLAMA_MODELS="/Volumes/SSD 990 PRO/ollama-models"`. Start Ollama with this env var before `./dev.sh start` when testing Keeper.
- One container per crew (not per agent). `Exec`, not `Run`. Name: `crewship-team-{slug}`.
- IPC is HTTP-over-Unix-socket on `/tmp/crewship.sock`. Internal auth via `X-Internal-Token`.
- Credential encryption: versioned `"v1:{base64}"`, byte layout see above.
- Multi-instance: `crewship_N` dirs → Go `:8080+N`, Next.js `:3010+N`.

## Git workflow

- **Never work directly on `main`** — always create a feature branch (`git checkout -b feat/<description>`).
- Commit frequently — uncommitted work in the working tree is a loss waiting to happen.
- When multiple Claude sessions run in parallel, each MUST have its own branch (otherwise they destroy each other's uncommitted work).
- After completing work, create a PR from the feature branch into `main`.

## Crew and agent conventions

- Crew icons: lucide icon names (`code`, `rocket`, `clipboard`...), NOT emoji.
- Crew colors: palette ID (`blue`, `emerald`, `violet`, `amber`, `rose`, `cyan`, `lime`, `fuchsia`), NOT hex.
- Agents created from templates, by Captain, or via internal API get credentials auto-assigned (`autoAssignCredentials`).
- Agents created via CLI/UI assign credentials manually (`crewship credential assign`).
- When adding a method to an interface (`ContainerProvider`, etc.) — update ALL mock types in test files.
