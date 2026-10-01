# Decisions — architecture decision records

**Purpose.** Accepted and rejected architectural decisions, with the evidence
that produced them. A decision here is *decided*: it names its date, its
status, and the issue that ordered it. The reasoning stays with the decision —
a future change reverses it by writing a new ADR that references this one,
not by editing this one.

**Rule for a new file.** `ADR-<SUBJECT>-<YYYY-MM-DD>.md`, status line first
(`decided (accept)` / `decided (reject)`), issue link, and the measurement or
argument that closed the question. Spike harnesses and raw measurements stay
where they can be re-run (`tools/spike-river/`, generated reports are
gitignored outputs) and are linked, not copied.

## Contents

- [`ADR-QUEUE-RIVER-SQLITE-2026-09-10.md`](ADR-QUEUE-RIVER-SQLITE-2026-09-10.md) —
  **reject** River over SQLite for the 1.0 durable work queue (issue #2488).
  Closes the spike ordered by
  [`docs/prd/SPIKE-RIVER-SQLITE-1-0.md`](../prd/SPIKE-RIVER-SQLITE-1-0.md);
  the harness is preserved at [`tools/spike-river/`](../../tools/spike-river/)
  as a reproducible experiment. Cited from `internal/database` and
  `internal/work` as the reason the queue stays on SQLite primitives.
