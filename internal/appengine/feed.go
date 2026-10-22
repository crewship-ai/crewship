package appengine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// MaxFeedLimit bounds one page of the commits feed.
const MaxFeedLimit = 200

// DefaultFeedLimit is the page size when the caller names none.
const DefaultFeedLimit = 100

// ErrSnapshotRequired reports that the commits feed cannot serve the requested
// revision range: the client must reread the workbook snapshot.
var ErrSnapshotRequired = errors.New("snapshot_required")

// HistoryGoneError details a feed request outside the retained history. It
// matches ErrSnapshotRequired. MinSince is the oldest since the feed accepts.
type HistoryGoneError struct {
	HeadRev, Generation, MinSince int64
}

func (e *HistoryGoneError) Error() string        { return ErrSnapshotRequired.Error() }
func (e *HistoryGoneError) Is(target error) bool { return target == ErrSnapshotRequired }

// FeedCommit is one confirmed revision. AuthorityID is internal; the host
// shows other writers only by display label. SessionID and MutationID are set
// only for the caller's own revisions.
type FeedCommit struct {
	Rev         int64
	Kind        string // "cells", or "snapshot" for an import
	Own         bool
	AuthorityID string
	SessionID   string
	MutationID  string
	Cells       []CellInput
}

// Feed is one ascending page of confirmed revisions after a given revision.
type Feed struct {
	HeadRev, Generation, MinSince int64
	Commits                       []FeedCommit
}

// Commits reads confirmed revisions after since, in ascending order, under the
// same authorization as a workbook read. Revisions whose command history was
// pruned (or predates command history) cannot be replayed: a since before the
// newest such revision, or after the head, requires a snapshot reread.
func (s *Store) Commits(ctx context.Context, k Key, since int64, limit int, a Authorize) (f Feed, err error) {
	if since < 0 || limit < 1 || limit > MaxFeedLimit {
		return Feed{}, ErrInvalid
	}
	err = s.tx(ctx, k, a, func(tx *sql.Tx, p Principal) error {
		f = Feed{}
		if e := tx.QueryRowContext(ctx, `SELECT head_rev,generation FROM gridcraft_heads WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&f.HeadRev, &f.Generation); errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		} else if e != nil {
			return e
		}
		var gone sql.NullInt64
		if e := tx.QueryRowContext(ctx, `SELECT max(rev) FROM gridcraft_audit WHERE page_id=? AND command IS NULL`, k.PageID).Scan(&gone); e != nil {
			return e
		}
		f.MinSince = gone.Int64
		if since < f.MinSince || since > f.HeadRev {
			return &HistoryGoneError{HeadRev: f.HeadRev, Generation: f.Generation, MinSince: f.MinSince}
		}
		rows, e := tx.QueryContext(ctx, `SELECT rev,kind,authority_id,writer,mutation_id,command FROM gridcraft_audit WHERE page_id=? AND rev>? ORDER BY rev LIMIT ?`, k.PageID, since, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		f.Commits = []FeedCommit{}
		next := since + 1
		for rows.Next() {
			var c FeedCommit
			var writer, mutation string
			var command []byte
			if e = rows.Scan(&c.Rev, &c.Kind, &c.AuthorityID, &writer, &mutation, &command); e != nil {
				return e
			}
			// Revisions are dense; a gap or a missing command is corrupt history.
			if c.Rev != next || len(command) == 0 {
				return ErrInvalid
			}
			next++
			var record commandRecord
			if e = json.Unmarshal(command, &record); e != nil || record.Kind != c.Kind {
				return ErrInvalid
			}
			c.Cells = record.Cells
			c.Own = c.AuthorityID == p.AuthorityID
			if c.Own {
				c.MutationID = mutation
				if _, session, ok := strings.Cut(writer, "#session:"); ok {
					c.SessionID = session
				}
			}
			f.Commits = append(f.Commits, c)
		}
		return rows.Err()
	})
	if err != nil {
		var gone *HistoryGoneError
		if errors.As(err, &gone) {
			return Feed{HeadRev: gone.HeadRev, Generation: gone.Generation, MinSince: gone.MinSince}, err
		}
		return Feed{}, err
	}
	return f, nil
}

// Committed is a confirmed revision claimed for live delivery. It carries no
// cell content: subscribers read the commits feed under their own access.
type Committed struct {
	WorkspaceID, PageID string
	Rev, Generation     int64
	Kind                string
	Cells               int
	// Deliverable is false when the Page is no longer a personal private
	// workbook Page; such revisions are marked sent without a push.
	Deliverable bool
}

// ClaimCommitted marks up to limit undelivered revisions as sent, optionally
// only those of one Page, and returns them in revision order for a push. A
// revision is claimed once, so a crash after claiming loses only the push;
// clients recover it from the commits feed after a reconnect or a revision gap.
func (s *Store) ClaimCommitted(ctx context.Context, pageID string, limit int) ([]Committed, error) {
	if !s.options.Enabled {
		return nil, ErrDisabled
	}
	if limit < 1 {
		limit = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Take the writer lock first, so concurrent claimers serialize and each
	// revision is claimed exactly once.
	if _, err = tx.ExecContext(ctx, `UPDATE gridcraft_writer_fence SET seq=seq WHERE id=1`); err != nil {
		return nil, err
	}
	query := `SELECT page_id,rev FROM gridcraft_outbox WHERE state='pending' ORDER BY page_id,rev LIMIT ?`
	args := []any{limit}
	if pageID != "" {
		query = `SELECT page_id,rev FROM gridcraft_outbox WHERE state='pending' AND page_id=? ORDER BY rev LIMIT ?`
		args = []any{pageID, limit}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var claimed []Committed
	for rows.Next() {
		var c Committed
		if err = rows.Scan(&c.PageID, &c.Rev); err != nil {
			rows.Close()
			return nil, err
		}
		claimed = append(claimed, c)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for i := range claimed {
		c := &claimed[i]
		var command []byte
		err = tx.QueryRowContext(ctx, `SELECT h.workspace_id,h.generation,coalesce(a.kind,'snapshot'),a.command,
EXISTS(SELECT 1 FROM pages p WHERE p.id=h.page_id AND p.workspace_id=h.workspace_id AND p.owner_user_id IS NOT NULL AND p.owner_crew_id IS NULL AND p.private_audience=1 AND p.folder_id IS NULL)
FROM gridcraft_heads h LEFT JOIN gridcraft_audit a ON a.page_id=h.page_id AND a.rev=? WHERE h.page_id=?`, c.Rev, c.PageID).Scan(&c.WorkspaceID, &c.Generation, &c.Kind, &command, &c.Deliverable)
		if err != nil {
			return nil, err
		}
		var record commandRecord
		if len(command) > 0 && json.Unmarshal(command, &record) == nil {
			c.Cells = len(record.Cells)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE gridcraft_outbox SET state='sent' WHERE page_id=? AND rev=? AND state='pending'`, c.PageID, c.Rev); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}
