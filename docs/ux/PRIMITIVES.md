# Shared UI primitives — one line per change, newest first

- 2026-09-03 · cluster B · `lib/entity-links.ts` — `journal` now writes `crew_id` / `agent_id` (the keys `/journal` owns; the server resolves a slug as readily as an id) instead of `crew` / `agent`.
- 2026-09-03 · cluster B · `hooks/use-issue-detail.ts` — `useUrlSelection(key, { aliases })` reads an older spelling of the parameter and rewrites it on the first pick; `readUrlSelection` is the pure read. Used by `/routines` (`?routine=` → `?slug=`).
- 2026-09-03 · cluster B · `lib/activity-url.ts` — the `/activity` URL round trip (`?run` / `?pipeline` / `?mission` / `?status` kept, `?walk=`, `?lens=`, `?entry=`); reuse `activityUrl()` when linking into a walk.
- 2026-09-03 · cluster B · `components/features/issues/issue-runs-card.tsx` — `IssueRunsCard` + `issueRunLinks()`; the run/agent/journal/activity links of an issue in one place.
- 2026-09-03 · wave 0 · `lib/entity-refs.ts` — `refHref("routine/x")` / `refLabel` turn stored `kind/slug` owner and producer refs into routes through entityHref; null for a kind with no page.
- 2026-09-03 · wave 0 · `components/layout/sidebar-kit.tsx` + `.kit-tap` in globals.css — every kit control is 44px under a coarse pointer; add `kit-tap` to any new interactive element in a sidebar.
- 2026-09-03 · wave 0 · `components/ui/status-pill.tsx` + `lib/format-status.ts` — THE status pill (dot + word, six tones) and the enum→word map. Replace every local pill map with it; add missing enums to format-status, never to a component.
- 2026-09-03 · wave 0 · `lib/entity-links.ts` — `entityHref()` for crew, agent, chat, issue(s), routine(s), run, journal, page, credential(s), inbox, spend. Build every cross-link through it.
- 2026-09-03 · wave 0 · `components/ui/inline-empty.tsx` — the one-line empty state for cards.
- 2026-09-03 · wave 0 · `components/ui/sparkline.tsx` — Sparkline moved here from the dashboard (old path re-exports).
- 2026-09-03 · wave 0 · `hooks/use-paged-list.ts` — client half of the paging convention (`?limit&offset`, `X-Total-Count`); `total === null` means the endpoint does not page yet.
- 2026-09-03 · `components/features/dashboard/sparkline.tsx` — `Sparkline` (draw-on-mount line) and `sparklinePoints`; safe to move to `components/ui` when a second area uses it.
- 2026-09-03 · `app/(dashboard)/dashboard-helpers.ts` — `crewColor` now accepts hex as well as palette ids; `foldRunVolumeSeries` folds a many-series chart to top N + Other.
- 2026-09-03 · `components/ui/crew-icon.tsx` and `components/icons/provider-icons.tsx` — real brand marks (Claude, OpenCode, Cursor); Factory stays a neutral glyph.
- 2026-09-03 · `lib/model-catalog.ts` + `config/models.json` — the one model list; never type a model id in a component.
