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
| 3 | C | hooks/use-pending-escalations.ts (C1/C2 deprecated, skipped) | ok | d931943 | 2026-04-16T21:52Z |
| 4 | D | components/features/chat/turn-renderer.tsx (D1/D2 too large for timebox, D4/D5 deprecated) | ok | 72a60f1 | 2026-04-17T00:10Z |
| 5 | E | e2e/credentials.spec.ts | skipped: no dev server (Playwright webServer needs localhost:3001; Mac has none — remote dev is on 192.168.1.201) | — | 2026-04-17T00:30Z |
| 6 | A | internal/sidecar/port_expose.go | ok | fc0402c | 2026-04-17T00:50Z |
| 7 | B | cmd/crewship/cmd_audit.go (B2 cmd_backup 722ř + B4 cmd_agent 803ř too large; B3 cmd_captain deprecated) | ok | 3cd1c9d | 2026-04-17T01:10Z |
