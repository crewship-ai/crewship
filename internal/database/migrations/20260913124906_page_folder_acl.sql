-- Pages folders — inherited permissions (F3′,
-- docs/prd/pages-folder-permissions-linux-model-2026-09-13.md §5, issue #2533).
--
-- A folder carries permissions `subject → can view / can edit` for a user, a
-- crew, or everyone in the workspace, and they apply CONTINUOUSLY to every page
-- that is in the folder right now: filing a page in a shared folder shares it,
-- taking it out unshares it. A folder permission adds to a page's own; it never
-- takes anything away.
--
-- grants_version becomes acl_version. It is the same fence a move carries
-- (§3/8) — it now changes with every change to the folder's ACL, which is the
-- thing it always stood for, and the wire and the CLI say `acl_version` too.
--
-- page_folder_acl. Three properties the schema states rather than trusts a
-- handler with:
--
--   * can_read is ALWAYS 1 (§3/2, "w ⇒ r"). A row exists (can view) or it
--     exists with can_write (can edit); "can edit without can view" is not a
--     state, in the data or on the wire. The handler answers 400 to a request
--     shape that tries to say otherwise; the CHECK is the same rule where the
--     handler cannot forget it.
--   * subject_type has no 'agent' (§3/1). An agent reaches a page only by a
--     grant on that page, named by a human; a folder ACL never widens what a
--     container in the crew can read.
--   * set_by_user_id is AUDIT, not authority (§3/5). A folder's permissions
--     belong to the folder and outlive the person who set them — the manager is
--     interchangeable, and "the sharing vanished when Petr left" would be a
--     surprise, not security. ON DELETE SET NULL keeps the entry and forgets
--     the setter. This is deliberately the opposite regime from page_grants,
--     whose issuer is re-checked at every use (pages_grants_authz.go).
--
-- subject_id is '' for the workspace subject; the CHECK pins it so the primary
-- key has exactly one row for "everyone here". No foreign key on subject_id,
-- for the reason page_grants gives (a polymorphic reference); the trigger below
-- is the one cascade §3/13 asks for — a deleted user's own `user:` rows go.
ALTER TABLE page_folders RENAME COLUMN grants_version TO acl_version;

CREATE TABLE page_folder_acl (
    folder_id      TEXT NOT NULL REFERENCES page_folders(id) ON DELETE CASCADE,
    subject_type   TEXT NOT NULL CHECK (subject_type IN ('user', 'crew', 'workspace')),
    subject_id     TEXT NOT NULL DEFAULT '',
    can_read       INTEGER NOT NULL DEFAULT 1 CHECK (can_read = 1),
    can_write      INTEGER NOT NULL DEFAULT 0 CHECK (can_write IN (0, 1)),
    set_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    set_at         TEXT NOT NULL,
    PRIMARY KEY (folder_id, subject_type, subject_id),
    CHECK ((subject_type = 'workspace') = (subject_id = ''))
);
CREATE INDEX idx_page_folder_acl_subject ON page_folder_acl(subject_type, subject_id);
CREATE INDEX idx_page_folder_acl_set_by ON page_folder_acl(set_by_user_id);

CREATE TRIGGER trg_page_folder_acl_user_deleted
AFTER DELETE ON users
BEGIN
    DELETE FROM page_folder_acl WHERE subject_type = 'user' AND subject_id = OLD.id;
END;

-- page_grants accepts the workspace subject (§3/14, option b): a page grant to
-- everyone in the workspace, for read and write only — never produce, which
-- names a producer and "everyone" is not one. SQLite cannot widen a CHECK in
-- place, so this is the standard rebuild (see 20260820074400 for the recipe
-- and why it is safe inside the wrapper transaction: nothing references
-- page_grants, so DROP TABLE fires no dependent's foreign key). Every row
-- makes the trip unchanged; the two indexes are recreated under their names.
CREATE TABLE page_grants_v2 (
    page_id            TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    subject_type       TEXT NOT NULL CHECK (subject_type IN ('user', 'crew', 'agent', 'workspace')),
    subject_id         TEXT NOT NULL DEFAULT '',
    level              TEXT NOT NULL CHECK (level IN ('read', 'produce', 'write')),
    panel_ids          TEXT,
    granted_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    granted_at         TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (page_id, subject_type, subject_id, level),
    CHECK (panel_ids IS NULL OR level = 'produce'),
    CHECK (subject_type <> 'workspace' OR (subject_id = '' AND level IN ('read', 'write')))
);

INSERT INTO page_grants_v2 (page_id, subject_type, subject_id, level, panel_ids, granted_by_user_id, granted_at)
SELECT page_id, subject_type, subject_id, level, panel_ids, granted_by_user_id, granted_at
FROM page_grants;

DROP TABLE page_grants;

ALTER TABLE page_grants_v2 RENAME TO page_grants;

CREATE INDEX IF NOT EXISTS idx_page_grants_subject ON page_grants(subject_type, subject_id);
CREATE INDEX IF NOT EXISTS idx_page_grants_granted_by ON page_grants(granted_by_user_id);
