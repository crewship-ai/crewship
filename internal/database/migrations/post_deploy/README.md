# Post-deployment migrations

These run **after** the server is already serving requests, in batches, on a
background worker. They exist for one reason: a migration that rewrites every
row of a large table is upgrade downtime, and downtime that grows with a
customer's data is not acceptable.

Measured on this schema (`migrate_scaling_test.go`): the three journal
rewrites cost ~59µs per row. That is one minute of downtime at a million
journal entries and ten at ten million — for an instance that is otherwise
ready to serve.

## The contract you are accepting

A post-deploy migration has NOT run when the new code starts handling
requests. So:

1. **The change must be additive.** Add a column, add a table, add an index.
   Never drop or rename anything a post-deploy migration depends on.
2. **The running code must work with the change half-applied.** Some rows will
   have the new column populated and some will not, for as long as the backfill
   takes. Read paths must tolerate both — usually by treating the unfilled
   value as "not yet known" rather than as a real value.
3. **It must be idempotent and resumable.** It commits per batch, so a restart
   re-enters partway through. `UPDATE … WHERE col IS NULL` is the shape that
   works; `SET counter = counter + 1` is the shape that corrupts.

If any of those three is uncomfortable, the migration belongs in the normal
lane and takes its downtime honestly.

## Removing a column later

Two releases, never one. Release N adds and backfills here; release N+1 —
after every instance has finished the backfill — drops the old column in the
normal lane. That is the expand/contract pattern, and skipping the gap is how
you break the instance that upgraded slowly.
