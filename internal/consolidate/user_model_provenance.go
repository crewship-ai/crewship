package consolidate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// #1693 — provenance beside the operator-model file, never inside it.
//
// The model file is capped at memory.UserModelCapBytes and read into every
// agent prompt, so the evidence span usermodel.Verify matched each fact
// against was discarded once the check passed. A person reading their model
// could see WHAT was stored but never WHERE it came from, and the only
// remedy for an entry that looked wrong but might not be was to delete it.
//
// This file is the store the issue asks for: one row per verified fact per
// sync, in user_model_provenance. Three rules, each the reason for a shape
// below:
//
//   - APPEND, never mutate. A re-sync that restates a key writes a new row;
//     readers resolve the newest per key. A row is never UPDATEd, so the
//     history of what the person said and when is the history — a later
//     correction is visible as a later row, not as a rewrite.
//   - Written only where the file is written. The sweep's dry-run seam
//     (#1702) writes nothing, and neither does this; a provenance row for a
//     fact that never reached the file would be evidence for nothing.
//   - Purged wherever the file is purged. Every one of the four delete paths
//     (opt-out, self-service delete, per-field forget, the Art. 17 cascade)
//     reaches this table, or it becomes the fifth place a SAR erase misses —
//     the exact bug #1669 found in the cascade for user_models itself.

// UserModelEvidence is one verified fact's provenance as the extractor
// hands it to the sweep: the quote usermodel.Verify found in the subject's
// own words, the conversation_messages.id of the turn it was found in, and
// the source type the profile admitted.
//
// Source is a string rather than usermodel.SourceType because this package
// cannot import usermodel (usermodel imports it). The value IS that type's
// vocabulary, and the table's CHECK constraint refuses anything else.
type UserModelEvidence struct {
	Key       string
	Value     string
	Quote     string
	MessageID string
	Source    string
}

// UserModelProvenance is one resolved evidence row as the readers hand it
// back — the newest row for a fact.
type UserModelProvenance struct {
	Key        string
	Value      string
	Quote      string
	MessageID  string
	SourceType string
	RecordedAt string
}

// sqlExecer is what the writers and purges need: *sql.DB or *sql.Tx.
type sqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// sqlQuerier is what the reader needs: *sql.DB or *sql.Tx.
type sqlQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// provenanceTimestamp renders the fixed-width millisecond T-form every
// ordered column in the schema uses; the newest-row read is a string
// comparison on it, so the width has to be stable.
func provenanceTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// RecordUserModelProvenance appends one row per evidence entry for the
// model at (workspaceID, userSlug). Returns how many were written.
//
// Rows are written in one transaction, so a sync whose model file was
// written has either all of its evidence or none of it — half a sync's
// provenance would say a fact came from nowhere. Entries with an empty key
// or quote are skipped rather than stored: a row without a quote is
// exactly the undeclared inference Verify exists to refuse, and cannot
// have come from it.
//
// An entry identical to the newest row for its key — same value, same
// quote, same message — is not appended. The sweep reads a fortnight of
// transcript every day, so the same sentence is re-found for up to
// fourteen syncs; fourteen rows saying the same thing would not be
// fourteen pieces of evidence, and the row that stays is the one dated
// when the person first said it, which is the date a person asking "when
// did I say that?" wants. Anything that differs — a new value, a new
// quote, the same words said again in a later message — is appended.
func RecordUserModelProvenance(
	ctx context.Context,
	db *sql.DB,
	workspaceID, userID, userSlug string,
	evidence []UserModelEvidence,
	now time.Time,
) (int, error) {
	if db == nil || len(evidence) == 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("provenance: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	at := provenanceTimestamp(now)
	n := 0
	for _, ev := range evidence {
		key := strings.ToLower(strings.TrimSpace(ev.Key))
		quote := strings.TrimSpace(ev.Quote)
		if key == "" || quote == "" {
			continue
		}
		value := strings.TrimSpace(ev.Value)
		messageID := strings.TrimSpace(ev.MessageID)
		var lastValue, lastQuote, lastMessage string
		err := tx.QueryRowContext(ctx, `
			SELECT value, quote, message_id FROM user_model_provenance
			WHERE workspace_id = ? AND user_slug = ? AND key = ?
			ORDER BY recorded_at DESC, rowid DESC LIMIT 1
		`, workspaceID, userSlug, key).Scan(&lastValue, &lastQuote, &lastMessage)
		switch {
		case err == nil && lastValue == value && lastQuote == quote && lastMessage == messageID:
			continue
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return 0, fmt.Errorf("provenance: read newest %q: %w", key, err)
		}
		source := strings.ToLower(strings.TrimSpace(ev.Source))
		if source == "" {
			// The extractor admitted it, and every shipped profile admits
			// only stated facts. Recording an empty source would fail the
			// CHECK and lose the whole sync's evidence over a field the
			// caller did not bother to fill.
			source = "stated"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_model_provenance
			    (id, workspace_id, user_id, user_slug, key, value, quote, message_id, source_type, recorded_at)
			VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, workspaceID, userID, userSlug, key, value, quote, messageID, source, at); err != nil {
			return 0, fmt.Errorf("provenance: insert %q: %w", key, err)
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("provenance: commit: %w", err)
	}
	return n, nil
}

// LoadUserModelProvenance resolves the newest evidence row per key for the
// model at (workspaceID, userSlug). Keys with no row — a fact written before
// this store existed — are simply absent from the map; the caller shows
// the fact without provenance rather than inventing some.
func LoadUserModelProvenance(ctx context.Context, db sqlQuerier, workspaceID, userSlug string) (map[string]UserModelProvenance, error) {
	if db == nil || workspaceID == "" || userSlug == "" {
		return map[string]UserModelProvenance{}, nil
	}
	// Newest first; the first row seen per key wins. rowid breaks the tie
	// between two rows the same sync stamped identically.
	rows, err := db.QueryContext(ctx, `
		SELECT key, value, quote, message_id, source_type, recorded_at
		FROM user_model_provenance
		WHERE workspace_id = ? AND user_slug = ?
		ORDER BY recorded_at DESC, rowid DESC
	`, workspaceID, userSlug)
	if err != nil {
		return nil, fmt.Errorf("provenance: query: %w", err)
	}
	defer rows.Close()

	out := map[string]UserModelProvenance{}
	for rows.Next() {
		var p UserModelProvenance
		if err := rows.Scan(&p.Key, &p.Value, &p.Quote, &p.MessageID, &p.SourceType, &p.RecordedAt); err != nil {
			return nil, fmt.Errorf("provenance: scan: %w", err)
		}
		if _, seen := out[p.Key]; seen {
			continue
		}
		out[p.Key] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("provenance: iterate: %w", err)
	}
	return out, nil
}

// PurgeUserModelProvenance removes every evidence row for the model at
// (workspaceID, userSlug) — the companion of every whole-model delete.
// Returns how many rows went.
func PurgeUserModelProvenance(ctx context.Context, db sqlExecer, workspaceID, userSlug string) (int64, error) {
	if db == nil || workspaceID == "" || userSlug == "" {
		return 0, nil
	}
	res, err := db.ExecContext(ctx, `
		DELETE FROM user_model_provenance WHERE workspace_id = ? AND user_slug = ?
	`, workspaceID, userSlug)
	if err != nil {
		return 0, fmt.Errorf("provenance: purge: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PurgeUserModelProvenanceKey removes the evidence rows for ONE field —
// the companion of the per-field forget. Every row for the key goes, not
// just the newest: forgetting a field is the person saying the record
// about it is wrong, and the history of how it got there is part of that
// record.
func PurgeUserModelProvenanceKey(ctx context.Context, db sqlExecer, workspaceID, userSlug, key string) (int64, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	if db == nil || workspaceID == "" || userSlug == "" || key == "" {
		return 0, nil
	}
	res, err := db.ExecContext(ctx, `
		DELETE FROM user_model_provenance WHERE workspace_id = ? AND user_slug = ? AND key = ?
	`, workspaceID, userSlug, key)
	if err != nil {
		return 0, fmt.Errorf("provenance: purge key %q: %w", key, err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
