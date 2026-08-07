-- Scope mission identifier uniqueness to the workspace (#1733).
--
-- v38 shipped `CREATE UNIQUE INDEX idx_mission_identifier ON missions(identifier)`
-- with no workspace in the key, which made issue identifiers a single global
-- namespace on an instance that is otherwise multi-tenant. Every other
-- tenant-owned table keys uniqueness on the workspace — crews is
-- UNIQUE(workspace_id, slug) — and missions was the outlier.
--
-- Identifiers are generated per crew: the prefix comes from the crew's
-- issue_prefix (or the first three letters of its slug) and the number from
-- issue_counters, which is keyed by crew_id and therefore starts at 1 for every
-- new crew. So "ENG-1" is not an unusual identifier, it is the *first* one any
-- crew slugged eng- produces. The first workspace on an instance to create it
-- consumed it for all the others, and their create failed with
--
--     UNIQUE constraint failed: missions.identifier
--
-- That is a broken feature and a cross-tenant disclosure at the same time: the
-- only thing that error can mean is "a row you cannot see already owns this
-- name", returned to a caller with no path to that row.
--
-- The migration is a widening, so it cannot fail on existing data: the index it
-- replaces is strictly stronger than the one it creates. Any set of rows that
-- satisfied UNIQUE(identifier) satisfies UNIQUE(workspace_id, identifier) —
-- duplicates across workspaces cannot exist today by construction, because the
-- old index is what prevented them. No backfill, no repair, no data loss. If
-- CREATE UNIQUE INDEX below ever did fail on a real install, that would mean
-- the old index was missing, not that this scoping is wrong.
--
-- The new index is created before the old one is dropped, and both statements
-- run inside the migration's transaction, so there is no instant at which
-- identifier uniqueness is unenforced.
--
-- The partial predicate is carried across verbatim. Most missions are
-- orchestration runs with a NULL identifier; keeping them out of the index is
-- what stops it from indexing the whole table to constrain a minority of rows.

CREATE UNIQUE INDEX IF NOT EXISTS idx_mission_workspace_identifier
    ON missions (workspace_id, identifier)
    WHERE identifier IS NOT NULL;

DROP INDEX IF EXISTS idx_mission_identifier;
