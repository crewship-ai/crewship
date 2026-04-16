# Auto-test coverage log

Automatický log z `/loop 20m` iterací. Nekomit/needit mimo loop task.

**Rotace kategorií (A→B→C→D→E):**

| # | Kód | Kategorie | Příklady cílů |
|---|---|---|---|
| 0 | A | Security-critical BE | keeper/types.go, sidecar/port_expose.go, backup/selftest.go, encryption/* |
| 1 | B | CLI commands | cmd_expose.go, cmd_backup.go, cmd_captain.go, cmd_agent.go |
| 2 | C | FE hooks | hooks/use-captain.ts, use-captain-store.ts, use-realtime.tsx |
| 3 | D | FE chat/captain komponenty | chat/chat-panel.tsx, chat/assistant-turn.tsx, captain/captain-panel.tsx |
| 4 | E | E2E Playwright | missions flow, credential assignment, crew provisioning |

Další iterace: `next_iter = iter_count (viz poslední řádek tabulky níže) % 5` → kategorie.

## Iterace

| Iter | Kat. | Cíl | Status | Commit | Čas |
|---|---|---|---|---|---|
| 0 | — | bootstrap (tato hlavička) | init | — | 2026-04-16 |
| 1 | A | internal/keeper/types.go | ok | fd3a059 | 2026-04-16T21:05Z |
| 2 | B | cmd/crewship/cmd_expose.go | ok | cce1ef8 | 2026-04-16T21:15Z |
