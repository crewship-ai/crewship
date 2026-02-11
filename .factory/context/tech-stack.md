# Tech Stack

## Frontend (TypeScript)

- **Next.js** latest (App Router, RSC, Turbopack)
- **React** latest
- **TypeScript** latest (strict mode)
- **Tailwind CSS** 4.x (CSS-first config, `@theme inline`, oklch)
- **shadcn/ui** latest (new-york style, Radix UI primitives)
- **lucide-react** latest (ONLY allowed icon library)
- **tw-animate-css** latest (animations)
- **next-themes** latest (dark mode)
- **clsx** + **tailwind-merge** (cn() utility)
- **Zod** latest (validation)
- **Zustand** latest (client state)
- **xterm.js** latest (web terminal to agent containers)
- **Prisma** latest (ORM for PostgreSQL CRUD)

## Frontend Auth & Security

- **NextAuth.js** (Auth.js v5) -- email+password, OAuth
- **@auth/prisma-adapter** -- NextAuth Prisma integration
- **CASL** latest (RBAC)
- **jose** latest (JWT verification)

## Backend (Go)

- **Go** latest -- `crewshipd` binary (WebSocket, Docker, orchestration)
- **Docker SDK for Go** (`github.com/docker/docker`) -- container lifecycle
- **bbolt** (`go.etcd.io/bbolt`) -- embedded KV store (WAL, durable job state)
- **fsnotify** (`github.com/fsnotify/fsnotify`) -- inotify file watcher
- **nhooyr.io/websocket** or **gorilla/websocket** -- WebSocket server
- **prometheus/client_golang** -- metrics
- **gopkg.in/yaml.v3** -- config parsing

## Database

- **PostgreSQL** 16 (local Docker, structured data only)
- **Prisma** (ORM, schema source of truth, accessed only from Next.js)

## Storage

- **Filesystem** -- agent output (persistent, bind mount)
- **Filesystem** -- JSONL logs (managed by logrotate)
- **bbolt** -- Go service WAL (job state, survives crashes)
- No Redis. No S3 (MVP). No cloud storage dependency.

## Monitoring

- **cAdvisor** -- container metrics (CPU, RAM, disk, network)
- **Prometheus** -- Go service metrics (built-in)
- **logrotate** -- Linux native log rotation

## Design

- **Tailwind CSS 4** -- CSS-first config via `app/globals.css`
- **tweakcn.com** -- theme generator (Meta Business Suite inspired)
- **Inter** -- primary font (next/font/google)
- **JetBrains Mono** -- monospace for code/logs

## Development

- **Mac Mini 16GB** -- local development
- **Coolify on Proxmox** (128GB RAM, i7-12700) -- staging
- **Production** -- TBD

## Testing

- **Vitest** latest (frontend unit tests)
- **Go testing** (backend tests)
- **pnpm** latest (JS package manager)

## Container Registry

- **ghcr.io/crewship-ai/** -- agent-runtime images

## NOT in stack (removed)

- ~~Redis~~ -- Go channels + bbolt replace BullMQ
- ~~BullMQ~~ -- Go service handles job orchestration natively
- ~~ioredis~~ -- no Redis
- ~~ws (npm)~~ -- Go handles WebSocket natively
- ~~pino~~ -- Go handles logging natively
- ~~dockerode~~ -- Go Docker SDK instead
- ~~Socket.IO~~ -- never considered
- ~~Supabase Auth~~ -- NextAuth.js instead
- ~~tailwindcss-animate~~ -- tw-animate-css instead
