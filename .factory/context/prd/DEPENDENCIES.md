# Crewship -- Dependencies (DEPENDENCIES.md)

**Verze:** 2.0
**Datum:** 2026-02-11
**Autor:** Pavel Srba + AI analyza
**Status:** Aktualizovano -- Tailwind v4, NextAuth, lokalni PostgreSQL

**Package manager:** pnpm (latest)
**Runtime:** Node.js 22 LTS
**Poznamka:** Vsechny verze "latest" -- pinnou se az pri `pnpm install`

---

## 1. PREHLED

### Principy

1. **Minimalni zavislosti** -- kazdá dependency musi mit jasny duvod
2. **Pinned verze** -- `pnpm-lock.yaml` committed, `--frozen-lockfile` v CI
3. **Advine reuse** -- vetsina dependencies uz je v Advine na spravnych verzich
4. **Zadne duplicity** -- jeden nastroj per ucel (napr. jen Zod pro validaci, ne Yup)
5. **Licence check** -- zadne copyleft (GPL) v enterprise mode
6. **Security audit** -- `pnpm audit` v CI, fail na critical

### Kategorie

| Kategorie | Pocet | Popis |
|---|---|---|
| Core (framework) | 6 | Next.js, React, Prisma, Zod, Zustand, Tailwind |
| Auth & Security | 4 | NextAuth, CASL, jose, bcrypt (CSRF je custom kod z Advine) |
| Infrastructure | 4 | BullMQ, ioredis, ws, pino |
| Business | 5 | Stripe, Resend, dockerode, bull-board (api + adapter) |
| UI | 4 | shadcn/ui, lucide-react, clsx, tailwind-merge |
| Dev | 8 | TypeScript, Vitest, ESLint, Prettier, ... |
| CLI tools (agent-runtime) | 4 | Claude Code, OpenCode, Codex CLI, Gemini CLI |

---

## 2. PRODUCTION DEPENDENCIES

### 2.1 Core Framework

| Balicek | Verze | Ucel | Licence | Zdroj |
|---|---|---|---|---|
| `next` | latest | Framework (App Router, RSC, Turbopack) | MIT | Advine |
| `react` | latest | UI knihovna | MIT | Advine |
| `react-dom` | latest | React DOM renderer | MIT | Advine |
| `@prisma/client` | latest | ORM -- JEDINY zpusob pristupu k DB | Apache-2.0 | Advine |
| `zod` | latest | Runtime validace | MIT | Advine |
| `zustand` | latest | Client state management | MIT | Advine |

### 2.2 Auth & Security

| Balicek | Verze | Ucel | Licence | Zdroj |
|---|---|---|---|---|
| `next-auth` | latest (Auth.js v5) | Autentizace (email+heslo, OAuth) | ISC | Novy |
| `@auth/prisma-adapter` | latest | NextAuth Prisma adapter | ISC | Novy |
| `@casl/ability` | latest | RBAC (role-based access control) | MIT | Advine |
| `@casl/prisma` | latest | CASL Prisma integrace (Phase 2) | MIT | Advine |
| `jose` | latest | JWT (WS token, overovani) | MIT | Advine |
| `bcryptjs` | latest | Password hashing (NextAuth) | MIT | Advine |

### 2.3 Infrastructure (Node.js)

> **WebSocket, Docker, logging, job queue** are handled by Go service (`crewshipd`).
> Node.js infrastructure deps are minimal.

| Balicek | Verze | Ucel | Licence | Zdroj |
|---|---|---|---|---|
| (none) | -- | Infra presunuta do Go service | -- | -- |

### 2.4 Business Logic

| Balicek | Verze | Ucel | Licence | Zdroj |
|---|---|---|---|---|
| `stripe` | latest | Billing a subscriptions (Phase 2+) | MIT | Advine |
| `resend` | latest | Transakcni emaily (Phase 2+) | MIT | Advine |

### 2.5 UI

| Balicek | Verze | Ucel | Licence | Zdroj |
|---|---|---|---|---|
| `tailwindcss` | 4.x (latest) | CSS framework (CSS-first config, @theme inline) | MIT | Novy |
| `lucide-react` | latest | Ikony (JEDINA povolena ikonova knihovna) | ISC | Advine |
| `clsx` | latest | Conditional CSS classes | MIT | Advine |
| `tailwind-merge` | latest | Merge Tailwind classes (cn() util) | MIT | Advine |
| `class-variance-authority` | latest | Varianty komponent (shadcn/ui) | Apache-2.0 | Advine |
| `next-themes` | latest | Dark mode provider (class strategy) | MIT | Novy |

> **shadcn/ui** neni npm balicek -- je to kolekce komponent kopirovanych do `components/ui/`.
> Styl: **new-york** (deprecated `default`). Pregenerovat po instalaci TW4.

### 2.6 Utility

| Balicek | Verze | Ucel | Licence | Zdroj |
|---|---|---|---|---|
| `date-fns` | latest | Datumove operace | MIT | Advine |
| `slugify` | latest | URL slug generovani | MIT | Novy |
| `nanoid` | latest | Kratke unikatni ID (request ID, tokeny) | MIT | Novy |
| `yaml` | 2.7.0 | YAML parser (skill templates, konfigurace) | ISC | Novy |

---

## 3. DEV DEPENDENCIES

### 3.1 TypeScript & Build

| Balicek | Verze | Ucel | Licence | Zdroj |
|---|---|---|---|---|
| `typescript` | 5.8.x | TypeScript compiler (strict mode) | Apache-2.0 | Advine |
| `@types/node` | 22.x | Node.js type definitions | MIT | Advine |
| `@types/react` | 19.x | React type definitions | MIT | Advine |
| `@types/react-dom` | 19.x | React DOM type definitions | MIT | Advine |
| `prisma` | latest | Prisma CLI (generate, migrate, push) | Apache-2.0 | Advine |

### 3.2 Testing

| Balicek | Verze | Ucel | Licence | Zdroj |
|---|---|---|---|---|
| `vitest` | 3.x | Unit + integracni testy | MIT | Advine |
| `@vitest/coverage-v8` | 3.x | Code coverage (V8 provider) | MIT | Advine |
| `happy-dom` | 17.x | DOM simulace pro testy | MIT | Advine |

### 3.3 Linting & Formatting

| Balicek | Verze | Ucel | Licence | Zdroj |
|---|---|---|---|---|
| `eslint` | 9.x | Linter | MIT | Advine |
| `@eslint/js` | 9.x | ESLint JS config | MIT | Advine |
| `typescript-eslint` | 8.x | ESLint TypeScript plugin | MIT | Advine |
| `eslint-plugin-react-hooks` | 5.x | React hooks linting | MIT | Advine |
| `eslint-config-next` | 16.x | Next.js ESLint config | MIT | Advine |

### 3.4 PostCSS & CSS

| Balicek | Verze | Ucel | Licence | Zdroj |
|---|---|---|---|---|
| `@tailwindcss/postcss` | latest | Tailwind v4 PostCSS plugin (nahradi postcss+autoprefixer) | MIT | Novy |
| `tw-animate-css` | latest | Animace (nahradi deprecated tailwindcss-animate) | MIT | Novy |

---

## 4. GO DEPENDENCIES (crewshipd)

> Go service `crewshipd` -- WebSocket, Docker orchestration, logs, files, webhooks.
> Managed via `go.mod`. Minimal dependencies -- prefer stdlib.

| Modul | Ucel | Licence |
|---|---|---|
| `github.com/docker/docker` | Docker SDK -- container lifecycle | Apache-2.0 |
| `go.etcd.io/bbolt` | Embedded KV store (WAL, durable job state) | MIT |
| `github.com/fsnotify/fsnotify` | inotify file watcher | BSD-3 |
| `nhooyr.io/websocket` | WebSocket server (or gorilla/websocket) | ISC |
| `github.com/prometheus/client_golang` | Prometheus metrics | Apache-2.0 |
| `gopkg.in/yaml.v3` | YAML config parsing | MIT |
| `google.golang.org/grpc` | gRPC (Phase 2, K8s inter-service) | Apache-2.0 |

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
>
> **OpenCode install:** Canonical URL je `https://opencode.ai/install` (curl | bash).
> SECURITY.md pouziva GitHub releases URL -- pri scaffoldingu sjednotit na jednu.

### Phase 2 SDK (volitelne)

| Balicek | Verze | Ucel | Licence |
|---|---|---|---|
| `@opencode-ai/sdk` | latest | OpenCode TypeScript SDK (HTTP server mod) | MIT |
| `@openai/codex-sdk` | latest | Codex App Server SDK (JSON-RPC) | MIT |

---

## 5. INFRASTRUKTURNI ZAVISLOSTI

### 5.1 Docker images

| Image | Verze | Ucel |
|---|---|---|
| `node:22-bookworm-slim` | 22 LTS | Zaklad pro vsechny Crewship images |
| `postgres:16-alpine` | 16.x | PostgreSQL databaze |
| `redis:7-alpine` | 7.x | Redis (BullMQ, PubSub, cache) |

### 5.2 System packages (agent-runtime image)

| Balicek | Ucel |
|---|---|
| `auditd` | Kernel-level syscall logging (forensic trail) |
| `inotify-tools` | Filesystem change tracking (host-side) |
| `ca-certificates` | TLS certifikaty pro HTTPS |
| `git` | Git operace v workspace (nekteri agenti) |
| `curl` | HTTP requesty (health checks, CLI install) |
| `jq` | JSON parsovani v shell skriptech |

### 5.3 System packages (worker image)

| Balicek | Ucel |
|---|---|
| `docker.io` | Docker CLI (kontejner management, ne daemon) |

---

## 6. ADVINE REUSE MATICE

### Co kopirujeme primo

| Soubor z Advine | Pouziti v Pasece | Zmeny |
|---|---|---|
| `package.json` (dependencies) | Zaklad pro Crewship package.json | Pridat: ws, dockerode, slugify. Odebrat: PPC-specificke |
| `lib/encryption.ts` | Credentials vault (AES-256-GCM) | Zadne zmeny |
| `lib/logger.ts` + config | Pino logger s redakci | Pridat Crewship-specificke redaction paths |
| `lib/redis-config.ts` | Redis multi-env konfigurace | Zadne zmeny |
| `lib/rate-limit.ts` | Rate limiting (Upstash + ioredis) | Zmenit endpointy |
| `lib/csrf.ts` | CSRF ochrana (Origin-based) | Zadne zmeny |
| `lib/security-middleware.ts` | Brute force tracking | Zmenit action typy |
| `lib/api-middleware.ts` | Request ID, logging | Zadne zmeny |
| `lib/request-context.ts` | AsyncLocalStorage per request | Zadne zmeny |
| `lib/utils/cn.ts` | clsx + tailwind-merge utility | Zadne zmeny |
| `components/ui/*` (34 komponent) | shadcn/ui primitives | Zadne zmeny |
| `hooks/use-mobile.tsx` | Responzivni design hook | Zadne zmeny |
| `vitest.config.ts` + setup | Test konfigurace | Zadne zmeny |
| `eslint.config.mjs` | ESLint konfigurace | Zadne zmeny |
| `tailwind.config.ts` | Tailwind konfigurace | Zadne zmeny |
| `tsconfig.json` | TypeScript konfigurace | Zadne zmeny |
| `postcss.config.cjs` | PostCSS konfigurace | Zadne zmeny |

### Co adaptujeme

| Soubor z Advine | Pouziti v Pasece | Zmeny |
|---|---|---|
| `lib/permissions/abilities.ts` | CASL RBAC | Subjects: Campaign→Agent, Integration→Skill, Alert→AgentRun |
| `lib/auth-helpers.ts` | Auth abstrakce | Refaktor na NextAuth-only (MVP) |
| `lib/security/audit-logger.ts` | Audit logger | `supabase.from()` → `prisma.auditLog.create()` |
| `lib/services/feature-flags.service.ts` | Feature flags | Odstranit PostHog, nechat DB flagy |
| `lib/services/subscription.service.ts` | Stripe billing | Refaktor na Prisma-only |
| `lib/validation.ts` | Zod schemata | Nove schemata pro Agent, Team, Skill, Credential |
| `lib/services/email.service.ts` | Resend emaily | Nove templates pro Paseku |
| `workers/sync-worker.ts` | BullMQ worker pattern | Nahradit sync logiku za agent logiku |
| `prisma/schema.prisma` | DB schema | Novy Crewship schema (20 tabulek) |

### Co nepouzijeme

| Soubor z Advine | Duvod |
|---|---|
| `lib/services/google-ads-*` | PPC-specificke |
| `lib/services/sklik-*` | PPC-specificke |
| `lib/services/meta-ads-*` | PPC-specificke |
| `lib/services/linkedin-*` | PPC-specificke |
| `lib/services/amazon-*` | PPC-specificke |
| `lib/services/microsoft-*` | PPC-specificke |
| `lib/alert-engine/*` | PPC alert system |
| `app/(dashboard)/monitoring/*` | PPC dashboard |
| `app/(dashboard)/campaigns/*` | PPC campaigns |
| `components/alerts/*` | PPC alerty |
| `components/analytics/*` | PPC analytics |
| `@sentry/nextjs` | Odlozeno na Phase 2 |
| `posthog-js` | Odlozeno na Phase 2 |

---

## 7. PACKAGE.JSON SABLONA

```jsonc
{
  "name": "crewship",
  "version": "0.1.0",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "next dev --turbopack",
    "build": "next build",
    "start": "next start",
    "lint": "eslint .",
    "test": "vitest run",
    "test:watch": "vitest",
    "test:coverage": "vitest run --coverage",
    "worker:dev": "tsx watch workers/agent-worker.ts",
    "build:worker": "tsc -p tsconfig.worker.json",
    "db:generate": "prisma generate",
    "db:push": "prisma db push",
    "db:migrate": "prisma migrate dev",
    "db:seed": "tsx prisma/seed.ts",
    "db:studio": "prisma studio"
  },
  "dependencies": {
    // Core
    "next": "16.1.6",
    "react": "19.2.4",
    "react-dom": "19.2.4",
    "@prisma/client": "7.3.0",
    "zod": "4.3.6",
    "zustand": "5.0.11",

    // Auth & Security
    "next-auth": "5.0.0",
    "@auth/prisma-adapter": "2.0.0",
    "@casl/ability": "6.8.0",
    "jose": "6.1.3",
    "bcryptjs": "3.0.2",

    // Infrastructure
    "bullmq": "5.67.2",
    "ioredis": "5.9.2",
    "ws": "8.19.0",
    "pino": "10.3.0",
    "pino-pretty": "13.0.0",

    // Business
    "stripe": "20.2.0",
    "resend": "6.9.1",
    "dockerode": "4.0.0",
    "@bull-board/api": "6.0.0",
    "@bull-board/server-adapter": "6.0.0",

    // UI
    "tailwindcss": "3.4.18",
    "lucide-react": "0.563.0",
    "clsx": "2.1.1",
    "tailwind-merge": "3.0.2",
    "class-variance-authority": "0.7.1",

    // Utility
    "date-fns": "4.1.0",
    "slugify": "1.6.6",
    "nanoid": "5.1.0",
    "yaml": "2.7.0"
  },
  "devDependencies": {
    // TypeScript & Build
    "typescript": "5.8.3",
    "@types/node": "22.15.0",
    "@types/react": "19.2.0",
    "@types/react-dom": "19.2.0",
    "@types/ws": "8.18.0",
    "@types/bcryptjs": "3.0.0",
    "@types/dockerode": "3.3.0",
    "prisma": "7.3.0",
    "tsx": "4.19.0",

    // Testing
    "vitest": "3.2.0",
    "@vitest/coverage-v8": "3.2.0",
    "happy-dom": "17.4.0",

    // Linting
    "eslint": "9.20.0",
    "@eslint/js": "9.20.0",
    "typescript-eslint": "8.25.0",
    "eslint-plugin-react-hooks": "5.2.0",
    "eslint-config-next": "16.1.6",

    // PostCSS
    "postcss": "8.5.0",
    "autoprefixer": "10.4.0"
  }
}
```

> **POZNAMKA:** Presne verze budou aktualizovany pri scaffoldingu (Tyden 1).
> Verze z Advine jsou pouzity kde je to mozne. Nove dependencies (ws, dockerode,
> slugify, nanoid, @auth/prisma-adapter) budou nainstalovany v latest stable.

---

## 8. VERZE MATRIX -- KOMPATIBILITA

| Komponenta | Vyzadovana verze | Duvod |
|---|---|---|
| Node.js | 22 LTS | Nativni TS type stripping, stable LTS |
| pnpm | 10+ | Corepack, workspace podpora |
| Docker Engine | 24+ | Buildx, compose v2, security features |
| PostgreSQL | 16+ | `gen_random_uuid()`, performance |
| Redis | 7+ | Streams, ACL (pouzite pro fs-events) |
| Next.js | 16.x | App Router RSC, Turbopack |
| React | 19.x | Server Components, use() |
| TypeScript | 5.8.x | Strict mode, satisfies, decorators |
| Prisma | 7.x | TS-native (bez Rust engine), pg adapter |
| Zod | 4.x | JSON Schema support, 14x rychlejsi |

### Budouci upgrady (planovane)

| Upgrade | Kdy | Duvod |
|---|---|---|
| Tailwind CSS 3 → 4 | Phase 2 | 34 Advine komponent funguje s v3, migrace neni kriticka |
| TypeScript 5.8 → 7.0 | Kdyz stable (odhad H2/2026) | Go-based compiler (10x rychlejsi build) |
| @casl/prisma integrace | Phase 2 | CASL filtruje Prisma dotazy (doplnek k RLS) |
| @supabase/ssr | Phase 2 | Supabase Auth adapter pro cloud |

---

## 9. SECURITY AUDIT

### 9.1 Kriticke dependencies

| Dependency | Riziko | Mitigace | Viz |
|---|---|---|---|
| `@prisma/client` | SQL injection (nepravdepodobne) | Parametrizovane dotazy, zadne `$executeRaw` | SECURITY.md 9.2 |
| `next` | RSC/middleware exploity | Vzdy latest patch verze | SECURITY.md 9.2 |
| `@casl/ability` | Logic error = auth bypass | Unit testy na ability matici | SECURITY.md 10.2 |
| `bullmq` | Job poisoning | Validace job dat pred zpracovanim | SECURITY.md 9.2 |
| `jose` / `next-auth` | Token forgery | NEXTAUTH_SECRET rotace, short expirece | SECURITY.md 9.2 |
| `ioredis` | Connection hijack | Redis AUTH + TLS (`rediss://`) | SECURITY.md 9.2 |
| `ws` | Connection hijack, DoS | Auth handshake, rate limiting, heartbeat | SECURITY.md 9.2 |
| `dockerode` | Container escape | Docker security options (viz DEPLOYMENT.md 5.3) | SECURITY.md 6.2 |
| `stripe` | Payment fraud | Webhook signature overovani (HMAC-SHA256) | API.md 5.15 |

### 9.2 CI audit pipeline

```bash
# Kazdý commit (CI):
pnpm audit --audit-level=critical    # fail na critical vulnerabilities
pnpm licenses list --json            # export licenci

# Kazdý tyden (GitHub Dependabot):
# Automaticke PR pro security updates

# Kazdý Docker build:
# Trivy scan na Docker image (viz DEPLOYMENT.md 13.3)
```

### 9.3 Licence kontrola

| Licence | Povoleno | Poznamka |
|---|---|---|
| MIT | Ano | Vetsina dependencies |
| Apache-2.0 | Ano | Prisma, CVA, dockerode |
| ISC | Ano | NextAuth, lucide-react |
| BSD-2/3 | Ano | |
| GPL-2.0/3.0 | **NE** | Copyleft, nekompatibilni s enterprise |
| AGPL | **NE** | Server-side copyleft |
| Proprietary | Opatrne | Claude Code CLI (ok, jen v runtime image) |

---

## 10. PROC NE -- ODMITNUTE ALTERNATIVY

| Alternativa | Proc NE |
|---|---|
| **tRPC** (misto REST) | Vendor lock-in, slozitejsi pro verejne API (Phase 2) |
| **Socket.IO** (misto ws) | Vetsi overhead, nepotrebujeme polling fallback |
| **Drizzle** (misto Prisma) | Mensi ekosystem, Prisma uz v Advine |
| **Valibot/ArkType** (misto Zod) | Mensi komunita, mene integrace |
| **Yup/Joi** (misto Zod) | Pomalejsi, mene type-safe |
| **Material-UI/Chakra/Ant** (misto shadcn) | Tezke, opinionated, Advine uz ma shadcn |
| **Bun** (misto Node.js) | Mladsi, riziko nekompatibility s npm balicky |
| **Express/Fastify** (misto Next.js API) | Dve codebasy pro solo dev |
| **Supabase JS client** (pro queries) | Vendor lock-in, Prisma je univerzalni |
| **Passport.js** (misto NextAuth) | Starsi, vice boilerplate, NextAuth je modernijsi |
| **Redis (standalone)** (misto Upstash) | Upstash = managed Redis s TLS, BullMQ kompatibilni |
| **Sentry** (v MVP) | Pridava slozitost, Pino staci pro MVP |
| **PostHog** (v MVP) | Pridava slozitost, neni kriticke pro launch |
| **Turborepo** (monorepo) | Overengineering pro solo dev, jeden repo staci |

> **Poznamka k Turborepo:** PRD.md zminuje Turborepo, ale pro solo dev s jednim
> repozitarem (frontend + worker ve stejnem projektu) je Turborepo zbytecna slozitost.
> Next.js + separatni worker script staci. Turborepo az pokud se projekt rozdeli
> na vice packages (Phase 2+).

---

## 11. OTEVRENE OTAZKY

1. **next-auth verze** -- Auth.js v5 je stable? Overit kompatibilitu s Prisma adapter pri scaffoldingu.
2. **dockerode vs Docker CLI** -- dockerode (programmatic) vs `child_process.exec("docker ...")` (jednodussi, mene dependencies)?
   Rozhodnuti: **dockerode** -- type-safe, streaming, lepssi error handling.
3. **Tailwind 4 migrace** -- Kdy presne? Sledovat stabilitu shadcn/ui s Tailwind 4.
4. **@opencode-ai/sdk** -- Phase 2 dependency. Overit stabilitu a API kompatibilitu.
5. **Monorepo split** -- Pokud projekt roste, rozdelit na `packages/` (shared types, UI, worker). Zatim jeden repo.

---

*Posledni dokument ze 7. Vsechny PRD dokumenty kompletni:
PRD.md, DATABASE.md, AGENT-RUNTIME.md, API.md, SECURITY.md, DEPLOYMENT.md, DEPENDENCIES.md*
