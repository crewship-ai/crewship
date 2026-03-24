# CLAUDE.md — Crewship Development Rules

## Git Workflow — MANDATORY

**NIKDY nepracuj přímo na `main` branch.** Vždy vytvoř feature branch:

```bash
git checkout -b feat/<popis-zmeny>
```

- Commituj průběžně — nekompletní práce v working tree je ztráta čekající na to, až se stane
- Po dokončení práce vytvoř PR z feature branch do `main`
- `main` branch je chráněný — žádné přímé commity, žádné force-pushe
- Pokud běží víc Claude sessions paralelně, každá MUSÍ mít vlastní branch (jinak si navzájem mažou uncommitted práci)

**Proč:** Uncommitted změny na `main` může jiná session zahodit přes `git checkout -- .` nebo `git clean -fd`. Feature branch toto řeší.

## Tech Stack

- **Backend:** Go 1.26, SQLite (`modernc.org/sqlite`, driver name `"sqlite"` — NIKDY `"sqlite3"`)
- **Frontend:** Next.js 16, React, TypeScript, Tailwind CSS 4, shadcn/ui
- **Kontejnery:** Docker, 1 container = 1 crew, agent-runtime image
- **IPC:** HTTP-over-Unix-socket, auth via X-Internal-Token
- **Build:** `make build` → Next.js static export → Go embed → single binary
- **Dev:** `./dev.sh start` (Go :8081 + Next.js :3001)
- **DB:** `./crewship.db` (dev, CWD-relative), `~/.crewship/crewship.db` (default)
- **Package manager:** `pnpm` only (NIKDY npm/yarn)

## Architecture Rules

- API routes POUZE v `internal/api/`, NIKDY v `app/` (static export je rozbije)
- Go migrations v `internal/database/migrate.go`, NE Prisma migrate
- GCM byte layout: `IV||AuthTag||Ciphertext` — neměnit
- Sidecar UID 1002, agent UID 1001 — bezpečnostní hranice, neměnit
- Credential encryption format: `v1:{base64}` — neměnit
- ES modules only v TypeScriptu, NIKDY `require()`/CommonJS

## CLI

- `./crewship` je hlavní CLI binary (build přes `go build -o crewship ./cmd/crewship/`)
- Seed skript: `scripts/setup-shipfast.sh http://localhost:8081`
- Prisma seed: `pnpm db:seed` (pro TypeScript side)
- Crew ikony: lucide icon names (`code`, `rocket`, `clipboard`...), NE emoji
- Crew barvy: palette ID (`blue`, `emerald`, `violet`, `amber`, `rose`, `cyan`, `lime`, `fuchsia`), NE hex

## Testing

- Go: `go test ./... -count=1 && go vet ./...`
- Frontend: `npx tsc --noEmit` (type check), `pnpm lint`, `pnpm build`
- Vždy spusť testy před commitem
- Při přidávání metody do interface (`ContainerProvider` apod.) — updatuj VŠECHNY mock typy v test souborech

## Agent Creation — Credentials

- Agenti vytvoření z template, Captainem, nebo přes internal API dostávají credentials automaticky (`autoAssignCredentials`)
- Agenti vytvoření přes CLI/UI je přiřazují ručně (`crewship credential assign`)
