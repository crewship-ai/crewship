# Crewship MVP -- Implementation Plan & Progress

**Datum:** 2026-02-16
**Autor:** Pavel Srba + AI
**Cil:** Funkcni MVP za 6-8 tydnu (open-source release)

---

## Strategicka rozhodnuti

### MVP scope (co uzivatel MUSI umet)
1. Prihlasit se, vytvorit workspace + crew
2. Vytvorit agenta, pridat mu credentials
3. Chatovat s agentem v realnem case (WebSocket)
4. Videt soubory co agent vytvoril
5. Zakladni audit log

### Vedome odlozeno z MVP
- Skills marketplace (MVP: 3 bundled skills)
- Lead/Coordinator orchestrace (Phase 2A/2B)
- Messaging kanaly (Discord, Telegram)
- Stripe billing
- Cron joby, loop mode
- M:1 kolaborace
- Web terminal (xterm.js)
- Git-like config versioning
- Sidecar (MCP Gateway) -- primo Docker exec v MVP

### Technicke omezeni MVP
- Agent runtime: pouze Claude Code (`claude --print`)
- Container model: 1 kontejner = 1 crew
- Storage: LocalFS provider
- State: bbolt provider
- No Landlock (per-agent FS izolace az Phase 2)
- No srt sandbox (MCP server sandboxing az Phase 2)

---

## 5 fazi MVP

### Faze 1: Scaffolding [HOTOVO]
- [x] package.json + pnpm install (vsechny dependencies)
- [x] Prisma schema (20 tabulek z DATABASE.md)
- [x] shadcn/ui komponenty (22 komponent)
- [x] next.config.ts
- [x] Auth.js v5 config (auth.ts)
- [x] Prisma DB helper (lib/db.ts)
- [x] `pnpm dev` startuje (Next.js 15 + Turbopack)
- [x] `pnpm build` projde bez chyb
- [x] `go run ./cmd/crewshipd` startuje

### Faze 2: Auth + Workspace + Crew CRUD [PRISTI]
- [ ] Go auth endpoints (NextAuth-compatible JWE) + login/signup stranky
- [ ] Dashboard layout (sidebar + hlavni obsah)
- [ ] Workspace CRUD + Go API routes
- [ ] Crew CRUD + Go API routes
- [ ] RBAC zaklad (Go middleware)
- [ ] Zustand store (currentWorkspace, currentUser)

### Faze 3: Agent + Credentials
- [ ] Agent CRUD + detail stranky (tabs)
- [ ] Credentials vault (AES-256-GCM)
- [ ] Agent-Credential prirazeni
- [ ] 3 bundled skills
- [ ] Audit log (zakladni)

### Faze 4: Go Backend + Chat (KRITICKA)
- [ ] Go WebSocket gateway
- [ ] ~~IPC vrstva (Unix socket)~~ Not needed (single binary, in-process)
- [ ] Docker container lifecycle (ContainerProvider)
- [ ] Agent execution (Docker exec + CLI adapter)
- [ ] Stdout streaming (Docker → WS → Browser)
- [ ] Chat UI (messages, streaming, markdown)
- [ ] JSONL logging
- [ ] bbolt WAL (StateProvider)

### Faze 5: Polish + Launch
- [ ] File browser + download
- [ ] Webhook ingress
- [ ] Dashboard (stat karty, overview)
- [ ] Onboarding wizard
- [ ] Error handling + graceful shutdown
- [ ] Testy (Vitest + Go test)
- [ ] Docker compose (prod)
- [ ] README pro GitHub release

---

## Rizika
1. Docker Desktop na macOS -- bind mounts pres FUSE mohou byt pomale
2. Claude Code CLI -- overit `--print` mode v Docker kontejneru
3. WebSocket spolehlivost -- reconnect, heartbeat
