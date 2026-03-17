# AGENTS.md

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

## Remote development server

All development happens on a **remote Proxmox VM** via SSH. Never build or run services locally on the Mac Mini.

- **Connect:** `ssh crewship-dev` (alias for `ubuntu@192.168.1.201`)
- **Repo path:** `/opt/crewship`
- **Backend:** `http://192.168.1.201:8080`
- **Frontend:** `http://192.168.1.201:3001`
- **Resources:** 12 vCPU, 64 GB RAM, 200 GB NVMe, Docker container provider
- **Start services:** `cd /opt/crewship && ./dev.sh start` (inside tmux to survive SSH disconnect)
- **VS Code / Cursor:** `code --remote ssh-remote+crewship-dev /opt/crewship`
- Go PATH on the server requires: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin` (already in `.bashrc`)

## Project-specific knowledge (not derivable from code)

- Single binary: `make build` → Next.js static export (`out/`) → `web/out/` → Go `//go:embed`. No Node.js at runtime.
- **Dev server: `./dev.sh start`** — starts Go backend + Next.js (+ PostgreSQL if configured). Other commands: `stop`, `restart`, `status`, `seed`, `nuke`, `logs`. Never start services manually.
- **Ollama models** are on the external SSD: `OLLAMA_MODELS="/Volumes/SSD 990 PRO/ollama-models"`. Start Ollama with this env var before `./dev.sh start` when testing Keeper.
- One container per crew (not per agent). `Exec`, not `Run`. Name: `crewship-team-{slug}`.
- IPC is HTTP-over-Unix-socket on `/tmp/crewship.sock`. Internal auth via `X-Internal-Token`.
- Credential encryption: versioned `"v1:{base64}"`, byte layout see above.
- Multi-instance: `crewship_N` dirs → Go `:8080+N`, Next.js `:3010+N`.
