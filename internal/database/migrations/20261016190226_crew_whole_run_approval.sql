ALTER TABLE crews ADD COLUMN whole_run_approval INTEGER NOT NULL DEFAULT 0 CHECK (whole_run_approval IN (0,1));
