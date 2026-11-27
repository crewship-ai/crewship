-- The versions of a workbook Sheet whose change no longer matches the workbook's XML package
-- signature: the signature is kept byte for byte, the person is told. Additive.
CREATE TABLE IF NOT EXISTS craft_version_signature_breaks (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 version INTEGER NOT NULL CHECK(version > 1),
 created_at TEXT NOT NULL,
 PRIMARY KEY(workspace_id, page_id, version)
);
CREATE INDEX IF NOT EXISTS craft_version_signature_breaks_page ON craft_version_signature_breaks(page_id);
