-- The retention index also covers lookups by Board; do not keep its redundant
-- single-column prefix. Separate forward step preserves already-applied dev DBs.
DROP INDEX idx_board_files_board;
