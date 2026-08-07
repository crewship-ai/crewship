-- Attachments — one table for every file attached to anything (#1768, item 7).
--
-- ── Why ONE table, and why not a third one ─────────────────────────────────
--
-- Two attachment tables already existed and neither was ever read or written
-- by a single line of product code: `chat_attachments` (v57) and
-- `workspace_files` (v57). Only internal/backup names them. Meanwhile the chat
-- composer HAS shipped attachments since #? — ProxyHandler.AgentChatAttachment
-- streams the upload to storage and records NOTHING, so an attachment today has
-- no size, no checksum, no MIME type, no uploader, cannot be listed, and cannot
-- be deleted through any API. Dead schema next to an undocumented live path is
-- the exact defect #1768 item 8 raises against `workflow_states`.
--
-- So: one table, shaped to hold all three owners (issue, issue comment, chat),
-- and `chat_attachments` is DROPPED in the same migration. It is exactly and
-- entirely subsumed — same columns, one of the three owner arcs below — and
-- nothing has ever inserted into it, so the drop cannot lose a row. Leaving a
-- dead twin of the table that replaces it is the defect, not a precaution.
--
-- `workspace_files` is deliberately NOT dropped here, and that is a decision
-- rather than an oversight:
--
--   * it is not an attachments table. It is a per-workspace file INDEX keyed
--     UNIQUE (workspace_id, rel_path) — a path→metadata map, a different
--     concept that `attachments` does not subsume and could not hold (an
--     attachment has no rel_path and two attachments may share a name);
--   * it is nonetheless dead, and that belongs to #1768 item 8 (dead schema
--     that reads as a feature) rather than to item 7;
--   * it currently has a second, load-bearing job: it is the canary table three
--     v144 regression guards use to prove the timestamp normalisation has not
--     been reverted (migrate_v144_datetime_default_tform_test.go,
--     migrate_upgrade_path_populated_test.go, api/orderby_and_timestamp_test.go).
--     Dropping it means re-pointing those guards at another pre-v144 table, and
--     doing that quietly inside a change about attachments is how a guard gets
--     weakened without anyone reading the diff that weakened it.
--
-- The plan, so this is a deferral and not an abandonment: item 8 drops
-- `workspace_files` together with the other dead schema it enumerates, and
-- moves the v144 canary to a table that is alive, in a change whose whole
-- subject is that move.
--
-- ── The owner is an EXCLUSIVE ARC, not a bare (owner_type, owner_id) pair ──
--
-- The obvious polymorphic shape is `owner_type TEXT, owner_id TEXT` with no
-- foreign key. It was rejected: issues are HARD-deleted (issue_handler_update.go
-- deletes BACKLOG/CANCELLED missions outright; crews_query.go deletes a crew's
-- missions wholesale), so a bare owner_id would leave attachment rows pointing
-- at ids that resolve to nothing — the decay mission_comment_mentions' FK was
-- added specifically to avoid.
--
-- Instead each owner kind gets its own nullable, foreign-keyed column and a
-- CHECK enforces that exactly one is set and that it agrees with `owner_type`.
-- `owner_type` is therefore redundant with the arc by construction; it is kept
-- because every read path filters on it and a stored discriminator is cheaper
-- and clearer than a three-way COALESCE in each query.
--
-- The cost is honest and worth naming: adding a fourth owner kind needs a
-- column plus a CHECK rewrite (SQLite cannot ALTER a CHECK, so that is a table
-- rebuild). That is a rarer event than an orphaned row, and a rebuild is a
-- migration someone writes deliberately rather than a class of bug nobody sees.
--
-- Which arcs have a PRODUCER as of this migration, stated plainly so nobody has
-- to grep for it:
--
--   mission_id  yes — internal/api/issue_attachments.go (human + CLI) and
--               issue_attachments_internal.go (agent, via the sidecar)
--   chat_id     yes — internal/api/proxy_attachments.go. The chat composer has
--               been storing blobs for releases and recording NOTHING; that call
--               site now writes this row. Note its blob is NOT content-addressed:
--               it stays at <crewID>/<agentSlug>/attachments/<chatId>/<filename>
--               because that path is the agent-visible contract. `storage_key` is
--               the authority on where any given attachment lives, which is why
--               it is a column rather than something derived on read.
--   comment_id  NO producer yet. It is declared because attaching to a comment is
--               the nearest next producer and adding it later costs the table
--               rebuild above; the migration tests exercise it, so it is schema
--               with a passing test rather than schema nothing can use. If it is
--               still unproduced when someone next reads this, that is a reason
--               to add the route or drop the column — not to leave it unread.
--
-- ── Blobs are content-addressed, and de-duplication stops at the tenant ────
--
-- storage_key is derived ENTIRELY from bytes we computed:
--
--     attachments/<workspace_id>/<sha256[0:2]>/<sha256>
--
-- No component of it comes from the uploader. A filename of "../../etc/passwd"
-- is stored in `filename` as a display label and never touches the path, so
-- traversal on the write path is impossible by construction rather than by
-- validation. `filename` is still sanitised to a basename before it is stored,
-- because it is echoed in Content-Disposition on the download.
--
-- Two owners that upload identical bytes IN THE SAME WORKSPACE share one blob
-- and get two rows. Identical bytes in two DIFFERENT workspaces get two blobs.
-- That is deliberate:
--
--   * a blob shared across tenants makes "erase this workspace" undecidable —
--     the unlink would have to consult another tenant's rows, and a tenant that
--     refuses to delete because a stranger holds the same file is not a
--     defensible answer to a deletion request;
--   * write-time de-duplication that crosses a tenant boundary is an existence
--     oracle. Upload a file, observe whether the store already had it, and you
--     have learned that some other workspace holds those exact bytes. For a
--     leaked document or a known-plaintext probe that is a real disclosure.
--
-- Within one workspace neither objection applies: the reader already has access
-- to the whole tenant.
--
-- Deletion is refcounted, in the application, at delete time: the row goes,
-- then the blob is unlinked only if no other row in the same workspace still
-- names that sha256. The partial UNIQUE indexes below make "the same bytes
-- attached twice to one owner" a single row at the DATA level, so the refcount
-- cannot be inflated by a client that retries an upload.
--
-- TWO CORRECTIONS to that paragraph landed before this migration shipped; they
-- are recorded here rather than silently diverging from it:
--
--   * the refcount is over (workspace, sha256, content-addressed storage_key),
--     not over the digest alone. A chat row carries the same digest for a blob
--     that lives somewhere else entirely, and counting it pinned an issue
--     attachment's bytes forever;
--   * the de-duplication key gains `filename` in
--     20260806214500_attachments_dedupe_filename.sql. Two byte-identical files a
--     user named apart are two attachments; the retry this key exists to
--     collapse is same-owner, same-bytes AND same-name.
--
-- Rows removed by FK CASCADE (an issue hard-deleted, a workspace wiped) do NOT
-- run that refcount — SQLite deletes them without the application seeing it, so
-- their blobs stay on disk until a reclaim pass runs. reclaimAttachmentBlobs in
-- internal/api/attachments.go is that pass. It is NOT "safe to run at any time"
-- for free, as this header first claimed: "a blob is garbage iff no row names
-- it" holds of a quiesced store and not of a live one, because an upload writes
-- the blob and inserts the row as two steps. The pass therefore checks each file
-- under the same (workspace, digest) lock the upload holds across both steps.
-- The issue-delete handler does not use the sweep at all: it reads the digests it
-- is about to orphan and unlinks exactly those. The crew/workspace wipe paths
-- reclaim nothing yet, and that is written down in the guide rather than implied.
--
-- created_at uses the ISO T-form DEFAULT, not `datetime('now')` — v144
-- converted every legacy space-form DEFAULT because ' ' sorts before 'T' and a
-- new table must not reintroduce the third shape.

CREATE TABLE IF NOT EXISTS attachments (
    id           TEXT NOT NULL PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,

    -- The exclusive arc. Exactly one of the three is non-NULL, and owner_type
    -- names which.
    owner_type TEXT NOT NULL,
    mission_id TEXT REFERENCES missions(id)         ON DELETE CASCADE,
    comment_id TEXT REFERENCES mission_comments(id) ON DELETE CASCADE,
    chat_id    TEXT REFERENCES chats(id)            ON DELETE CASCADE,

    -- Display metadata. `filename` is the uploader's label, basename-only.
    -- `content_type` is OURS: it is resolved from the extension against an
    -- allowlist, never copied from the request's Content-Type, which is
    -- attacker-chosen and is what turns a stored file into stored XSS.
    filename     TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size_bytes   INTEGER NOT NULL,
    sha256       TEXT NOT NULL,
    storage_key  TEXT NOT NULL,

    -- Who attached it. Exactly one of the two is set in practice: a human
    -- through the public API/CLI, or an agent through the sidecar. Both are
    -- ON DELETE SET NULL for the reason mission_code_links documents — an
    -- Art. 17 erasure must not be blocked by an attached file.
    uploaded_by_user_id  TEXT REFERENCES users(id)  ON DELETE SET NULL,
    uploaded_by_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,

    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),

    CHECK (owner_type IN ('issue', 'comment', 'chat')),
    CHECK (
        (CASE WHEN mission_id IS NOT NULL THEN 1 ELSE 0 END) +
        (CASE WHEN comment_id IS NOT NULL THEN 1 ELSE 0 END) +
        (CASE WHEN chat_id    IS NOT NULL THEN 1 ELSE 0 END) = 1
    ),
    CHECK (
        (owner_type = 'issue'   AND mission_id IS NOT NULL) OR
        (owner_type = 'comment' AND comment_id IS NOT NULL) OR
        (owner_type = 'chat'    AND chat_id    IS NOT NULL)
    ),
    CHECK (size_bytes >= 0),
    CHECK (length(sha256) = 64)
);

-- De-duplication, per owner, at the DATA level.
--
-- These are PARTIAL unique indexes rather than one UNIQUE (owner_type,
-- mission_id, comment_id, chat_id, sha256) table constraint, because SQLite
-- treats NULLs as distinct in a unique index: two issue attachments both carry
-- comment_id IS NULL and chat_id IS NULL, so the combined constraint would
-- never fire and the de-duplication would exist only in the comment above it.
CREATE UNIQUE INDEX IF NOT EXISTS idx_attachments_dedupe_mission
    ON attachments(mission_id, sha256) WHERE mission_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_attachments_dedupe_comment
    ON attachments(comment_id, sha256) WHERE comment_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_attachments_dedupe_chat
    ON attachments(chat_id, sha256) WHERE chat_id IS NOT NULL;

-- "what is attached to this issue" — the card's and the agent's read path.
CREATE INDEX IF NOT EXISTS idx_attachments_mission ON attachments(mission_id);
CREATE INDEX IF NOT EXISTS idx_attachments_comment ON attachments(comment_id);
CREATE INDEX IF NOT EXISTS idx_attachments_chat    ON attachments(chat_id);

-- Workspace scoping for the backup dump's generic filter, and the first half of
-- the refcount lookup on delete.
CREATE INDEX IF NOT EXISTS idx_attachments_workspace ON attachments(workspace_id);

-- The refcount itself: "does any other row in this workspace still name these
-- bytes?". Without it every delete is a workspace-wide scan.
CREATE INDEX IF NOT EXISTS idx_attachments_blob ON attachments(workspace_id, sha256);

-- The dead predecessor. Never read or written by product code, and exactly
-- subsumed by the owner_type='chat' arc above — see the header for why
-- `workspace_files` is not dropped alongside it.
DROP TABLE IF EXISTS chat_attachments;
