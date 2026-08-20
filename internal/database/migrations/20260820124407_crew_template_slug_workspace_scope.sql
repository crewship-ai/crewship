-- Scope crew template slug uniqueness to the workspace (#1796).
--
-- v23 declared the table with
--
--     slug TEXT NOT NULL UNIQUE
--
-- and v26 later bolted a tenant onto it —
--
--     ALTER TABLE crew_templates ADD COLUMN workspace_id TEXT
--         REFERENCES workspaces(id) ON DELETE CASCADE
--
-- — without ever rescoping that UNIQUE. So the column that says which tenant
-- owns a template plays no part in the constraint that decides whether the
-- name is free: one global namespace on a table that is otherwise
-- workspace-owned, exactly the shape #1733 fixed for missions.identifier.
--
-- # Why this is a rebuild and #1733 was two statements
--
-- missions.identifier was constrained by a NAMED index (`CREATE UNIQUE INDEX
-- idx_mission_identifier`), so the fix was create-the-new-one, DROP INDEX the
-- old one. Here the UNIQUE is a COLUMN CONSTRAINT, which SQLite implements as
-- an implicit `sqlite_autoindex_crew_templates_2`. Implicit indexes cannot be
-- dropped — `DROP INDEX sqlite_autoindex_...` is an error — and SQLite has no
-- ALTER TABLE that removes a column constraint. The only way to unsay it is
-- the standard rebuild: CREATE new / copy / DROP old / RENAME.
--
-- The rebuild is safe inside the migration runner's wrapper transaction. The
-- recipe that is NOT (see migrate.go's fnNoTx contract) fails because DROP
-- TABLE fires the DEPENDENTS' foreign keys; crew_templates has no dependents —
-- `REFERENCES crew_templates` has zero hits in the schema. Templates are
-- copied FROM, never pointed AT: a deployed crew carries no template_slug
-- column (see internal/manifest/kinds/crew_template.go, which reconstructs
-- provenance by matching slugs precisely because the column does not exist).
--
-- # What replaces it
--
-- Two partial unique indexes, splitting the row population the way the code
-- already reads it:
--
--   * workspace_id IS NOT NULL — a workspace-owned template. UNIQUE per
--     (workspace_id, slug), so two tenants may both hold `backend-team`.
--   * workspace_id IS NULL — a builtin seeded from the embedded YAML
--     (seed_crew_templates.go writes workspace_id NULL). Those share one
--     namespace, and UNIQUE(slug) keeps the seeder's update-then-insert
--     idempotent.
--
-- Together they are what the old constraint should have been. Note this is a
-- widening in one direction only: any row set that satisfied the global
-- UNIQUE(slug) satisfies both of these by construction, so the copy below
-- cannot fail on existing data. There is no backfill and no row class to
-- drop — unlike the issue_counters rebuild, every row makes the trip.
--
-- # Precedence: a workspace template SHADOWS a builtin of the same slug
--
-- The old constraint did double duty: it also guaranteed that
--
--     WHERE slug = ? AND (is_builtin = 1 OR workspace_id = ?)
--
-- matched at most one row, which is why four QueryRow sites (crew_templates.go
-- Get + deployCrewTemplate, agents_hire.go lookupCrewTemplate,
-- onboarding.go's crew-name default) could read it with no tie-break. After
-- this migration a builtin and a workspace template can both match, so the
-- rule has to be stated rather than inherited from the schema:
--
--     the more specific row wins — a workspace template of slug X shadows the
--     builtin of slug X for that workspace, and only for that workspace.
--
-- That is what lets an operator customise a shipped template without renaming
-- it. The API implements it as `ORDER BY (workspace_id IS NULL) LIMIT 1` on
-- every single-row lookup, and List drops the shadowed builtin so a slug
-- appears once. Documented in docs/guides/templates.mdx.
--
-- One behaviour change falls out of this and is worth stating: before, a user
-- template holding slug X permanently suppressed the seeding of builtin X (the
-- seeder's INSERT OR IGNORE hit the global UNIQUE and gave up, silently). Now
-- the builtin seeds correctly alongside it, and the user's row shadows it.
--
-- The `ON DELETE CASCADE` on workspace_id is carried across verbatim: it is
-- what removes a tenant's private templates when the workspace goes away.

CREATE TABLE crew_templates_v2 (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL,
    description TEXT,
    icon TEXT,
    color TEXT,
    category TEXT NOT NULL DEFAULT 'GENERAL',
    agents_json TEXT NOT NULL,
    is_builtin INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE
);

-- The timestamp DEFAULTs above are the ISO T-form, not v23's
-- `datetime('now')`: v144 already converted this table's created_at /
-- updated_at to that form, and rebuilding with the original literal would
-- silently undo it and reintroduce the mixed-format ordering bug #1073 closed.

INSERT INTO crew_templates_v2 (id, name, slug, description, icon, color,
                               category, agents_json, is_builtin,
                               created_at, updated_at, workspace_id)
SELECT id, name, slug, description, icon, color,
       category, agents_json, is_builtin,
       created_at, updated_at, workspace_id
FROM crew_templates;

DROP TABLE crew_templates;

ALTER TABLE crew_templates_v2 RENAME TO crew_templates;

-- The rebuild took the table's indexes down with it, so every index this
-- table still needs is recreated here — not just the new unique pair.
CREATE UNIQUE INDEX idx_crew_templates_workspace_slug
    ON crew_templates (workspace_id, slug)
    WHERE workspace_id IS NOT NULL;

CREATE UNIQUE INDEX idx_crew_templates_global_slug
    ON crew_templates (slug)
    WHERE workspace_id IS NULL;

CREATE INDEX idx_crew_templates_category ON crew_templates (category);
CREATE INDEX idx_crew_templates_workspace ON crew_templates (workspace_id);

-- idx_crew_templates_slug (v23) is deliberately NOT recreated. It was a plain
-- index on the same single column as the implicit unique autoindex the same
-- statement created, so on the old schema it never served a query the
-- autoindex could not — a duplicate that cost a write on every insert and
-- bought nothing.
--
-- Stating the trade-off plainly rather than leaving it implied: with both
-- slug indexes now partial, a probe that names ONLY slug can use
-- idx_crew_templates_global_slug just for the builtin half, and falls back to
-- a scan for the rest. That is deliberate. Every lookup in the codebase names
-- a workspace alongside the slug, the table holds the embedded builtin roster
-- plus whatever one tenant saved, and a scan of that is cheaper than carrying
-- a third slug index. If a slug-only hot path ever appears, add the index
-- then, with the query that justifies it.
