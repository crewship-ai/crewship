# CRE-20: Real-time WebSocket Push + Animated UI

**Status:** In Progress
**Branch:** `feat/realtime-websocket-push`
**Effort:** ~5-7 dnu
**Dependencies:** motion (installed), lucide-react (installed)

---

## Co uz existuje

### Backend (Go)
- `internal/ws/hub.go` -- Hub s Broadcast(), Subscribe/Unsubscribe, channel pub/sub
- `internal/api/internal.go` -- broadcastuje `run.started`, `run.completed`, `run.failed`, `agent.status`
- `internal/api/assignments.go` -- broadcastuje `assignment_created/running/completed/failed` do session + workspace channels
- `internal/api/queries.go` -- broadcastuje `escalation_created`, `escalation.created`, peer conversation events
- `internal/api/missions.go` -- broadcastuje `mission.created`, `mission.updated`, `task.updated`

### Frontend (TypeScript)
- `hooks/use-realtime.tsx` -- RealtimeProvider, useRealtimeEvent hook, 9 event typu
- Dashboard page -- useRealtimeEvent pro run.*/agent.status (tichy refetch)
- Runs page -- useRealtimeEvent pro run.*
- Activity Feed -- useRealtimeEvent pro assignment.updated, escalation.created
- Escalations -- useRealtimeEvent pro escalation.created
- Assignments -- useRealtimeEvent pro assignment.updated

### Problem
Vsechny eventy delaji JEN tichy refetch. Uzivatel nepozna ze se neco zmenilo.
Zadna vizualni indikace, zadne animace, zadna zvyrazneni.

---

## Implementation Plan

### Phase 1: Frontend Infrastructure (~1 den)

#### 1.1 Install lucide-animated icons (shadcn registry)
```bash
pnpm dlx shadcn add "https://lucide-animated.com/r/activity"
pnpm dlx shadcn add "https://lucide-animated.com/r/bot"
pnpm dlx shadcn add "https://lucide-animated.com/r/check"
pnpm dlx shadcn add "https://lucide-animated.com/r/loader-pinwheel"
pnpm dlx shadcn add "https://lucide-animated.com/r/sparkles"
pnpm dlx shadcn add "https://lucide-animated.com/r/brain"
pnpm dlx shadcn add "https://lucide-animated.com/r/bell"
pnpm dlx shadcn add "https://lucide-animated.com/r/badge-alert"
pnpm dlx shadcn add "https://lucide-animated.com/r/refresh-cw"
pnpm dlx shadcn add "https://lucide-animated.com/r/send"
pnpm dlx shadcn add "https://lucide-animated.com/r/wifi"
pnpm dlx shadcn add "https://lucide-animated.com/r/rocket"
pnpm dlx shadcn add "https://lucide-animated.com/r/key"
pnpm dlx shadcn add "https://lucide-animated.com/r/circle-check"
```

#### 1.2 Create `components/ui/animated-number.tsx`
- Number counter with roll-up effect using motion
- Props: value, duration, className
- Inspirace: Wigggle Stocks ticker, Dashboard visitor counter

#### 1.3 Create `components/ui/flash-highlight.tsx`
- Wrapper that flashes bg-primary/5 -> transparent when children data changes
- Props: children, trigger (any value that changes), duration
- Pouziti: table rows, stat cards

#### 1.4 Enhance `components/layout/stat-card.tsx`
- Animated number counter misto statickeho value
- Support animated icon variant (lucide-animated)
- Flash highlight pri zmene value

---

### Phase 2: Dashboard Live Updates (~1 den)
- [ ] StatCards: animated numbers + animated icons (bot, activity, chart, key)
- [ ] Agent table: animated status badges (pulsujici pri RUNNING)
- [ ] Agent table: flash highlight pri zmene statusu radku
- [ ] Agent table: smooth status badge transition (Idle->Running)

### Phase 3: Mission Board + Crew Detail (~1-2 dny)
- [ ] Mission Board: live tikajici timer pro IN_PROGRESS tasky
- [ ] Mission Board: animated task status badge prechody
- [ ] Mission Board: animated progress summary (bar/counter)
- [ ] Activity Feed: odstranit Refresh button, auto-update, slide-in animace
- [ ] Assignments: odstranit Refresh button, animated status badges
- [ ] Escalations: odstranit Refresh button, pulsujici PENDING badge
- [ ] Crew Stats: animated countery

### Phase 4: Chat + Global Polish (~1 den)
- [ ] Chat status: brain/sparkles ikona misto Loader2
- [ ] AgentCard: animated status dot (pulse pri RUNNING)
- [ ] Sidebar: animated ikony na hover
- [ ] WS connection indicator (animated wifi ikona)
- [ ] Toast notifikace pri dulezitych eventech (escalation, run.failed)

---

## Lucide Animated Icon Mapping

| Use case | Ikona | Komponenta |
|----------|-------|------------|
| Agent running | loader-pinwheel | AgentCard, Dashboard table, Assignments |
| Task completed | check / circle-check | Mission Board, Assignments, Runs |
| Agent thinking | brain | Chat status indicator |
| Agent writing | sparkles | Chat status indicator |
| New notification | bell | Toolbar notification badge |
| Escalation | badge-alert | Escalation table |
| Dashboard stats | activity, bot, key | StatCards |
| Webhook trigger | rocket | Runs table |
| Refresh/sync | refresh-cw | Replace manual refresh buttons |
| Send message | send | Chat input |
| Live connection | wifi | WS status |

## Wigggle UI Inspirace

| Widget | Kde pouzit | Pattern |
|--------|-----------|---------|
| Stocks ticker | StatCard countery | Number roll-up animace |
| Productivity progress bars | Mission Board summary | Animated fill progress |
| Dashboard visitor donut | StatCard "Running Now" | Mini donut chart |
| Clock countdown | Mission Board duration | Live tikajici casovac |
| Tasks checklist | Mission Board | Checkbox + strikethrough animace |

---

## Iterace / Progress Log

### Iterace 1 (aktualni)
- [ ] Phase 1.1: Install lucide-animated icons
- [ ] Phase 1.2: animated-number component
- [ ] Phase 1.3: flash-highlight component
- [ ] Phase 1.4: StatCard enhancement
