-- The attachment de-duplication key gains the filename (#1768 item 7, #1791).
--
-- ── What was wrong ────────────────────────────────────────────────────────
--
-- 20260806194500_attachments.sql keyed de-duplication on (owner, sha256). The
-- reasoning was sound for the case it was written against — a retried or
-- double-clicked upload must be one row, because the refcount that decides
-- whether a blob may be unlinked is exactly "how many rows name it" — but the
-- key it chose answers a different question than the one it was asked.
--
-- Two byte-identical files are not necessarily the same file:
--
--     crash-before-fix.log   \
--                             > identical bytes, opposite meanings
--     crash-after-fix.log    /
--
-- Attaching both to one issue hit the index on the second INSERT. attachBytes
-- read the UNIQUE and answered 200 with the row that was already there — under
-- the FIRST file's name — wrote no `attachment_added` activity row, and left the
-- user believing both files were attached. Nothing recorded that the second name
-- had ever been offered. Zero-byte files collided across every unrelated pair for
-- the same reason: same digest, and the key could see nothing else.
--
-- ── The key ───────────────────────────────────────────────────────────────
--
-- (owner, sha256, filename). It still collapses the retry that motivated the
-- original index — same owner, same bytes, same name is one row and one timeline
-- entry — and it stops collapsing two files a user deliberately named apart.
--
-- The refcount is unaffected in kind and slightly changed in value: two rows now
-- share one blob, so the first delete finds a reference and keeps the bytes, and
-- the second unlinks them. That is the shared-blob path the store already had for
-- two ISSUES holding one file; this only makes it reachable within one issue.
--
-- `filename` is the SANITISED basename (attachments.go), so the key is over a
-- value the store computed, not over whatever arrived in the multipart header.
-- Case is significant, as it is everywhere else in this schema: `Crash.log` and
-- `crash.log` are two names, and SQLite's default BINARY collation says so.
--
-- ── Safe to apply ─────────────────────────────────────────────────────────
--
-- Strictly a RELAXATION: every pair the new index refuses, the old one refused
-- too. No existing row can violate it, so there is nothing to reconcile and the
-- recreate cannot fail on live data.
--
-- All three arcs change together. The chat arc has the same defect for the same
-- reason — recordChatAttachment swallows a UNIQUE as "already attached", so two
-- differently-named chat uploads of identical bytes recorded ONE row while the
-- chat surface (which is not content-addressed and puts the filename in the path)
-- stored TWO files, leaving the second with no row at all. The comment arc has no
-- producer yet and is changed now so it does not ship with the defect the other
-- two are having removed.
--
-- The non-unique lookup indexes (idx_attachments_mission / _comment / _chat /
-- _workspace / _blob) are untouched: they serve reads and the refcount, and
-- neither question involves the filename.

DROP INDEX IF EXISTS idx_attachments_dedupe_mission;
DROP INDEX IF EXISTS idx_attachments_dedupe_comment;
DROP INDEX IF EXISTS idx_attachments_dedupe_chat;

CREATE UNIQUE INDEX IF NOT EXISTS idx_attachments_dedupe_mission
    ON attachments(mission_id, sha256, filename) WHERE mission_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_attachments_dedupe_comment
    ON attachments(comment_id, sha256, filename) WHERE comment_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_attachments_dedupe_chat
    ON attachments(chat_id, sha256, filename) WHERE chat_id IS NOT NULL;
