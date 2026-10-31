-- Ledger frontier marker for craft_native_proposals. That table's migration
-- (20261029165715) was merged with a version below the then-newest
-- 20261029173053, so a database that had only just applied it still reported
-- 20261029173053 as its highest version. A rollback to the previous binary
-- passed the version-skew guard, and a backup taken there omitted the table.
-- This no-op row sits above every shipped version, so any database that has
-- the table also reports a version the previous binary refuses to run on.
-- Nothing about the schema changes; the already applied migration is untouched.
SELECT 1;
