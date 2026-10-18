-- Private audience is independent of type and storage accounting. Shared Pages
-- preserve their existing ACL. Durable chat creation provenance classifies
-- already-created Documents without inspecting private text.
ALTER TABLE pages ADD COLUMN private_audience INTEGER NOT NULL DEFAULT 0
    CHECK (private_audience IN (0, 1));
UPDATE pages SET private_audience = 1
WHERE EXISTS (SELECT 1 FROM document_creations c
              WHERE c.page_id = pages.id AND c.workspace_id = pages.workspace_id);
