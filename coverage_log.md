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
| 8 | C | hooks/use-agent-activity.ts | ok | f3d9ccc | 2026-04-17T01:30Z |
| 9 | D | components/features/chat/chat-tree-row.tsx (D1/D2 too large, D3 done, D4/D5 deprecated) | ok | 7d4487e | 2026-04-17T01:50Z |
| 10 | E | e2e/* | skipped: no local dev server (2nd E skip — unchanged since iter 5; unblock by either starting local `pnpm dev` on Mac or making playwright.config.ts baseURL env-overridable to point at 192.168.1.201) | — | 2026-04-17T02:10Z |
| 11 | A | internal/encryption/encryption.go (A1/A2 done; A3 backup/selftest + A4 orchestrator/failover already had tests; new layout/versioning tests added on top of existing) | ok | 7414d0f | 2026-04-17T02:30Z |
| 12 | B | cmd/crewship/cmd_activity.go (B1/B5 done, B2/B4 too large, B3 deprecated) | ok | 1da1e94 | 2026-04-17T02:50Z |
| 13 | C | hooks/use-agent-detail.tsx | ok | 82f6d83 | 2026-04-17T03:10Z |
| 14 | D | components/features/chat/status-indicator.tsx (D1/D2 too large; D3/D-chat-tree done; D4/D5 deprecated; right-panel 652ř too large) | ok | d9be952 | 2026-04-17T03:30Z |
| 15 | E | e2e/* | skipped: no dev server (3rd E skip — same blocker as iter 5, 10) | — | 2026-04-17T03:50Z |
| 16 | A | internal/backup/checksum.go (A1/A2/A5 done; A3 selftest/A4 failover/A6 llm already covered) | ok | 99ec1c8 | 2026-04-17T04:10Z |
| 17 | B | cmd/crewship/cmd_config.go (B1/B5/B6 done; B2/B4 too large; B3 deprecated) | ok | e013568 | 2026-04-17T04:30Z |
| 18 | C | hooks/use-backups.ts | ok | e4d3234 | 2026-04-17T04:50Z |
| 19 | D | components/features/chat/assistant-turn.tsx (D2 — covered focused dispatch logic with mocked ai-elements) | ok | d8125a3 | 2026-04-17T05:10Z |
| 20 | E | e2e/* | skipped: no dev server (4th E skip — same blocker as iter 5/10/15) | — | 2026-04-17T05:30Z |
| 21 | A | internal/backup/rate_limit.go (had thin test in instance_test.go; added dedicated coverage for prune/retry-after/reset/concurrency) | ok | ea5ca0d | 2026-04-17T05:50Z |
