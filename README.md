<p align="center">
  <img src="crewship.svg" height="80" alt="Crewship" />
</p>

<h1 align="center">Crewship</h1>

<p align="center">
  Open-source platform for orchestrating AI agents as virtual employees.
</p>

<p align="center">
  <a href="https://github.com/crewship-ai/crewship/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue.svg" alt="License" /></a>
</p>

---

Crewship lets you organize AI agents into teams, assign them roles, credentials, and skills, and run them in isolated containers. Think of it as an HR platform — but for AI workers.

## Features

- **Agents as employees** — each agent has a role, team, credentials, and work history
- **Team-based organization** — group agents into teams with shared context
- **Credential vault** — AES-256-GCM encrypted keys with priority-based failover
- **Isolated execution** — every agent runs in its own Docker container (K8s ready)
- **Role-based access** — 5-tier RBAC (Owner, Admin, Manager, Member, Viewer)
- **Real-time logs** — JSONL streaming via WebSocket
- **Multi-org support** — manage multiple organizations from a single instance
- **Self-hosted** — run on your own infrastructure, keep your data

## Tech Stack

| Layer | Technology |
|-------|------------|
| UI | Next.js 16, React 19, Tailwind CSS 4, shadcn/ui |
| Auth | NextAuth.js v5 (Auth.js) |
| Database | PostgreSQL 16 via Prisma 7 |
| Backend | Go (crewshipd) — WebSocket, Docker orchestration, logs |
| Agent runtime | Docker containers with CLI adapters (Claude, GPT, etc.) |

## Quick Start

```bash
# Clone
git clone https://github.com/crewship-ai/crewship.git
cd crewship

# Start PostgreSQL
docker compose -f docker/docker-compose.yml up -d

# Install dependencies
pnpm install

# Set up environment
cp .env.example .env
# Edit .env with your DATABASE_URL, NEXTAUTH_SECRET, ENCRYPTION_KEY

# Generate Prisma client & push schema
pnpm db:generate
pnpm db:push

# Run
pnpm dev
```

Open [http://localhost:3000](http://localhost:3000).

## Project Structure

```
app/              Next.js frontend (TypeScript)
cmd/crewshipd/    Go backend service
prisma/           Database schema
lib/              Shared utilities, auth, RBAC, encryption
components/       UI components (shadcn/ui)
docker/           Docker Compose & container configs
```

## Contributing

Contributions are welcome. Please open an issue first to discuss what you'd like to change.

## License

[Apache License 2.0](LICENSE) — free to use, modify, and distribute.

Copyright 2025-2026 Unify Technology s.r.o.
