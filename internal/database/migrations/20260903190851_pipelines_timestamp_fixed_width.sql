-- #2294: internal/pipeline/store.go writes created_at, updated_at and
-- last_invoked_at (pipelines) and created_at (pipeline_versions) with
-- time.RFC3339Nano, which TRIMS trailing zero fractional digits. Two
-- instants in the same second can then serialise to strings of different
-- length, and List's `ORDER BY COALESCE(last_invoked_at, created_at) DESC`
-- compares them as TEXT: 'Z' (0x5A) sorts after '0' (0x30), so the shorter
-- (trimmed) string of the EARLIER instant sorts after the longer string of
-- a LATER one, and DESC then puts the earlier row first.
--
-- store.go itself is fixed in the same change to write a fixed 9-digit
-- fraction going forward (internal/tsformat.Format, already used by every
-- other pipeline.* store — see internal/tsformat's own doc comment for the
-- byte-for-byte reasoning). This migration pads the rows already on disk to
-- match, so an old (trimmed) row and a new (fixed-width) row compare
-- correctly against each other instead of only among themselves.
--
-- Every value store.go has ever written is UTC with a literal 'Z' — never a
-- numeric offset, never SQLite's `datetime('now','subsec')` space form
-- (nothing inserts into these two tables outside store.go, so that column
-- DEFAULT has never actually fired) — so padding only needs to handle
-- "…18Z" (no fraction) and "…18.1Z" .. "…18.100000000Z" (1-9 digit
-- fraction). The WHERE guard restricts to exactly that shape; anything else
-- is left untouched rather than mangled.
--
-- Pure SQL string surgery, no Go round-trip needed:
--   1. no '.' at all           -> insert '.000000000' before the trailing Z
--   2. '.' present, N<9 digits -> right-pad the fraction with zeros to 9
--   3. '.' present, 9 digits   -> already fixed-width; the formula below is
--                                 a no-op on it (idempotent, so a partial or
--                                 re-run migration never double-pads)
--
-- WHERE col NOT GLOB pattern skips rows already at the target width, so a
-- re-run after a partial apply only touches what is left.

UPDATE pipelines
SET created_at = CASE
    WHEN instr(created_at, '.') = 0
        THEN substr(created_at, 1, length(created_at) - 1) || '.000000000Z'
    ELSE
        substr(created_at, 1, instr(created_at, '.')) ||
        substr(substr(created_at, instr(created_at, '.') + 1,
                       length(created_at) - instr(created_at, '.') - 1) || '000000000', 1, 9) ||
        'Z'
    END
WHERE created_at LIKE '____-__-__T__:__:__%Z'
  AND created_at NOT GLOB '*.[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z';

UPDATE pipelines
SET updated_at = CASE
    WHEN instr(updated_at, '.') = 0
        THEN substr(updated_at, 1, length(updated_at) - 1) || '.000000000Z'
    ELSE
        substr(updated_at, 1, instr(updated_at, '.')) ||
        substr(substr(updated_at, instr(updated_at, '.') + 1,
                       length(updated_at) - instr(updated_at, '.') - 1) || '000000000', 1, 9) ||
        'Z'
    END
WHERE updated_at LIKE '____-__-__T__:__:__%Z'
  AND updated_at NOT GLOB '*.[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z';

UPDATE pipelines
SET last_invoked_at = CASE
    WHEN instr(last_invoked_at, '.') = 0
        THEN substr(last_invoked_at, 1, length(last_invoked_at) - 1) || '.000000000Z'
    ELSE
        substr(last_invoked_at, 1, instr(last_invoked_at, '.')) ||
        substr(substr(last_invoked_at, instr(last_invoked_at, '.') + 1,
                       length(last_invoked_at) - instr(last_invoked_at, '.') - 1) || '000000000', 1, 9) ||
        'Z'
    END
WHERE last_invoked_at LIKE '____-__-__T__:__:__%Z'
  AND last_invoked_at NOT GLOB '*.[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z';

-- pipeline_versions.created_at is not compared/ordered by any query today
-- (version history sorts by the integer `version` column), but it is the
-- sibling table this same store writes with the same trimmed format, so it
-- is padded here too rather than left as a landmine for the next query that
-- orders by it.
UPDATE pipeline_versions
SET created_at = CASE
    WHEN instr(created_at, '.') = 0
        THEN substr(created_at, 1, length(created_at) - 1) || '.000000000Z'
    ELSE
        substr(created_at, 1, instr(created_at, '.')) ||
        substr(substr(created_at, instr(created_at, '.') + 1,
                       length(created_at) - instr(created_at, '.') - 1) || '000000000', 1, 9) ||
        'Z'
    END
WHERE created_at LIKE '____-__-__T__:__:__%Z'
  AND created_at NOT GLOB '*.[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z';
