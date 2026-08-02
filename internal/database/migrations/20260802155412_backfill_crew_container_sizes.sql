-- Rows written with container_memory_mb = 0 still resolve to 8192 (#1643).
--
-- `PATCH /crews/{id}` with container_memory_mb: 0 used to write the literal 0.
-- The "0 = use the server default" sentinel was resolved on create and not on
-- update, so the two paths disagreed about what the same flag meant. #1641
-- made both resolve it, and no new row can hold 0 — but the rows already
-- holding it were left alone, and every downstream consumer asks `<= 0` and
-- substitutes its own default:
--
--   * the docker provider substitutes 8192, i.e. TWICE what the create path,
--     the schema column (NOT NULL DEFAULT 4096) and the docs all promise;
--   * computeCrewBudget treats a non-positive value as "no answer" and hands
--     the crew a concurrency budget of 1 instead of 2.
--
-- 4096 / 2.0 are not new numbers: they are defaultCrewContainerMemoryMB and
-- defaultCrewContainerCPUs (internal/api/crew_resource_policy.go), the column
-- DEFAULTs in the initial schema, and what docs/api-reference/crews.mdx,
-- docs/cli/crew.mdx and docs/manifest/crew.md publish.
--
-- `<= 0` rather than `= 0` on purpose. Validation rejects a negative today,
-- but a row written before that check reaches the identical fallback, and a
-- negative would be rejected outright by the daemon.
--
-- Idempotent by construction: the predicate cannot match a row this statement
-- has already fixed. Deliberately NOT a blanket normalisation — a crew sized
-- at 2048 on purpose is left exactly where its operator put it.
UPDATE crews
   SET container_memory_mb = 4096
 WHERE container_memory_mb <= 0;

UPDATE crews
   SET container_cpus = 2.0
 WHERE container_cpus <= 0;
