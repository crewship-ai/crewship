# Crewship -- Dependencies (DEPENDENCIES.md)

**Verze:** 3.0
**Datum:** 2026-02-17
**Autor:** Pavel Srba + AI analyza
**Status:** Aktualizovano 2026-02-17: Odstranen Redis/BullMQ/ws/pino, pridany Go deps, SQLite planovany.

**Package manager:** pnpm 10.x
**Runtime:** Go 1.25 (production runtime)
**Node.js:** 25.x (build-time only -- Next.js static export, Prisma type generation)
**Go:** 1.25

---

## 1. PREHLED

### Principy

1. **Minimalni zavislosti** -- kazda dependency musi mit jasny duvod
2. **Pinned verze** -- `pnpm-lock.yaml` committed, `--frozen-lockfile` v CI
3. **Zadne duplicity** -- jeden nastroj per ucel (napr. jen Zod pro validaci, ne Yup)
4. **Licence check** -- zadne copyleft (GPL) v enterprise mode
5. **Security audit** -- `pnpm audit` v CI, fail na critical
6. **Dve jazykova prostredi** -- TypeScript (Next.js) pro UI/CRUD/auth, Go (crewshipd) pro infra/runtime

### Kategorie

| Kategorie | Pocet | Popis |
|---|---|---|
| Core (framework) | 6 | Next.js, React, Prisma, Zod, Zustand, Tailwind |
| Auth & Security | 4 | NextAuth, CASL, bcrypt, @auth/prisma-adapter |
| UI | 12 | shadcn/ui, lucide-react, clsx, tailwind-merge, radix-ui, cmdk, motion, embla, next-themes, shiki, streamdown, ansi-to-react |
| AI / Chat | 3 | ai (Vercel AI SDK), use-stick-to-bottom, tokenlens |
| Flow / Diagram | 1 | @xyflow/react |
| Utility | 2 | nanoid, pg |
| Dev | 16 | TypeScript, Vitest, ESLint, Prisma CLI, PostCSS, testing-library, ... |
| Go (crewshipd) | 5 | Docker SDK, bbolt, fsnotify, go-jose, yaml |
| CLI tools (agent-runtime) | 4 | Claude Code, OpenCode, Codex CLI, Gemini CLI |

> **Poznamka:** Node.js NEOBSAHUJE zadne infrastrukturni deps (zadne WebSocket, job queue, logging, Docker).
> Veskerá infra je v Go service `crewshipd`.

---

## 2. TYPESCRIPT PRODUCTION DEPENDENCIES

### 2.1 Core Framework

| Balicek | Verze | Ucel | Licence |
|---|---|---|---|
| `next` | ^16.1.6 | Framework (App Router, RSC, Turbopack) | MIT |
| `react` | ^19.2.4 | UI knihovna | MIT |
| `react-dom` | ^19.2.4 | React DOM renderer | MIT |
| `@prisma/client` | ^7.4.0 | Type generation only (NOT runtime ORM -- Go accesses DB directly) | Apache-2.0 |
| `@prisma/adapter-pg` | ^7.4.0 | ~~Runtime dep~~ Build-time only (Prisma type gen) | Apache-2.0 |
| `zod` | ^4.3.6 | Runtime validace | MIT |
| `zustand` | ^5.0.11 | Client state management | MIT |

### 2.2 Auth & Security

| Balicek | Verze | Ucel | Licence |
|---|---|---|---|
| `next-auth` | 5.0.0-beta.30 | ~~Runtime auth~~ Client-side session only (auth handled by Go backend) | ISC |
| `@auth/prisma-adapter` | ^2.11.1 | ~~Runtime~~ No longer used at runtime (Go handles auth directly) | ISC |
| `@casl/ability` | ^6.8.0 | ~~Runtime RBAC~~ No longer used (Go middleware handles RBAC) | MIT |
| `@casl/prisma` | ^1.6.1 | ~~Runtime~~ No longer used (Go handles authorization) | MIT |
| `bcryptjs` | ^3.0.3 | ~~Runtime~~ No longer used at runtime (Go uses bcrypt directly) | MIT |

### 2.3 UI

| Balicek | Verze | Ucel | Licence |
|---|---|---|---|
| `lucide-react` | ^0.564.0 | Ikony (JEDINA povolena ikonova knihovna) | ISC |
| `clsx` | ^2.1.1 | Conditional CSS classes | MIT |
| `tailwind-merge` | ^3.4.1 | Merge Tailwind classes (cn() util) | MIT |
| `class-variance-authority` | ^0.7.1 | Varianty komponent (shadcn/ui) | Apache-2.0 |
| `next-themes` | ^0.4.6 | Dark mode provider (class strategy) | MIT |
| `radix-ui` | ^1.4.3 | Headless UI primitives (shadcn/ui zaklad) | MIT |
| `@radix-ui/react-use-controllable-state` | ^1.2.2 | Radix utility hook | MIT |
| `cmdk` | ^1.1.1 | Command palette (⌘K) | MIT |
| `motion` | ^12.34.0 | Animace (Framer Motion) | MIT |
| `embla-carousel-react` | ^8.6.0 | Carousel komponenta | MIT |
| `tw-animate-css` | ^1.4.0 | Tailwind animace (nahradi deprecated tailwindcss-animate) | MIT |

> **shadcn/ui** neni npm balicek -- je to kolekce komponent kopirovanych do `components/ui/`.
> Styl: **new-york**. Pouziva Tailwind CSS 4, CSS-first konfigurace.

### 2.4 AI / Chat

| Balicek | Verze | Ucel | Licence |
|---|---|---|---|
| `ai` | ^6.0.86 | Vercel AI SDK (streaming, chat UI hooks) | Apache-2.0 |
| `use-stick-to-bottom` | ^1.1.3 | Auto-scroll v chatovem okne | MIT |
| `tokenlens` | ^1.3.1 | Token counting / visualization | MIT |

### 2.5 Rendering & Formatting

| Balicek | Verze | Ucel | Licence |
|---|---|---|---|
| `shiki` | ^3.22.0 | Syntax highlighting (code bloky) | MIT |
| `streamdown` | ^2.2.0 | Streaming markdown rendering | MIT |
| `@streamdown/cjk` | ^1.0.2 | Streamdown CJK plugin | MIT |
| `@streamdown/code` | ^1.0.2 | Streamdown code plugin | MIT |
| `@streamdown/math` | ^1.0.2 | Streamdown math plugin | MIT |
| `@streamdown/mermaid` | ^1.0.2 | Streamdown mermaid plugin | MIT |
| `ansi-to-react` | ^6.2.6 | ANSI escape kody → React komponenty (terminaly) | MIT |
| `media-chrome` | ^4.17.2 | Media player web components | MIT |

### 2.6 Flow / Diagram

| Balicek | Verze | Ucel | Licence |
|---|---|---|---|
| `@xyflow/react` | ^12.10.0 | Flow diagram editor (orchestrace agentů) | MIT |

### 2.7 Utility

| Balicek | Verze | Ucel | Licence |
|---|---|---|---|
| `nanoid` | ^5.1.6 | Kratke unikatni ID (request ID, tokeny) | MIT |
| `pg` | ^8.18.0 | ~~Runtime~~ No longer used at runtime (Go accesses DB directly) | MIT |

### 2.8 Business (Phase 2+, zatim neinstalovano)

| Balicek | Verze | Ucel | Licence |
|---|---|---|---|
| `stripe` | latest | Billing a subscriptions | MIT |
| `resend` | latest | Transakcni emaily | MIT |

> Tyto balicky budou pridany az v Phase 2 pri implementaci billing/email.

---

## 3. TYPESCRIPT DEV DEPENDENCIES

### 3.1 TypeScript & Build

| Balicek | Verze | Ucel | Licence |
|---|---|---|---|
| `typescript` | ^5.9.3 | TypeScript compiler (strict mode) | Apache-2.0 |
| `@types/node` | ^25.2.3 | Node.js type definitions | MIT |
| `@types/react` | ^19.2.14 | React type definitions | MIT |
| `@types/react-dom` | ^19.2.3 | React DOM type definitions | MIT |
| `@types/pg` | ^8.16.0 | PostgreSQL klient typy | MIT |
| `prisma` | ^7.4.0 | Prisma CLI (generate, migrate, push) | Apache-2.0 |
| `tsx` | ^4.21.0 | TypeScript execution (seed skripty) | MIT |
| `dotenv-cli` | ^11.0.0 | Dotenv pro CLI prikazy (prisma) | MIT |
| `globals` | ^17.3.0 | Global variable definitions pro ESLint | MIT |

### 3.2 Testing

| Balicek | Verze | Ucel | Licence |
|---|---|---|---|
| `vitest` | ^4.0.18 | Unit + integracni testy | MIT |
| `happy-dom` | ^20.6.1 | DOM simulace pro testy | MIT |
| `@testing-library/react` | ^16.3.2 | React testing utilities | MIT |
| `@testing-library/jest-dom` | ^6.9.1 | DOM matchers pro testy | MIT |
| `@vitejs/plugin-react` | ^5.1.4 | React plugin pro Vite/Vitest | MIT |

### 3.3 Linting

| Balicek | Verze | Ucel | Licence |
|---|---|---|---|
| `eslint` | ^9.39.2 | Linter | MIT |
| `@typescript-eslint/eslint-plugin` | ^8.55.0 | ESLint TypeScript plugin | MIT |
| `@typescript-eslint/parser` | ^8.55.0 | ESLint TypeScript parser | MIT |
| `eslint-plugin-react` | ^7.37.5 | React linting pravidla | MIT |
| `eslint-plugin-react-hooks` | ^7.0.1 | React hooks linting | MIT |

### 3.4 PostCSS & CSS

| Balicek | Verze | Ucel | Licence |
|---|---|---|---|
| `tailwindcss` | ^4.1.18 | CSS framework (CSS-first config, @theme inline, Tailwind 4) | MIT |
| `@tailwindcss/postcss` | ^4.1.18 | Tailwind v4 PostCSS plugin | MIT |
| `postcss` | ^8.5.6 | CSS post-processor | MIT |

---

## 4. GO DEPENDENCIES (crewshipd)

> Go service `crewshipd` -- WebSocket, Docker orchestration, logs, files, webhooks.
> Managed via `go.mod`. Prefer stdlib -- minimalni externé dependencies.

### 4.1 Primé dependencies (require)

| Modul | Verze | Ucel | Licence |
|---|---|---|---|
| `github.com/docker/docker` | v28.5.2 | Docker SDK -- container lifecycle, exec, logs | Apache-2.0 |
| `go.etcd.io/bbolt` | v1.4.3 | Embedded KV store (WAL, durable job state) | MIT |
| `github.com/fsnotify/fsnotify` | v1.9.0 | inotify file watcher (/output/ zmeny) | BSD-3 |
| `golang.org/x/net` | v0.50.0 | Stdlib extension -- `x/net/websocket` server | BSD-3 |
| `gopkg.in/yaml.v3` | v3.0.1 | YAML config parsing (crewshipd.yaml) | MIT |

### 4.2 Neprime dependencies (indirect, vyznamne)

| Modul | Verze | Ucel | Licence |
|---|---|---|---|
| `github.com/go-jose/go-jose/v4` | v4.1.3 | JOSE/JWT -- NextAuth token validace v Go | Apache-2.0 |
| `golang.org/x/crypto` | v0.48.0 | Kryptografie (HKDF pro JWT derivaci) | BSD-3 |
| `golang.org/x/time` | v0.14.0 | Rate limiting (x/time/rate) | BSD-3 |
| `go.opentelemetry.io/otel` | v1.40.0 | OpenTelemetry tracing (Docker SDK dep) | Apache-2.0 |
| `github.com/docker/go-connections` | v0.6.0 | Docker connection utilities | Apache-2.0 |

### 4.3 Stdlib -- klicove pouzite packages

> Go service preferuje stdlib. Nasledujici jsou pouzite BEZ externich deps:

- `log/slog` -- strukturovane JSON logging (nahradi pino)
- `net/http` -- HTTP server + routes (nahradi Express/Fastify)
- `crypto/hmac`, `crypto/sha256` -- webhook HMAC validace
- `encoding/json` -- JSON serialization
- `context` -- context propagation na vsech funkcich

---

## 5. CLI NASTROJE (AGENT RUNTIME IMAGE)

> Tyto balicky se instaluji GLOBALNE v `crewship/agent-runtime` Docker image.
> VZDY pinovat konkretni verze -- nikdy `latest`.

| Balicek | Verze | Ucel | Licence | Zdroj |
|---|---|---|---|---|
| `@anthropic-ai/claude-code` | pinned | Claude Code CLI adapter | Proprietary | npm |
| `opencode` | pinned | OpenCode CLI adapter (75+ provideru) | MIT | GitHub release |
| `@openai/codex` | pinned | Codex CLI adapter | MIT | npm |
| `@google/gemini-cli` | pinned | Gemini CLI adapter (experimentalni) | Apache-2.0 | npm |

> **Verze se pinuji v Dockerfile** (viz DEPLOYMENT.md sekce 4.4).
> Update postup: novy release → update Dockerfile → build → test → rolling deploy.

### Phase 2 SDK (volitelne)

| Balicek | Verze | Ucel | Licence |
|---|---|---|---|
| `@opencode-ai/sdk` | latest | OpenCode TypeScript SDK (HTTP server mod) | MIT |
| `@openai/codex-sdk` | latest | Codex App Server SDK (JSON-RPC) | MIT |

---

## 6. INFRASTRUKTURNI ZAVISLOSTI

### 6.1 Docker images

| Image | Verze | Ucel |
|---|---|---|
| `node:22-bookworm-slim` | 22 LTS | Zaklad pro Crewship frontend image |
| `golang:1.25` | 1.25 | Build image pro crewshipd |
| `postgres:16-alpine` | 16.x | PostgreSQL databaze (docker-compose) |

> **Zadne Redis!** Job queue + PubSub + cache jsou reseny v Go (bbolt + in-memory).

### 6.2 System packages (agent-runtime image)

| Balicek | Ucel |
|---|---|
| `ca-certificates` | TLS certifikaty pro HTTPS |
| `git` | Git operace v workspace (agenti) |
| `curl` | HTTP requesty (health checks, CLI install) |
| `jq` | JSON parsovani v shell skriptech |

---

## 7. SINGLE BINARY DEPENDENCIES (IMPLEMENTOVANO)

> Single binary architektura je AKTUALNI stav -- Go binary s embedded Next.js static export.

| Dependency | Verze | Ucel | Typ | Status |
|---|---|---|---|---|
| `modernc.org/sqlite` | latest | Pure-Go SQLite driver (no CGO) | Go modul | ✅ Implementovano |
| `embed.FS` | stdlib | Embedded Next.js export v Go binary | Go stdlib | ✅ Implementovano |
| `goreleaser` | latest | Multi-platform release (macOS, Linux, Windows) | CI tool | ✅ Konfigurovano |

> **SQLite je default databaze (implementovano).**
> Default = SQLite (single binary, zero config, `crewship start` just works).
> PostgreSQL = optional pro scaling (multi-replica, production).
> Obe varianty pres provider pattern (`CREWSHIP_DB_PROVIDER=sqlite|postgres`).

---

## 8. VERZE MATRIX -- KOMPATIBILITA

| Komponenta | Vyzadovana verze | Duvod |
|---|---|---|
| Node.js | 25.x (engines >=22) | Build-time only (Next.js static export, Prisma type gen) |
| pnpm | 10.x | Corepack, workspace podpora |
| Go | 1.25 | Generics, slog, latest stdlib |
| Docker Engine | 24+ | Buildx, compose v2, security features |
| PostgreSQL | 16+ | `gen_random_uuid()`, performance |
| Next.js | 16.x | App Router RSC, Turbopack |
| React | 19.x | Server Components, use() |
| TypeScript | 5.9.x | Strict mode, satisfies |
| Prisma | 7.x | TS-native (bez Rust engine), pg adapter |
| Zod | 4.x | JSON Schema support, 14x rychlejsi |
| Tailwind CSS | 4.x | CSS-first config, @theme inline, oklch |

---

## 9. SECURITY AUDIT

### 9.1 Kriticke dependencies

| Dependency | Riziko | Mitigace |
|---|---|---|
| `@prisma/client` | SQL injection (nepravdepodobne) | Parametrizovane dotazy, zadne `$executeRaw` |
| `next` | RSC/middleware exploity | Vzdy latest patch verze |
| `@casl/ability` | Logic error = auth bypass | Unit testy na ability matici |
| `next-auth` | Token forgery | NEXTAUTH_SECRET rotace, short expiry |
| `docker SDK (Go)` | Container escape | Docker security options, non-root UID 1001 |
| `stripe` (Phase 2) | Payment fraud | Webhook signature overovani (HMAC-SHA256) |
| `go-jose` | JWT vulnerabilities | Pinned verze, algorithm restriction |

### 9.2 CI audit pipeline

```bash
# Kazdý commit (CI):
pnpm audit --audit-level=critical    # fail na critical vulnerabilities
pnpm licenses list --json            # export licenci

# Kazdý tyden (GitHub Dependabot):
# Automaticke PR pro security updates

# Go dependencies:
go vet ./...                          # static analysis
govulncheck ./...                     # Go vulnerability database

# Kazdý Docker build:
# Trivy scan na Docker image (viz DEPLOYMENT.md)
```

### 9.3 Licence kontrola

| Licence | Povoleno | Poznamka |
|---|---|---|
| MIT | Ano | Vetsina dependencies |
| Apache-2.0 | Ano | Prisma, CVA, Docker SDK |
| ISC | Ano | NextAuth, lucide-react |
| BSD-2/3 | Ano | Go stdlib extensions |
| GPL-2.0/3.0 | **NE** | Copyleft, nekompatibilni s enterprise |
| AGPL | **NE** | Server-side copyleft |
| Proprietary | Opatrne | Claude Code CLI (ok, jen v runtime image) |

---

## 10. PROC NE -- ODMITNUTE ALTERNATIVY

| Alternativa | Proc NE |
|---|---|
| **BullMQ + Redis** (job queue) | Go service (`crewshipd`) resi job orchestraci primo -- bbolt pro state, goroutines pro concurrency. Redis = zbytecna infrastruktura. |
| **ws (npm)** (WebSocket) | Go nativni `x/net/websocket` -- rychlejsi, mene overhead, jednotna codebaze. |
| **pino** (logging) | Go `log/slog` -- strukturovane JSON logging, stdlib, zadna dependency. |
| **dockerode** (Docker SDK) | Go `docker/docker` SDK -- type-safe, streaming, nativni v Go service. |
| **Socket.IO** (misto ws) | Vetsi overhead, nepotrebujeme polling fallback. Go ws staci. |
| **tRPC** (misto REST) | Vendor lock-in, slozitejsi pro verejne API (Phase 2). |
| **Drizzle** (misto Prisma) | Mensi ekosystem, Prisma uz zavedene v projektu. |
| **Valibot/ArkType** (misto Zod) | Mensi komunita, mene integrace. |
| **Material-UI/Chakra/Ant** (misto shadcn) | Tezke, opinionated. shadcn = kopirovane komponenty, plna kontrola. |
| **Bun** (misto Node.js) | Mladsi ekosystem, riziko nekompatibility. |
| **Express/Fastify** (misto Next.js API) | Dve codebasy, Next.js API Routes staci pro CRUD. |
| **Passport.js** (misto NextAuth) | Starsi, vice boilerplate. NextAuth (Auth.js) je modernijsi. |
| **Sentry** (v MVP) | Pridava slozitost, Go slog + Next.js console staci pro MVP. |
| **PostHog** (v MVP) | Pridava slozitost, neni kriticke pro launch. |
| **Turborepo** (monorepo) | Overengineering pro solo dev, jeden repo staci. |
| ~~**SQLite v MVP**~~ | ~~Odmitnuto~~ **SQLite je AKTUALNE default databaze** pro single binary. PostgreSQL je opt-in. |
| **@supabase/ssr** | Nepouzivame Supabase -- Prisma + vlastni PostgreSQL. |
| **Tailwind 3.x** | Tailwind 4 je CSS-first, lepsi performance, @theme inline. Zadny tailwind.config.ts. |

---

*Aktualizovano 2026-02-17. Plne reflektuje skutecny stav package.json, go.mod a AGENTS.md.*
