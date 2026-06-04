package database

// migrationOneLeadPerCrew (v110) enforces the "at most one LEAD per crew"
// invariant at the database level via a partial unique index.
//
// Why: the create + promote paths in internal/api/agents_create.go and
// agents_update.go used a check-then-act sequence ("SELECT ... WHERE
// crew_id=? AND agent_role='LEAD'", then INSERT/UPDATE) with no DB
// constraint backing it. Two concurrent requests could both pass the
// SELECT (find no lead) and then both write a LEAD into the same crew —
// a classic TOCTOU race yielding two leads, which BuildLeadContext and
// the orchestrator assume can never happen.
//
// A partial unique index makes the invariant atomic and code-path
// independent: any second LEAD insert/promotion for a crew fails with a
// UNIQUE constraint violation, which the handlers translate to 409. The
// index is partial so it only constrains live LEAD rows — AGENT rows and
// soft-deleted (deleted_at IS NOT NULL) leads are exempt, so demoting /
// re-creating a lead works normally.
//
// Safe to apply on existing data: builtin crew templates and the seed
// path each create exactly one LEAD per crew, so no current install has a
// duplicate that would block the CREATE UNIQUE INDEX. IF NOT EXISTS keeps
// the migration idempotent.
const migrationOneLeadPerCrew = `
CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_one_lead_per_crew
    ON agents(crew_id)
    WHERE agent_role = 'LEAD' AND deleted_at IS NULL;
`
