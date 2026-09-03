# Shared UI primitives — one line per change, newest first

- 2026-09-03 · `components/features/dashboard/sparkline.tsx` — `Sparkline` (draw-on-mount line) and `sparklinePoints`; safe to move to `components/ui` when a second area uses it.
- 2026-09-03 · `app/(dashboard)/dashboard-helpers.ts` — `crewColor` now accepts hex as well as palette ids; `foldRunVolumeSeries` folds a many-series chart to top N + Other.
- 2026-09-03 · `components/ui/crew-icon.tsx` and `components/icons/provider-icons.tsx` — real brand marks (Claude, OpenCode, Cursor); Factory stays a neutral glyph.
- 2026-09-03 · `lib/model-catalog.ts` + `config/models.json` — the one model list; never type a model id in a component.
