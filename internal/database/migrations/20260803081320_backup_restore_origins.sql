-- Record which bundle a workspace was restored FROM, so a disaster-recovery
-- restore can be finished.
--
-- crewship#1716: the CLI prints, verbatim, "provision the new crews with
-- `crewship crew provision` and re-run restore without the rewrite flag to
-- land container state". That second run is rejected 100% of the time. The
-- guard it hits (internal/api/backup.go allowRestore) only lets a restore
-- through when the caller's workspace matches the BUNDLE's workspace by id or
-- by slug, or when the instance has no workspaces at all. A workspace created
-- by `restore --as-workspace X` / `--as-crew X` has a freshly minted id and a
-- slug taken from the flag, so it can never match either — and by then the
-- instance is not empty, because that same restore just populated it.
--
-- So the only step that can land the crews' files is the one step the guard
-- forbids, and the operator is left with a workspace whose DB rows are
-- complete and whose crews are empty. That is the whole disaster-recovery
-- path, and it has never worked.
--
-- Relaxing the guard is not the fix. It exists to stop one tenant's bundle
-- being unpacked into another tenant's workspace, and a flag that says "no
-- really, let me" would delete the boundary rather than move it. What was
-- actually missing is the fact that makes the second restore legitimate: this
-- workspace came from this bundle. Recorded here at rewrite time, it lets
-- allowRestore answer the question on evidence instead of on a coincidence of
-- slugs.
--
-- crew_slug_map is the second half. --as-crew renames the crew, and the
-- manifest keeps the ORIGINAL slug, so a resume that resolved containers from
-- the manifest would address the SOURCE crew's container — on a same-instance
-- restore, that is the source crew's live data being overwritten by its own
-- backup's siblings. The map is the bundle's crew slug -> the crew row this
-- instance created for it, so the resume writes to the crew it made.
--
-- Instance-local by construction: classified IntentExcludeOperational in
-- internal/backup/intent.go, because a bundle carrying its own restore
-- provenance into the next instance would be asserting a lineage that
-- instance never had.

CREATE TABLE IF NOT EXISTS backup_restore_origins (
    workspace_id  TEXT PRIMARY KEY,
    bundle_sha256 TEXT NOT NULL,
    -- Advisory only. The path a bundle was restored from is useful in an
    -- incident write-up and is never used to authorise anything; the sha256
    -- is what identity is decided on, because a path can be re-pointed at a
    -- different file and a payload digest cannot.
    bundle_path   TEXT NOT NULL DEFAULT '',
    -- JSON object: {"<bundle crew slug>": {"id": "<crew id>", "slug": "<crew slug>"}}
    crew_slug_map TEXT NOT NULL DEFAULT '{}',
    restored_at   TEXT NOT NULL,
    restored_by   TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
);

-- allowRestore looks the row up by (workspace_id) and re-checks the digest, so
-- the PK covers the authorisation path. This index serves the other direction:
-- "which workspaces came out of this bundle", which is the question asked when
-- a bundle turns out to have been corrupt.
CREATE INDEX IF NOT EXISTS idx_backup_restore_origins_sha256
    ON backup_restore_origins(bundle_sha256);
