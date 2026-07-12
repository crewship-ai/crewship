# Frontend audit — 2026-07-12

Scope: placeholders, performance, realtime (WS/webhook) coverage, desktop-app (macOS) readiness.
Method: 4 parallel code-reading agents + measured production build.

Build ground truth: static export **47 MB total, 43.9 MB JS across 794 chunks** (largest 1.47 MB + 1.08 MB uncompressed); CSS 320 KB; fonts self-hosted woff2; no raster bloat. The entire export embeds into the Go binary (why `crewship` is ~65 MB). **107 runtime deps; 525/577 components `'use client'`** — client bundle weight is the dominant perf lever.

---

## 1. Placeholders / unfinished UI

| Sev | Finding | Where |
|---|---|---|
| HIGH | Docker overview renders **fabricated data** — hardcoded `node:18-slim`, always-green "Running", `--` metrics; reads as live infra | `components/features/orchestration/docker-overview.tsx:57-66` |
| HIGH | Artifact viewer: **Diff + Preview tabs are clickable dead ends** ("coming soon") | `components/features/chat/artifact/artifact-pane.tsx:180-190`, switcher `components/ai-elements/artifact.tsx:153-155` |
| MED | **SpendView (~370 lines) fully implemented but never imported** — Journal "Spend" tab locked with "soon" badge | `components/features/journal/spend-view.tsx`, `app/(dashboard)/journal/page.tsx:76` |
| MED | Saved views silently **drop status/label/priority/sort filters** (TODO in code) | `components/features/orchestration/orchestration-layout.tsx:831-839` |
| MED | Template browser: My/Workspace/Marketplace source tabs = ComingSoon body | `components/features/crews/create-agent/template-browser.tsx:250-273` |
| MED | **8 design-mockup HTML files ship publicly** in the static export (`/mockups/*.html`) | `public/mockups/` |
| MED | SpendView "Avg cost / mission" KPI divides by top-rows count, not mission count (misleading) | `spend-view.tsx:128-132` |

Positives: no `href="#"`, no empty handlers, no swallowed errors, designed empty/error states everywhere, clean consistent English copy, no leftover Next.js boilerplate.

## 2. Performance

**Bundle (top lever):**
- F1 HIGH: **all 10 DiceBear style packs statically imported** into the app shell (`lib/agent-avatar.ts:1-11`) — multi-hundred-KB eager cost. Lazy-import per style.
- F6 MED: **`@streamdown/mermaid` eagerly in the chat bundle** via `reasoning.tsx:12-15` (~500 KB+) — lazy-load on ```mermaid fence.
- F4 MED: **full `shiki` import in chat critical path** (`ai-elements/code-block.tsx:31`) — switch to `shiki/core` + dynamic langs or `next/dynamic` the CodeBlock.
- F5 MED: `@xyflow/react` statically imported (trace-canvas, workflow-graph) — `next/dynamic` the canvases.
- F2 MED: **`react-pdf` is a dead dependency** (zero imports) — remove.
- F3 MED: redundant stacks: shiki + highlight.js/lowlight (2 highlighters), marked+turndown only for tiptap issues editor. Consolidate.
- `@prisma/client` in runtime deps (types-only usage) — move to devDependencies.
- `experimental.optimizePackageImports` not configured (lucide-react, radix-ui, recharts, date-fns candidates); run `@next/bundle-analyzer` to attribute the 1.47 MB chunk.
- Recharts statically imported by the dashboard landing route (`app/(dashboard)/page.tsx:17-23`).

**Runtime hygiene:**
- F7 HIGH: **chat composer keystroke re-renders the whole message list** — `chat-panel.tsx:102` input state + `:431` `turns.map` in same component inside `AnimatePresence popLayout`. Extract composer.
- F9 HIGH: **chat message list not virtualized** (hundreds of animated motion.divs stay mounted); react-virtuoso already shipped.
- F8 MED: whole-store Zustand subscriptions (`useDrawerStore()`, `useComposerStore()` without selectors) in chat-panel/right-rail/artifact-pane.
- F13 MED: logs-viewer unbounded `setLogs([...prev, entry])` + unmemoized filter per render (`logs-viewer.tsx:100,118`).
- F11 MED: unconditional 3s polls (use-trace, use-pipeline-runs, orchestration shell, engine-status) with no visibilitychange gating; inbox polls in background deliberately.
- F12 LOW: 1s setTick re-render clocks on mission-board/runs-view.
- F10 MED: inbox list unvirtualized and unbounded.

Good already: rAF-batched token streaming in use-chat, ref-based realtime pub/sub, no lodash/moment footguns, pagination on runs-view, fonts/assets clean.

## 3. Realtime / webhook coverage

Architecture: shared workspace WS (`RealtimeProvider`, ~40 event types) + dedicated chat session WS. Coverage is broad and mature: runs, agent badges, provisioning, inbox, routines, waitpoints, escalations, logs, dashboard tiles, global toasts, disconnect banner.

Gaps (ranked):
1. **Notification channels: full backend (CRUD+test, v133, `internal/api/notification_channels.go`) with ZERO frontend** — biggest capability-vs-UI gap.
2. **No reconnect resync in RealtimeProvider** (no `onConnect` → pure-WS consumers like files-pane stay stale after a WS gap; chat socket does resume correctly).
3. **Paymaster spend page has no realtime and no poll** (manual reload only) — wire `run.completed`/`pipeline.run.completed` → reloadKey.
4. **Routine webhooks tab stale** (`fire_count`/`last_fired_at` refresh only on mount) — refresh on `pipeline.run.started` with `triggered_via==="webhook"`.
5. No global toast for `pipeline.run.failed` (agent `run.failed` has one).
- Note: backend channels `files:{id}` and `agent:{id}` unused by frontend (covered by workspace broadcast) — dead surface, not a bug. No per-delivery webhook history (backend tracks aggregates only). No visibilitychange handling anywhere (efficiency).

## 4. Desktop app (macOS) readiness

**Works as-is:** static export is 100% server-runtime-free (no API routes, no middleware, no server actions, dev-only rewrites) — loads fine from `tauri://`. localStorage is UI-prefs only. No service worker/PWA to fight.

**Blockers:**
1. `apiFetch.assertSameOrigin` (`lib/api-fetch.ts:60-86`) hard-throws on cross-origin + **cookie-session auth** breaks across custom-scheme origins.
2. **WS hub origin check hardcoded** (`internal/ws/hub.go:519-547`) — ignores `CREWSHIP_ALLOWED_ORIGINS`, rejects desktop origins in prod. Needs server change.
3. **No base-URL concept** — same-origin baked in: 1 chokepoint (apiFetch) + ~20 bare fetches (auth/onboarding pages) + 4 WS URL builders (`use-websocket.ts:336`, `use-realtime.tsx:122`, `use-terminal.ts:80`, `chat-panel.tsx:55`).

**Change list:** window.open OAuth popups → shell.open + deep-link return (4 sites); target=_blank links; blob downloads (9 sites, work in Tauri, want save dialog); clipboard perms. HTTP origin fix is config-only (`CREWSHIP_ALLOWED_ORIGINS=tauri://localhost,http://tauri.localhost`).

**Recommended architecture:**
1. **Tauri v2** (WKWebView, ~10 MB shell; real Origin header — `file://`/null is rejected by server).
2. **Remote-server model** (user-entered crewshipd URL, CLI-profile style); local embedded server later.
3. **Bearer-token auth** via existing `/api/v1/auth/pair/start|poll` pairing flow + `crewship_cli_` tokens in macOS keychain (backend middleware already accepts Bearer).
4. Single API/WS client chokepoint: relax assertSameOrigin to allowlisted base, inject base URL + Authorization; route the 4 WS builders + ~20 bare fetches through it.
5. Server: extend WS hub origin check to honor `CREWSHIP_ALLOWED_ORIGINS`.

**Effort estimate:** frontend base-URL + token auth M–L; server WS origin M; Tauri shell + niceties (menu, dock badge, notifications, shortcuts) M. The frontend refactor (base URL + Bearer) is desktop-prep AND generally useful (any cross-origin deploy).

## Suggested implementation waves
- **W1 (quick wins, 1 PR each):** remove public/mockups + react-pdf + prisma dep move; hide artifact Diff/Preview tabs + template-browser dead tabs; docker-overview honest empty state; DiceBear lazy-load; mermaid/shiki/xyflow lazy; optimizePackageImports.
- **W2 (chat perf):** composer extraction + virtualized turns; Zustand selectors; logs cap+memo.
- **W3 (realtime):** RealtimeProvider onConnect resync; paymaster + webhooks tab wiring; pipeline.run.failed toast; notification-channels UI (bigger — design needed).
- **W4 (desktop prep):** base-URL abstraction + Bearer auth mode + WS-hub allowlist; then Tauri v2 shell skeleton.
