ALTER TABLE crews ADD COLUMN allow_codex_outer_sandbox INTEGER NOT NULL DEFAULT 0 CHECK (allow_codex_outer_sandbox IN (0, 1));
