-- Instance administrator's desired state of the bundled Craft applications
-- (#5315). One row per subject: the Word and Sheet applications and the
-- separate switch for agents working in them. A missing row means the default
-- (disabled). Turning an application off never touches a document: it only
-- refuses new documents and new editing, lets open sessions finish saving
-- until drain_until, and keeps every document readable.
CREATE TABLE craft_application_settings (
 subject TEXT PRIMARY KEY CHECK(subject IN ('wordcraft','gridcraft','agents')),
 desired TEXT NOT NULL CHECK(desired IN ('enabled','disabled')),
 drain_until TEXT,
 updated_by TEXT NOT NULL DEFAULT '' CHECK(length(updated_by) <= 256),
 updated_at TEXT NOT NULL,
 CHECK(drain_until IS NULL OR desired='disabled')
);
