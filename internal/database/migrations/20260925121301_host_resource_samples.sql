-- One host reading per minute for the dashboard's 24h/7d/30d resource view.
-- These are instance-wide measurements, not per-workspace agent usage.
CREATE TABLE IF NOT EXISTS host_resource_samples (
    ts TEXT PRIMARY KEY,
    cpu_percent REAL NOT NULL CHECK(cpu_percent >= 0 AND cpu_percent <= 100),
    memory_percent REAL NOT NULL CHECK(memory_percent >= 0 AND memory_percent <= 100),
    memory_used_mb INTEGER NOT NULL CHECK(memory_used_mb >= 0),
    memory_total_mb INTEGER NOT NULL CHECK(memory_total_mb > 0)
);
