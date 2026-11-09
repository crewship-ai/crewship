# Template Crewship Dashboard

- Original: https://claude.ai/artifact/RwrX5Qdjw7zLHdvxjEABKL (a claude.ai
  Artifact of type "Dashboard", read on 2026-11-08 at version
  `1794068836-6c50`). No comment threads.
- Seeded as: `ref-dashboard-template`, an interactive dashboard artifact
  project (`POST /api/v1/pages/artifacts`) with one data collection, `tasks`
  (`POST /api/v1/pages/{slug}/artifact/data/tasks`). Registered from
  `seeddata/mirror_rwrx.go`; the existing showcase seed step creates it.

## What the original holds

The dashboard type keeps its content in the artifact's store:

| Store document | Here |
| --- | --- |
| `dash/meta` `{title, lang: "cs"}` | `original/store.json`; the title is the Page name and `<title>`, the language `<html lang>` |
| `dash/params` `{crew: ""}` | `original/store.json`; the filter's default in `host/runtime.js` |
| `datasets/tasks` (a fixed JSON file, `sample-crew-tasks.json`) | `original/store.json`; the file is `original/sample-crew-tasks.json` |
| `files/index.html` (the page) | `original/index.html`, byte for byte |

## How it was converted

`build.py` writes `manifest.json` and `data-tasks.json` from those files:

- **The page is verbatim.** `original/index.html` is placed unchanged inside
  `<div id="dash-root">`, as the dashboard host places it. Its Czech text,
  styles, calcs, chart and table code are the original's.
- **The rows are verbatim.** The 18 rows of `sample-crew-tasks.json` become
  documents `t01`…`t18` of `tasks`; the store returns them in id order, so the
  page sees the original row order.
- **The host is reproduced, not the page rewritten.** The dashboard host gives
  the page design tokens, d3 and a `dash` API; the Crewship frame gives none.
  - `host/theme.css` (a `text/css` file of the project): the tokens the page
    reads (`--color-*`, `--cds-*`, `--font-anthropic-*`), resolved from the
    type's stylesheet at its default "comfortable" density, light and dark,
    plus its page gutter, `dash-select` and `dash-skeleton` styles.
  - d3 v7.9.0 (ISC, copyright header kept), inlined because the artifact
    runtime runs inline scripts only.
  - `host/runtime.js`: the part of `dash` this page calls (`data`, `calc`,
    `draw`, `onData`, `params`, `setParams`, `resetParams`, `colors`) over
    `crewship.data.get("tasks")`, with the host's `data-dash-source-state`
    and `data-dash-state` stamps. `dash.colors` names the same categorical
    chart colors (`#2a78d6` light / `#3987e5` dark first).

## Known gaps

- **Per-number citations.** The host's hover-a-number citation and the
  data-mark check are not reproduced; the `data-source`/`data-field` marks are
  kept in the page but nothing reads them. The Crewship Sources drawer says
  that this dashboard's values carry no per-number citation and lists the
  `tasks` dataset with its title, description, mode, row count, last write
  and the code version that declares it.
- **Dataset metadata** is carried in `datasets.json`: the title and
  description from `original/store.json`, `mode: "fixed"` from its `fixed`
  flag, and `sample: true` because the original names the rows "Sample" and
  describes them as invented. The frame chip therefore reads **Sample data**.
  The original's `source` (file name and blob URL) and its `edit`/`updated`
  stamps are not copied: the last write and its writer come from the server.
- **Filter state.** The crew filter lives in the open page only, per reader:
  it never writes the shared data and a second reader is not affected. It is
  not kept across visits (the host keeps it per viewer); a reload starts from
  the page default.
- **Empty data.** `host/runtime.js` reports a collection with no rows as its
  own `empty` state, so the page's KPIs show a dash instead of `0`/`NaN`.
- **Fonts.** "Anthropic Sans/Serif" are not shipped; the original's own
  fallbacks (system UI and Georgia) are used.
- **Editing.** The host's in-place page editor, remix and Ask Claude about a
  number have no Crewship counterpart here; the page is changed by publishing
  a new version.
