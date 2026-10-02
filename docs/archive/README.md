# Archive — superseded documents

**Purpose.** Documents preserved for the record after a newer document
replaced them. An archived document describes the system or the plan *as it
was* at the date in its header. **Editing an archived document to match the
present falsifies the record** — if it is wrong about today, that is because
today is not its subject; its replacement carries the current truth.

`go run ./scripts/docs-inventory -strict` excludes this directory for the
same reason: history may name commands and routes that no longer exist, and
nobody is allowed to "fix" that.

**Rule for a new file.** A document moves here only when (a) a specific
replacement exists, (b) the old document links to it (and the replacement
links back for history), and (c) the header states it is archived/superseded.
Age alone is not a reason; unreferenced alone is not a reason.

## Contents

- [`pages-apps.md`](pages-apps.md) — the 2026-09-08 research into Pages as
  internal applications (Refine/Puck evaluation). Superseded by
  [`docs/prd/pages-apps-architecture.md`](../prd/pages-apps-architecture.md)
  (implementation architecture) and
  [`docs/specs/pages-apps.md`](../specs/pages-apps.md) (the shipped v1
  contract); kept for the market research and the decision record.
- [`pages-apps-v1-history-2026-09-09.md`](pages-apps-v1-history-2026-09-09.md) —
  the implementation chronology of Pages Apps v1 before the #2472 hardening.
  The current contract is
  [`docs/specs/pages-apps.md`](../specs/pages-apps.md), which links here
  for the history.
